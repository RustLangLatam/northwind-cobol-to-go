// Package db2fake is an in-memory db2access.Driver for the tests of a generated Db2
// service. It is skeleton code, so a service test needs no database/sql import (the
// gate forbids one outside pkg/db2access) and never touches a real host.
//
// It evaluates the statements pkg/db2access builds with Db2's semantics (see sql.go for
// the exact grammar): WHERE with =, <>, <, <=, >, >=, IS NULL and IS NOT NULL joined by
// AND, a DECIMAL compared as a number, CHAR text compared blank-padded, a comparison
// with NULL never true; ORDER BY on several columns, ASC or DESC, stable, NULL sorting
// high; FETCH FIRST n ROWS ONLY; INSERT, and UPDATE and DELETE on the rows their WHERE
// matches, reporting that count.
//
// Text compares and sorts by the bytes of its column's CCSID (the *HEX sort sequence,
// the only one the driver can run: EBCDIC order for CCSID 37, UTF-8 order for 1208), a
// column with no CCSID takes the job CCSID of its connection (SetJobCCSID when declared,
// else Config.CCSID of the Open; a later Open changes no earlier store), text equal only
// through blank padding is not a tie in ORDER BY (more trailing blanks first), and GRAPHIC,
// VARGRAPHIC and DBCLOB (CCSID 1200 or 13488) order by UTF-16 code unit, count their length
// in code units and tie on trailing blanks (PUB400 round 2 and 3). A binary parameter longer
// than its column is refused as the i400 fork (v0.1.3, after F01) refuses it: fork v0.1.2
// cut a BINARY value silently, so db2access refuses it first (round 3). A rolled-back transaction undoes its own writes, in reverse, and no other
// connection's. Every behaviour is one measured on PUB400 (IBM i 7.5, job CCSID 273, *HEX):
// docs/evidence/pub400-db2fake-semantics-2026-11-24 (round 1) and
// docs/evidence/pub400-db2-semantics-round2-2026-11-24 (round 2) and
// docs/evidence/pub400-db2-semantics-round3-2026-11-24 (round 3), tabulated with their
// sections in ADR-0068.
//
// It keeps a DECIMAL as its exact text and a DATE, TIME or TIMESTAMP as the host's ISO
// text, and refuses a SELECT that reads either without the CAST (or
// CHAR(col, ISO)) that db2access always emits. Any SQL it cannot interpret is an error
// naming the fake, never a silent "every row", so a statement that bypassed the
// builders fails the test.
//
// The catalogue (catalogue.go): every table is declared with Declare, from the same
// db2access.Table the service uses, and a statement on an undeclared table or column
// fails. From the declaration the fake stores what Db2 for i stores: CHAR and BINARY
// padded to their length (VARCHAR and VARBINARY not), a value too long refused, NULL
// only in a nullable column, and in a column an INSERT omits its declared default (the
// DDS rule: a field without ALWNULL is NOT NULL WITH DEFAULT, so blanks, zeros or the
// DFT value; NULL only with ALWNULL; the clock of SetNow for CURRENT_*).
//
// Test helpers: Seed loads rows through a built INSERT without going through the store
// (so a test of a *CS service needs no unit of work to set up data), FailQuery and
// FailExec inject a database error, DBError carries the SQLSTATE and SQLCODE that
// db2access.Classify reads, and Statements lists the SQL the service sent.
//
// Known limits, each pinned by a test (ADR-0068): one lock and no isolation between
// concurrent transactions (another connection sees uncommitted rows; there is no lock
// wait, so no -913), and Seed is refused while a transaction is open. It suits
// deterministic unit tests, not concurrency tests. Without ORDER BY the fake returns
// insertion order, which the host does not promise (it returned arrival order and reused
// deleted slots): ShuffleUnordered(seed) opts into a random order, and SetLatency makes a
// missing timeout visible. A table needs a Journal entry before a commit mode above *NONE
// can open it, and a write inside a transaction on an unjournaled table is SQL7008: a
// test of a *CS, *CHG, *ALL or *RR service calls fake.Journal("SCHEMA.TABLE", true) for
// every table it writes before db2access.Open (see the Example).
package db2fake

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"time"
	"weak"

	"milton_prism/pkg/db2access"
	"milton_prism/pkg/db2access/ibmi"
)

