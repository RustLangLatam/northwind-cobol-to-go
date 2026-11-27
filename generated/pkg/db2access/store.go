package db2access

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"time"
)

// runner is what *sql.DB and *sql.Tx share.
type runner interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// ErrTooManyRows is returned when a Select with no Limit returns more than the cap.
var ErrTooManyRows = errors.New("db2access: the result has more rows than the row cap; set Select.Limit, narrow the WHERE, or raise Config.MaxRows")

// ErrWriteOutsideTx is returned by Store.Exec under a commit mode above *NONE: a write
// outside InTx would run at the connection's default isolation and outside the unit of
// work the mode promises, so it is refused instead of silently allowed.
var ErrWriteOutsideTx = errors.New("db2access: under a commit mode above *NONE every write runs inside Store.InTx")

// limits are what every statement of a Store or Tx is bounded by.
type limits struct {
	maxRows        int
	maxLimit       int
	maxResultBytes int           // per result; a field, not a package variable, so tests do not race
	budget         *resultBudget // shared by every Store of the process
	timeout        time.Duration
	jobCCSID       int // Config.CCSID: the CCSID of columns that declare none
}

// ErrLimitTooLarge is returned for a Select.Limit above Config.MaxLimit.
var ErrLimitTooLarge = errors.New("db2access: Select.Limit is above Config.MaxLimit (DB2_MAX_LIMIT): a read that large puts unbounded load on the host; page the read or raise the setting")

// bound gives a statement the store's timeout; an earlier deadline of the caller wins
// (context.WithTimeout keeps the parent's deadline when it is sooner).
func (l limits) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, l.timeout)
}

func query(ctx context.Context, r runner, sel Select, l limits) ([]Row, error) {
	if sel.Limit > l.maxLimit {
		return nil, ErrLimitTooLarge
	}
	capped := sel.Limit == 0
	if capped { // ask for one row more than the cap, to tell "exactly the cap" from "more"
		sel.Limit, sel.capProbe = l.maxRows+1, true
	}
	st, err := sel.Build()
	if err != nil {
		return nil, err
	}
	if err := st.checkJobText(l.jobCCSID); err != nil {
		return nil, err
	}
	ctx, cancel := l.bound(ctx)
	defer cancel()
	rows, err := r.QueryContext(ctx, st.sql, st.args...)
	if err != nil {
		return nil, Classify("select "+st.table.Qualified(), err)
	}
	defer rows.Close()
	out, err := scanRows(rows, st.cols, l)
	if err != nil {
		return nil, err
	}
	if capped && len(out) > l.maxRows {
		return nil, ErrTooManyRows
	}
	return out, nil
}

func exec(ctx context.Context, r runner, st Stmt, l limits) (int64, error) {
	if st.sql == "" {
		return 0, errors.New("db2access: empty statement; build one with Insert, Update or Delete")
	}
	if err := st.checkJobText(l.jobCCSID); err != nil {
		return 0, err
	}
	ctx, cancel := l.bound(ctx)
	defer cancel()
	res, err := r.ExecContext(ctx, st.sql, st.args...)
	if err != nil {
		return 0, Classify("write "+st.table.Qualified(), err)
	}
	n, err := res.RowsAffected()
	return n, Classify("rows affected", err)
}

// Store is an open, checked connection pool.
type Store struct {
	db   *sql.DB
	mode CommitMode
	lim  limits
	pool poolLimits
}

// Open validates cfg, opens the pool with its limits (Config.MaxOpenConns,
// MaxIdleConns, ConnMaxLifetime) and, for a commit mode above *NONE, checks that every
// table in written is journaled before returning. An unjournaled table fails here,
// naming SQL7008, instead of at the first write. The connect check and each journaling
// check are bounded by Config.QueryTimeout. An AllowPlaintext connection logs a warning.
func Open(ctx context.Context, drv Driver, cfg Config, written ...Table) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if _, err := cfg.TLSConfig(); err != nil {
		return nil, err
	}
	if !cfg.UseTLS() {
		slog.Warn("db2access: connecting in plaintext, without TLS (AllowPlaintext is set): the credentials and every row cross the network in clear",
			"host", cfg.Host, "port", cfg.EffectivePort())
	}
	db, err := drv.Open(cfg)
	if err != nil {
		return nil, Classify("open", err)
	}
	if db == nil {
		return nil, errors.New("db2access: the driver returned no connection pool")
	}
	pool := cfg.limits()
	db.SetMaxOpenConns(pool.maxOpen)
	db.SetMaxIdleConns(pool.maxIdle)
	db.SetConnMaxLifetime(pool.maxLifetime)
	db.SetConnMaxIdleTime(pool.maxIdleTime)
	maxRows := cfg.MaxRows
	if maxRows == 0 {
		maxRows = DefaultMaxRows
	}
	if maxRows > HardMaxRows { // Validate refuses it; this keeps maxRows+1 safe even so
		maxRows = HardMaxRows
	}
	lim := limits{maxRows: maxRows, maxLimit: pool.maxLimit, maxResultBytes: HardMaxResultBytes,
		budget: processResultBudget, timeout: pool.queryTimeout, jobCCSID: cfg.CCSID}
	pctx, cancel := lim.bound(ctx)
	err = db.PingContext(pctx)
	cancel()
	if err != nil {
		_ = db.Close()
		return nil, Classify("connect", err)
	}
	if err := checkHostCCSID(ctx, drv, db, cfg.CCSID, lim); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := checkDefaults(cfg.CCSID, written); err != nil {
		_ = db.Close()
		return nil, err
	}
	if cfg.CommitMode.Transactional() {
		var bad []string
		for _, t := range written {
			jctx, cancel := lim.bound(ctx)
			ok, err := drv.Journaled(jctx, db, t)
			cancel()
			if err != nil {
				_ = db.Close()
				return nil, Classify("journaling check", err)
			}
			if !ok {
				bad = append(bad, t.Qualified())
			}
		}
		if len(bad) > 0 {
			_ = db.Close()
			return nil, &Error{Op: "startup", SQLState: "55019", SQLCode: -7008, Tables: bad,
				Message: "commit mode " + string(cfg.CommitMode) + " needs journaled tables; " + msgNotJournaled}
		}
	}
	return &Store{db: db, mode: cfg.CommitMode, lim: lim, pool: pool}, nil
}