type decCell string // a stored DECIMAL, kept as its exact text

// tempCell is a stored DATE, TIME or TIMESTAMP, kept as the IBM ISO text the host holds
// ("2026-11-23", "10.20.30", "2026-11-23-10.20.30.123456"), which is what the builders
// bind and what CHAR(col, ISO) and the TIMESTAMP CAST read back.
type tempCell string

type state struct {
	mu      sync.Mutex
	rows    map[string][]map[string]any
	sqls    []string
	iso     []sql.IsolationLevel
	execErr error
	// queryErr: SELECTs on the table ("SCHEMA.NAME") fail with the error; "" = every table
	queryErr map[string]error
	commits  int
	rollbk   int
	openTx   int
	// tables is the catalogue Declare fills ("SCHEMA.NAME"); now is the clock of CURRENT_* defaults
	tables map[string]declTable
	now    time.Time
	// journaled holds the journaling status of "SCHEMA.NAME" files; absent = no entry
	journaled map[string]bool
	// job is the host's job CCSID (Config.CCSID of an Open, or SetJobCCSID): the CCSID of
	// the columns that declare none. 0 = unknown.
	job int
	// latency delays every statement (or until its context ends); shuffle randomises the
	// order of rows no ORDER BY fixes
	latency time.Duration
	shuffle *int64
}

// sqlDriver exists only so a connector can name a driver; every connection comes from
// the Fake's own connector. There is no registry with database/sql and no global map: a
// Fake and its rows are garbage once nothing refers to them.
type sqlDriver struct{}

func (sqlDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("db2fake: open a database through Fake.Open or Fake.ConnectFunc")
}

// Fake is a db2access.Driver backed by memory.
type Fake struct {
	st     *state
	mu     sync.Mutex
	opened []db2access.Config
	// pools holds, for each opened pool, the commit mode and the job CCSID it was given:
	// both belong to the connection, so a later Open changes nothing for an earlier one
	// (A37, A47). Closing the pool removes its entry (the connector is an io.Closer, A49, A54):
	// no garbage-collection hook can do it, because the opener goroutine of an unclosed
	// sql.DB keeps the pool reachable. A pool that is never closed stays; the key is weak
	// only so the map itself holds no pool.
	pools map[weak.Pointer[sql.DB]]poolInfo
}

type poolInfo struct {
	mode db2access.CommitMode
	job  int
}

var _ db2access.Driver = (*Fake)(nil)

// New returns an empty fake. Every table is journaled unless Journal says otherwise.
func New() *Fake {
	st := &state{rows: map[string][]map[string]any{}, journaled: map[string]bool{}, queryErr: map[string]error{},
		tables: map[string]declTable{}, now: DefaultNow}
	return &Fake{st: st, pools: map[weak.Pointer[sql.DB]]poolInfo{}}
}

// Open implements db2access.Driver; it records cfg. The new connection's job CCSID is the one
// SetJobCCSID declared for the host or, when none was, cfg.CCSID. It belongs to the
// connection: a later Open or SetJobCCSID changes no store that is already open (A47).
func (f *Fake) Open(cfg db2access.Config) (*sql.DB, error) {
	job := f.jobFor(cfg.CCSID)
	f.mu.Lock()
	f.opened = append(f.opened, cfg)
	f.mu.Unlock()
	c := &connector{st: f.st, mode: cfg.CommitMode, job: job}
	db := sql.OpenDB(c)
	f.mu.Lock()
	key := weak.Make(db)
	f.pools[key] = poolInfo{mode: cfg.CommitMode, job: job}
	c.forget = func() {
		f.mu.Lock()
		delete(f.pools, key)
		f.mu.Unlock()
	}
	f.mu.Unlock()
	return db, nil
}

// jobFor is the job CCSID of a connection opened with the configured one: what the host's
// job really has (SetJobCCSID) wins over what the configuration declares, so a wrong
// DB2_CCSID shows as the mismatch db2access.Open refuses (A39).
func (f *Fake) jobFor(configured int) int {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	if f.st.job != 0 {
		return f.st.job
	}
	return configured
}

// SetJobCCSID tells the fake the host's job DEFAULT CCSID (273 on PUB400): the CCSID of every
// column that declares none, for the text guard, the length of a value and the sort order
// (PUB400 round 2, A29; round 3: a job whose own CCSID is 65535 has default 37, pass 37).
// It applies to the connections opened afterwards, each of which keeps the value it was
// opened with, and to Seed. 0 resets it to unknown: a later Open takes the CCSID of its
// Config, and Seed accepts only 7-bit ASCII in such columns.
func (f *Fake) SetJobCCSID(ccsid int) {
	f.st.mu.Lock()
	f.st.job = ccsid
	f.st.mu.Unlock()
}

// DefaultCCSID implements db2access.Driver: the default CCSID of the job behind db, as
// ACTIVE_JOB_INFO reports it on the host (A39).
func (f *Fake) DefaultCCSID(ctx context.Context, db *sql.DB) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if job := f.pools[weak.Make(db)].job; job != 0 {
		return job, nil
	}
	return 0, errors.New("db2fake: the connection has no job CCSID (declare one with SetJobCCSID or Config.CCSID)")
}

// Journaled implements db2access.Driver.
func (f *Fake) Journaled(ctx context.Context, db *sql.DB, t db2access.Table) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f.mu.Lock()
	mode := f.pools[weak.Make(db)].mode
	f.mu.Unlock()
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	return f.st.journalStatus(t.Qualified(), mode)
}

// journalStatus is whether the file is journaled. Under commitment control a file with no
// Journal entry is an error: counting it as journaled would let a *CS service pass Open
// where the real unjournaled file fails with SQL7008. The caller holds mu.
func (st *state) journalStatus(qualified string, mode db2access.CommitMode) (bool, error) {
	if j, ok := st.journaled[qualified]; ok {
		return j, nil
	}
	if mode != "" && mode.Transactional() {
		// db2access.Classify drops the text of an error, so say it where a test author sees it
		slog.Error("db2fake: no journal entry for a table under commitment control: call fake.Journal(name, true), or false to see SQL7008",
			"commit_mode", string(mode), "table", qualified)
		return false, fmt.Errorf("db2fake: commit mode %s needs the journaling status of %s", mode, qualified)
	}
	return true, nil
}

// ConnectFunc returns an ibmi.ConnectFunc whose connector is this fake, so a service
// test can run the skeleton driver itself (ibmi.Driver{Connect: fake.ConnectFunc()}):
// its TLS decision, its journaling check and the unit of work, against memory. The
// fake answers the QSYS2 catalogue queries of ibmi.Driver.Journaled from Journal.
func (f *Fake) ConnectFunc() ibmi.ConnectFunc {
	return func(p ibmi.Params) (driver.Connector, error) {
		return &connector{st: f.st, mode: p.CommitMode, current: p.Library, job: f.jobFor(p.CCSID)}, nil
	}
}

// connector carries what belongs to one connection: its commit mode, CURRENT SCHEMA and job CCSID.
type connector struct {
	st      *state
	mode    db2access.CommitMode
	current string
	job     int
	forget  func() // set by Open; run by sql.DB.Close
}

// Close implements io.Closer, which sql.DB.Close calls on its connector.
func (c *connector) Close() error {
	if c.forget != nil {
		c.forget()
	}
	return nil
}

func (c *connector) Connect(context.Context) (driver.Conn, error) {
	return &conn{st: c.st, mode: c.mode, current: c.current, job: c.job}, nil
}
func (c *connector) Driver() driver.Driver { return sqlDriver{} }