// checkHostCCSID compares the declared job CCSID with the host's default CCSID (A39). A
// declared 0 is the ASCII-only opt-out and is safe on any host, so nothing is asked.
func checkHostCCSID(ctx context.Context, drv Driver, db *sql.DB, declared int, lim limits) error {
	if declared == 0 {
		return nil
	}
	cctx, cancel := lim.bound(ctx)
	defer cancel()
	host, err := drv.DefaultCCSID(cctx, db)
	if err != nil {
		return Classify("job CCSID check", err)
	}
	if host != declared {
		return &Error{Op: "startup", SQLState: SQLStateCCSIDMismatch,
			Message: "DB2_CCSID is not the default CCSID of the host job (" + strconv.Itoa(host) + "), which is the CCSID a column without a CCSID clause gets: the text guard would be wrong; set DB2_CCSID to it (platform guard, not a host SQLSTATE)"}
	}
	return nil
}

// checkDefaults applies the text guard to the default literal of each column with no CCSID
// of the tables the service writes, against the job CCSID: the builders cannot (A43), and
// the fake does, so the real path must agree with it.
func checkDefaults(jobCCSID int, tables []Table) error {
	for _, t := range tables {
		for _, c := range t.Columns() {
			if rule, text := c.Default(); c.kind == KindText && rule == DefaultLiteral {
				if err := c.CheckTextFor(jobCCSID, text); err != nil {
					var e *Error
					if errors.As(err, &e) {
						e.Op = "startup"
						e.Message = "the default of " + t.Qualified() + ": " + e.Message
					}
					return err
				}
			}
		}
	}
	return nil
}

// Stats reports the connection pool (open, in use, idle, waits), for metrics.
func (s *Store) Stats() sql.DBStats { return s.db.Stats() }

// Query runs a SELECT, outside any transaction (one statement at the connection's
// default isolation). A SELECT with no Limit is capped at Config.MaxRows rows; one
// more row is ErrTooManyRows. Reads that need the commit mode's isolation go in InTx.
// The statement is bounded by Config.QueryTimeout unless ctx ends sooner.
func (s *Store) Query(ctx context.Context, sel Select) ([]Row, error) {
	return query(ctx, s.db, sel, s.lim)
}

// Exec runs a built INSERT, UPDATE or DELETE and returns the rows affected. Under a
// commit mode above *NONE it is refused (ErrWriteOutsideTx): writes go through InTx.
func (s *Store) Exec(ctx context.Context, st Stmt) (int64, error) {
	if s.mode.Transactional() {
		return 0, ErrWriteOutsideTx
	}
	return exec(ctx, s.db, st, s.lim)
}

// Close releases the pool.
func (s *Store) Close() error { return s.db.Close() }

// Tx is a unit of work under the configured commit mode.
type Tx struct {
	tx  *sql.Tx
	lim limits
}

// Query runs a SELECT inside the unit of work, bounded by Config.QueryTimeout.
func (t *Tx) Query(ctx context.Context, sel Select) ([]Row, error) {
	return query(ctx, t.tx, sel, t.lim)
}

// Exec runs a built statement inside the unit of work, bounded by Config.QueryTimeout.
func (t *Tx) Exec(ctx context.Context, st Stmt) (int64, error) { return exec(ctx, t.tx, st, t.lim) }

// InTx runs fn in a transaction at the isolation the commit mode maps to: commit on
// nil, rollback on an error, a panic or runtime.Goexit. A rollback that fails is
// joined with fn's error. A COMMIT cut short by the context is reported as an Error
// with Unknown set (the outcome is not known). Under *NONE there is no commitment
// control and InTx refuses, because a rollback would be a lie. Inside fn use the Tx,
// never the Store: the unit of work holds a pool connection, so a Store call from fn
// waits for another one and, with the pool exhausted, until its timeout.
func (s *Store) InTx(ctx context.Context, fn func(*Tx) error) error {
	if !s.mode.Transactional() {
		return errors.New("db2access: commit mode *NONE has no commitment control; use Exec for single statements")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: s.mode.Isolation()})
	if err != nil {
		return Classify("begin", err)
	}
	settled := false
	defer func() {
		if !settled { // a panic or runtime.Goexit in fn
			_ = tx.Rollback()
		}
	}()
	if ferr := fn(&Tx{tx: tx, lim: s.lim}); ferr != nil {
		settled = true
		if rerr := tx.Rollback(); rerr != nil {
			return errors.Join(ferr, Classify("rollback", rerr))
		}
		return ferr
	}
	settled = true
	return classifyCommit(tx.Commit())
}