// catalogue answers the three QSYS2 queries of the journaling check, or reports false.
func (c *conn) catalogue(q string, args []driver.NamedValue) (driver.Rows, bool, error) {
	str := func(i int) string { s, _ := args[i].Value.(string); return s }
	switch q {
	case "SELECT CURRENT SCHEMA FROM SYSIBM.SYSDUMMY1":
		return &rowset{cols: []string{"CURRENT SCHEMA"}, data: [][]driver.Value{{c.current}}}, true, nil
	}
	if strings.Contains(q, "QSYS2.ACTIVE_JOB_INFO(") && len(args) == 0 { // the job CCSID check of ibmi.Driver.DefaultCCSID
		if c.job == 0 {
			return &rowset{cols: []string{"DEFAULT_CCSID"}}, true, nil
		}
		return &rowset{cols: []string{"DEFAULT_CCSID"}, data: [][]driver.Value{{int64(c.job)}}}, true, nil
	}
	switch {
	case strings.HasPrefix(q, "SELECT SYSTEM_TABLE_SCHEMA, SYSTEM_TABLE_NAME FROM QSYS2.SYSTABLES ") && len(args) == 2:
		return &rowset{cols: []string{"SYSTEM_TABLE_SCHEMA", "SYSTEM_TABLE_NAME"}, data: [][]driver.Value{{str(0), str(1)}}}, true, nil
	case strings.HasPrefix(q, "SELECT JOURNALED FROM TABLE(QSYS2.OBJECT_STATISTICS(") && len(args) == 2:
		j, err := c.st.journalStatus(str(0)+"."+str(1), c.mode)
		if err != nil {
			return nil, true, err
		}
		status := "YES"
		if !j {
			status = "NO"
		}
		return &rowset{cols: []string{"JOURNALED"}, data: [][]driver.Value{{status}}}, true, nil
	}
	return nil, false, nil
}

// Journal sets whether the table (as db2access.Table.Qualified renders it) is journaled.
func (f *Fake) Journal(qualified string, journaled bool) {
	f.st.mu.Lock()
	f.st.journaled[qualified] = journaled
	f.st.mu.Unlock()
}

// SetLatency delays every statement by d, or until its context ends (then the statement
// returns the context's error and changes nothing). A test of a service's timeout fails
// without a latency: an instant fake never lets a missing bound show.
func (f *Fake) SetLatency(d time.Duration) {
	f.st.mu.Lock()
	f.st.latency = d
	f.st.mu.Unlock()
}

// ShuffleUnordered makes a SELECT with no ORDER BY return its rows in a seeded random
// order, and the rows an ORDER BY leaves tied too. Db2 promises no order there (PUB400
// returned arrival order and reused deleted slots, which is no promise), so a test that
// passes only in insertion order is wrong. Off by default: results are then stable.
func (f *Fake) ShuffleUnordered(seed int64) {
	f.st.mu.Lock()
	f.st.shuffle = &seed
	f.st.mu.Unlock()
}

// FailExec makes every INSERT, UPDATE and DELETE fail with err (nil to stop).
func (f *Fake) FailExec(err error) {
	f.st.mu.Lock()
	f.st.execErr = err
	f.st.mu.Unlock()
}

// FailQuery makes every SELECT on the table fail with err; qualified is the name as
// db2access.Table.Qualified renders it ("NWLIB.ORDDTL"), "" means every table, and a
// nil err stops it. Inject a DBError so the service sees the *db2access.Error a real
// host produces (db2fake.DBError{SQLSTATE: "57033", SQLCODE: -913} is a lock timeout).
func (f *Fake) FailQuery(qualified string, err error) {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	if err == nil {
		delete(f.st.queryErr, qualified)
		return
	}
	f.st.queryErr[qualified] = err
}

// DBError is a database error as an IBM i driver reports it: db2access.Classify reads
// its SQLSTATE and SQLCODE into a *db2access.Error with the fixed message of that code.
type DBError struct {
	SQLSTATE string
	SQLCODE  int
}

func (e DBError) Error() string {
	return fmt.Sprintf("db2fake: injected error (SQLSTATE %s, SQLCODE %d)", e.SQLSTATE, e.SQLCODE)
}

// SQLState and SQLCode are the accessors db2access.Classify looks for.
func (e DBError) SQLState() string { return e.SQLSTATE }
func (e DBError) SQLCode() int     { return e.SQLCODE }

// Seed stores the row of a built INSERT (db2access.Table.Insert) directly, outside any
// store or transaction. It is test setup: it is not listed by Statements and FailExec
// does not apply to it.
func (f *Fake) Seed(st db2access.Stmt) error {
	args := st.Args()
	nv := make([]driver.NamedValue, len(args))
	for i, a := range args {
		nv[i] = driver.NamedValue{Ordinal: i + 1, Value: a}
	}
	s, err := parse(st.SQL(), len(nv))
	if err != nil {
		return err
	}
	if s.verb != "INSERT" {
		return fmt.Errorf("db2fake: Seed takes a built INSERT, not a %s", s.verb)
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	if f.st.openTx > 0 { // a ROLLBACK would silently drop the seeded row
		return errors.New("db2fake: Seed while a transaction is open; seed before the code under test runs")
	}
	_, err = f.st.apply(s, nv, nil, "", f.st.job)
	return err
}

// Statements returns every SQL text the fake received, in order.
func (f *Fake) Statements() []string {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	return append([]string(nil), f.st.sqls...)
}

// Commits and Rollbacks count the finished transactions.
func (f *Fake) Commits() int   { f.st.mu.Lock(); defer f.st.mu.Unlock(); return f.st.commits }
func (f *Fake) Rollbacks() int { f.st.mu.Lock(); defer f.st.mu.Unlock(); return f.st.rollbk }

// Isolations are the isolation levels transactions were begun with.
func (f *Fake) Isolations() []sql.IsolationLevel {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	return append([]sql.IsolationLevel(nil), f.st.iso...)
}

// Opened are the configurations Open received.
func (f *Fake) Opened() []db2access.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]db2access.Config(nil), f.opened...)
}

// conn is one connection; cur is its open transaction, if any.
type conn struct {
	st      *state
	mode    db2access.CommitMode
	current string
	job     int // the connection's job CCSID
	cur     *tx
}

func (c *conn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("db2fake: use QueryContext/ExecContext")
}
func (c *conn) Close() error              { return nil }
func (c *conn) Begin() (driver.Tx, error) { return c.begin(), nil }
func (c *conn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	c.st.mu.Lock()
	c.st.iso = append(c.st.iso, sql.IsolationLevel(o.Isolation))
	c.st.mu.Unlock()
	return c.begin(), nil
}

// wait honours the context and the injected latency; no statement is logged or applied
// when it fails.
func (c *conn) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.st.mu.Lock()
	d := c.st.latency
	c.st.mu.Unlock()
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *conn) begin() *tx {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	c.st.openTx++
	c.cur = &tx{st: c.st, c: c}
	return c.cur
}

// tx undoes its own writes on ROLLBACK, in reverse. It never restores a snapshot of a
// table, so a write another connection made meanwhile survives the rollback.
type tx struct {
	st   *state
	c    *conn
	undo []func() error // an undo that cannot restore a row says why
}

func (t *tx) end() {
	t.st.openTx--
	if t.c.cur == t {
		t.c.cur = nil
	}
}

func (t *tx) Commit() error {
	t.st.mu.Lock()
	defer t.st.mu.Unlock()
	t.st.commits++
	t.undo = nil
	t.end()
	return nil
}

func (t *tx) Rollback() error {
	t.st.mu.Lock()
	defer t.st.mu.Unlock()
	t.st.rollbk++
	var errs []error
	for i := len(t.undo) - 1; i >= 0; i-- {
		if err := t.undo[i](); err != nil {
			errs = append(errs, err)
		}
	}
	t.undo = nil
	t.end()
	return errors.Join(errs...)
}

func (c *conn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	c.st.sqls = append(c.st.sqls, q)
	if c.st.execErr != nil {
		return nil, c.st.execErr
	}
	s, err := parse(q, len(args))
	if err != nil {
		return nil, err
	}
	if s.verb == "SELECT" {
		return nil, fmt.Errorf("db2fake: a SELECT sent through Exec: %q", q)
	}
	n, err := c.st.apply(s, args, c.cur, c.mode, c.job)
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(n), nil
}

// matching returns the indexes of the table's rows that satisfy the WHERE.
func (st *state) matching(d declTable, s stmt, vals []any) ([]int, error) {
	var hit []int
	for i, r := range st.rows[s.table] {
		ok, err := match(d, r, s.where, vals)
		if err != nil {
			return nil, err
		}
		if ok {
			hit = append(hit, i)
		}
	}
	return hit, nil
}

// errUnjournaled is what a write inside a transaction gets on a file without a journal.
var errUnjournaled = DBError{SQLSTATE: "55019", SQLCODE: -7008}

// apply runs an INSERT, UPDATE or DELETE on the rows; the caller holds mu. It is atomic,
// like a Db2 statement: every row is evaluated before any row is changed. u is the open
// transaction of the connection (nil for autocommit and Seed); it receives the undo.
func (st *state) apply(s stmt, args []driver.NamedValue, u *tx, mode db2access.CommitMode, job int) (int64, error) {
	d, err := st.declared(s, job)
	if err != nil {
		return 0, err
	}
	vals, err := boundFor(d, s, args)
	if err != nil {
		return 0, err
	}
	if u != nil && mode != "" && mode.Transactional() { // autocommit runs at *NONE on the host: no journal needed
		if j, err := st.journalStatus(s.table, mode); err != nil {
			return 0, err
		} else if !j {
			return 0, errUnjournaled
		}
	}
	set := map[string]any{}
	for _, a := range s.assign {
		if set[a.col], err = store(d.byName[a.col], job, true, vals[a.arg]); err != nil {
			return 0, err
		}
	}
	rows := st.rows[s.table]
	if s.verb == "INSERT" {
		for _, c := range d.cols { // in declaration order: every omitted column takes its default
			if _, ok := set[c.Name()]; !ok {
				if set[c.Name()], err = defaultOf(c, job, st.now); err != nil {
					return 0, err
				}
			}
		}
		if err := checkUniqueNew(d, rows, set); err != nil {
			return 0, err
		}
		st.rows[s.table] = append(rows, set)
		if u != nil {
			u.undo = append(u.undo, func() error { st.drop(s.table, set); return nil })
		}
		return 1, nil
	}
	hit, err := st.matching(d, s, vals)
	if err != nil {
		return 0, err
	}
	if s.verb == "UPDATE" {
		next := append([]map[string]any(nil), rows...) // the table as it will be, checked before any change
		for _, i := range hit {
			row := make(map[string]any, len(rows[i]))
			for k, v := range rows[i] {
				row[k] = v
			}
			for k, v := range set {
				row[k] = v
			}
			next[i] = row
		}
		if err := checkUnique(d, next); err != nil {
			return 0, err
		}
		for _, i := range hit {
			old := make(map[string]any, len(rows[i]))
			for k, v := range rows[i] {
				old[k] = v
			}
			row := rows[i]
			for k, v := range set {
				if b, ok := v.([]byte); ok { // no row shares a byte slice with another
					v = append([]byte{}, b...)
				}
				row[k] = v
			}
			if u != nil {
				u.undo = append(u.undo, func() error {
					for k, v := range old {
						row[k] = v
					}
					return nil
				})
			}
		}
		return int64(len(hit)), nil
	}
	// DELETE
	gone := map[int]bool{}
	var removed []removedRow
	for _, i := range hit {
		gone[i] = true
		removed = append(removed, removedRow{at: i, row: rows[i]})
	}
	kept := make([]map[string]any, 0, len(rows)-len(hit))
	for i, r := range rows {
		if !gone[i] {
			kept = append(kept, r)
		}
	}
	st.rows[s.table] = kept
	if u != nil && len(removed) > 0 {
		u.undo = append(u.undo, func() error { return st.restore(d, s.table, removed) })
	}
	return int64(len(hit)), nil
}

// removedRow is a row a DELETE took out, with the position it had.
type removedRow struct {
	at  int
	row map[string]any
}

// restore puts the rows a rolled-back DELETE removed back at their original positions
// (the host puts them back in their slots). The fake has no isolation, so another
// connection may have taken a deleted row's unique key meanwhile; on the host that
// writer would have waited for the lock. Such a row is NOT restored, the rest are, and
// the ROLLBACK reports it: two rows with one unique key are never created. The position is
// the one the row had when it was deleted: rows that other connections inserted or deleted
// since shift it, so across connections the restored position is approximate (the host
// reuses slots in its own order anyway, and no test may rely on either).
func (st *state) restore(d declTable, table string, removed []removedRow) error {
	var conflict error
	for _, r := range removed { // ascending original positions
		rows := st.rows[table]
		if err := checkUniqueNew(d, rows, r.row); err != nil {
			conflict = fmt.Errorf("db2fake: ROLLBACK cannot restore a deleted row of %s: another connection took its unique key (the host would have held the lock): %w", table, err)
			continue
		}
		at := min(r.at, len(rows))
		next := make([]map[string]any, 0, len(rows)+1)
		next = append(append(append(next, rows[:at]...), r.row), rows[at:]...)
		st.rows[table] = next
	}
	return conflict
}

// drop removes the row (by identity) from the table.
func (st *state) drop(table string, row map[string]any) {
	rows := st.rows[table]
	for i, r := range rows {
		if reflect.ValueOf(r).Pointer() == reflect.ValueOf(row).Pointer() {
			st.rows[table] = append(rows[:i:i], rows[i+1:]...)
			return
		}
	}
}

func (c *conn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	c.st.sqls = append(c.st.sqls, q)
	if rows, ok, err := c.catalogue(q, args); ok {
		return rows, err
	}
	s, err := parse(q, len(args))
	if err != nil {
		return nil, err
	}
	if s.verb != "SELECT" {
		return nil, fmt.Errorf("db2fake: a %s sent through Query: %q", s.verb, q)
	}
	if err := c.st.queryErr[s.table]; err != nil {
		return nil, err
	}
	if err := c.st.queryErr[""]; err != nil {
		return nil, err
	}
	d, err := c.st.declared(s, c.job)
	if err != nil {
		return nil, err
	}
	vals, err := boundFor(d, s, args)
	if err != nil {
		return nil, err
	}
	idx, err := c.st.matching(d, s, vals)
	if err != nil {
		return nil, err
	}
	hit := make([]map[string]any, len(idx))
	for i, j := range idx {
		hit[i] = c.st.rows[s.table][j]
	}
	if c.st.shuffle != nil {
		rand.New(rand.NewSource(*c.st.shuffle)).Shuffle(len(hit), func(i, j int) { hit[i], hit[j] = hit[j], hit[i] })
	}
	if err := sortRows(d, hit, s.order); err != nil {
		return nil, err
	}
	if s.hasLimit && len(hit) > s.limit {
		hit = hit[:s.limit]
	}
	cols := make([]string, len(s.items))
	for i, it := range s.items {
		cols[i] = it.col
	}
	data := make([][]driver.Value, 0, len(hit))
	for _, r := range hit {
		out := make([]driver.Value, len(s.items))
		for i, it := range s.items {
			if out[i], err = project(it, r); err != nil {
				return nil, err
			}
		}
		data = append(data, out)
	}
	return &rowset{cols: cols, data: data}, nil
}

type rowset struct {
	cols []string
	data [][]driver.Value
	i    int
}

func (r *rowset) Columns() []string { return r.cols }
func (r *rowset) Close() error      { return nil }
func (r *rowset) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}
