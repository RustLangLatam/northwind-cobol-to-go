// Package ibmi is the IBM i implementation of db2access.Driver (ADR-0056, phase 3). It
// is skeleton code: a generated service wires it, never writes it, and numgate fails
// the gate when any file under pkg/db2access differs from the skeleton's.
//
// What it guarantees, each pinned by a test:
//
//   - Every connection is built from cfg.TLSConfig(). Params.TLS is nil only when the
//     operator set AllowPlaintext; without it there is no code path that dials the
//     plaintext host server, and Open refuses port 8471 (Config.Validate).
//   - The password is revealed in exactly one place (Open) and travels as a redacted
//     Params field; no error carries it.
//   - The journaling check is the QSYS2 catalogue query below, run on the connection
//     pool; a database that says "not journaled" fails Open before the first write.
//   - The i400 driver is reached only through a ConnectFunc. The package that links the
//     hardened fork (pkg/db2access/ibmi/i400conn) registers one; this package does not
//     import it, so it builds and tests with no network and no fork.
//
// Naming and library-list resolution. The fork connects with SQL naming (naming=sql,
// pinned by i400conn), the default of the i400 driver. Under SQL naming an unqualified
// table name resolves in the connection's CURRENT SCHEMA only (Config.Library); the
// library list is not used for tables, so the journaling check looks in CURRENT SCHEMA
// and nowhere else. A table not found there is an error, not "journaled", and views,
// aliases and logical files are excluded (TABLE_TYPE). These queries are written from the
// QSYS2 documentation and are NOT yet verified on a real host (task 2.6 e of the openspec
// change); a wrong guess fails closed (an error at Open), never as "journaled".
package ibmi

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"

	"milton_prism/pkg/db2access"
)

// Params is what a ConnectFunc needs to build a connector. It carries the password, so
// every formatting of it is redacted.
type Params struct {
	Host     string
	Port     int
	Library  string
	User     string
	Password db2access.Secret
	// TLS is cfg.TLSConfig(): the verified configuration (CA pool, server name, TLS
	// 1.2 minimum, never InsecureSkipVerify). Nil only for an AllowPlaintext connection.
	TLS        *tls.Config
	CCSID      int
	CommitMode db2access.CommitMode
	// MinPasswordLevel is cfg.EffectiveMinPasswordLevel(): the connector must refuse a
	// server that announces a lower QPWDLVL before sending password material.
	MinPasswordLevel int
}

// String redacts the credential.
func (p Params) String() string {
	return fmt.Sprintf("ibmi.Params{host=%s port=%d library=%s user=%s password=[redacted] tls=%t ccsid=%d commit=%s}",
		p.Host, p.Port, p.Library, p.User, p.TLS != nil, p.CCSID, p.CommitMode)
}

// GoString keeps %#v from printing the password.
func (p Params) GoString() string { return p.String() }

// ConnectFunc builds the database/sql connector of the IBM i driver for p. A connector
// for p.TLS == nil is a plaintext one and exists only for AllowPlaintext.
type ConnectFunc func(p Params) (driver.Connector, error)

var (
	regMu          sync.RWMutex
	defaultConnect ConnectFunc
)

// RegisterConnect sets the process-wide ConnectFunc used by a Driver with no Connect of
// its own. The package linking the i400 fork calls it from init.
func RegisterConnect(f ConnectFunc) {
	regMu.Lock()
	defaultConnect = f
	regMu.Unlock()
}

// ErrNoConnector is returned by Open when no ConnectFunc is available.
var ErrNoConnector = errors.New("ibmi: no IBM i connector is linked: import _ \"milton_prism/pkg/db2access/ibmi/i400conn\" in the composition root")

// Driver implements db2access.Driver for IBM i.
type Driver struct {
	// Connect overrides the registered ConnectFunc (tests inject a fake).
	Connect ConnectFunc
}

var _ db2access.Driver = Driver{}

func (d Driver) connect() ConnectFunc {
	if d.Connect != nil {
		return d.Connect
	}
	regMu.RLock()
	defer regMu.RUnlock()
	return defaultConnect
}

// ParamsFor validates cfg and returns the connection parameters. It is the only place
// the password is revealed and the only place Params.TLS is decided: cfg.TLSConfig() is
// nil exactly when cfg.AllowPlaintext is set.
func ParamsFor(cfg db2access.Config) (Params, error) {
	if err := cfg.Validate(); err != nil {
		return Params{}, err
	}
	if placeholderPassword(cfg.Password.Reveal()) {
		return Params{}, errors.New("ibmi: the password is empty or still a placeholder (DB2_PASSWORD): set the real one before starting")
	}
	tc, err := cfg.TLSConfig()
	if err != nil {
		return Params{}, err
	}
	if tc == nil && !cfg.AllowPlaintext { // cannot happen; a plaintext dial is never the fallback
		return Params{}, errors.New("ibmi: refusing a connection without TLS: AllowPlaintext is not set")
	}
	return Params{
		Host: cfg.Host, Port: cfg.EffectivePort(), Library: strings.ToUpper(strings.TrimSpace(cfg.Library)), User: cfg.User,
		Password: cfg.Password, TLS: tc, CCSID: cfg.CCSID, CommitMode: cfg.CommitMode, MinPasswordLevel: cfg.EffectiveMinPasswordLevel(),
	}, nil
}

// Open implements db2access.Driver.
func (d Driver) Open(cfg db2access.Config) (*sql.DB, error) {
	p, err := ParamsFor(cfg)
	if err != nil {
		return nil, err
	}
	connect := d.connect()
	if connect == nil {
		return nil, ErrNoConnector
	}
	conn, err := connect(p)
	if err != nil {
		// The connector's own text can echo a DSN; keep it out of the error.
		return nil, errors.New("ibmi: the connector could not be built (the driver's message is withheld: it can carry connection details)")
	}
	if conn == nil {
		return nil, errors.New("ibmi: the connector is nil")
	}
	return sql.OpenDB(conn), nil
}

// The QSYS2 catalogue queries of the journaling check (see the package doc).
const (
	qCurrentSchema = "SELECT CURRENT SCHEMA FROM SYSIBM.SYSDUMMY1"
	// TABLE_TYPE: T = table, P = physical file; views (V), aliases (A) and logical files (L)
	// are not journaled objects of their own and are excluded.
	qSystemName = "SELECT SYSTEM_TABLE_SCHEMA, SYSTEM_TABLE_NAME FROM QSYS2.SYSTABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND TABLE_TYPE IN ('T', 'P')"
	qJournaled  = "SELECT JOURNALED FROM TABLE(QSYS2.OBJECT_STATISTICS(CAST(? AS VARCHAR(10)), '*FILE', CAST(? AS VARCHAR(10)))) AS S"
)

// qDefaultCCSID reads the DEFAULT CCSID of the current job (A39). PUB400 round 3 measured it
// through the TLS server job: CCSID 273 and DEFAULT_CCSID 273, and after CHGJOB CCSID(65535)
// CCSID 65535 but DEFAULT_CCSID 37, which is what new columns got. QSYS2.GET_JOB_INFO has no
// CCSID column on that release, so it is not used. DETAILED_INFO 'ALL' is costly and that '*'
// is unique is unconfirmed, so the query stops at two rows: two still fail the exactly-one
// check below (A55). The FETCH FIRST clause itself was not run on PUB400.
const qDefaultCCSID = "SELECT DEFAULT_CCSID FROM TABLE(QSYS2.ACTIVE_JOB_INFO(JOB_NAME_FILTER => '*', DETAILED_INFO => 'ALL')) X FETCH FIRST 2 ROWS ONLY"

// ErrNoJobCCSID is returned by DefaultCCSID when the host does not report one value.
var ErrNoJobCCSID = errors.New("ibmi: the host did not report exactly one default CCSID for the job")

// DefaultCCSID implements db2access.Driver: the default CCSID of the job behind db, which
// is the CCSID a column without a CCSID clause gets. Fails closed: no row, several rows, a
// NULL or a non-positive value is an error, never a guess.
//
// It reads ONE pooled job: whichever connection database/sql hands out. That is enough
// because the DEFAULT_CCSID of a job comes from the user profile (CCSID, or the system value
// QCCSID when the profile says *SYSVAL), and every pooled connection signs on with the same
// profile, so every job gets the same value. A job changed afterwards (CHGJOB) is outside
// this check (A60). Without authority to ACTIVE_JOB_INFO the check fails closed (A55).
func (Driver) DefaultCCSID(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, qDefaultCCSID)
	if err != nil {
		return 0, classifyJobCCSID(err)
	}
	defer rows.Close()
	var got []sql.NullInt64
	for rows.Next() {
		var v sql.NullInt64
		if err := rows.Scan(&v); err != nil {
			return 0, classifyJobCCSID(err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		return 0, classifyJobCCSID(err)
	}
	if len(got) != 1 || !got[0].Valid || got[0].Int64 <= 0 || got[0].Int64 > db2access.BinaryCCSID {
		return 0, ErrNoJobCCSID
	}
	return int(got[0].Int64), nil
}

// classifyJobCCSID is Classify, except that a refusal on authority names what is missing:
// ACTIVE_JOB_INFO needs *USE authority on the job (A55). The text carries no value.
func classifyJobCCSID(err error) error {
	c := db2access.Classify("job CCSID check", err)
	var e *db2access.Error
	if errors.As(c, &e) && (e.SQLState == "42501" || e.SQLCode == -551 || e.SQLCode == -552) {
		out := *e
		out.Message = "not authorized: reading the job CCSID needs *USE authority on the job (QSYS2.ACTIVE_JOB_INFO), or set DB2_CCSID=0"
		return &out
	}
	return c
}

// ErrTableNotFound is returned by Journaled for a table the catalogue does not hold.
var ErrTableNotFound = errors.New("ibmi: the table is not in the catalogue of the schema or library list searched")

// Journaled implements db2access.Driver: whether the physical file behind t is
// journaled, so a write under commitment control will not fail with SQL7008.
func (Driver) Journaled(ctx context.Context, db *sql.DB, t db2access.Table) (bool, error) {
	schemas, err := candidateSchemas(ctx, db, t)
	if err != nil {
		return false, err
	}
	for _, schema := range schemas {
		var sysSchema, sysName string
		err := db.QueryRowContext(ctx, qSystemName, schema, t.Name()).Scan(&sysSchema, &sysName)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, db2access.Classify("journaling check", err)
		}
		var journaled sql.NullString
		err = db.QueryRowContext(ctx, qJournaled, strings.TrimSpace(sysSchema), strings.TrimSpace(sysName)).Scan(&journaled)
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("%w: %s (no object statistics)", ErrTableNotFound, t.Qualified())
		}
		if err != nil {
			return false, db2access.Classify("journaling check", err)
		}
		return strings.EqualFold(strings.TrimSpace(journaled.String), "YES"), nil
	}
	return false, fmt.Errorf("%w: %s", ErrTableNotFound, t.Qualified())
}

// candidateSchemas is the schema when the table is qualified, otherwise CURRENT SCHEMA:
// the driver connects with SQL naming (naming=sql in i400conn, the fork's default), under
// which an unqualified name resolves in the current schema only and the library list is
// not consulted. That the host agrees is a task 2.6 (e) check on a real IBM i.
func candidateSchemas(ctx context.Context, db *sql.DB, t db2access.Table) ([]string, error) {
	if t.Schema() != "" {
		return []string{t.Schema()}, nil
	}
	var current sql.NullString
	if err := db.QueryRowContext(ctx, qCurrentSchema).Scan(&current); err != nil {
		return nil, db2access.Classify("journaling check", err)
	}
	schema := strings.ToUpper(strings.TrimSpace(current.String))
	if schema == "" {
		return nil, fmt.Errorf("%w: %s (the connection has no current schema)", ErrTableNotFound, t.Qualified())
	}
	return []string{schema}, nil
}

// placeholderPassword reports whether pw is empty or one of the placeholders the generated
// deliverable ships (<set-me>, <your-...>, change-me, SET_ME), so a service that was never
// given a real password refuses to start instead of trying to sign on with a marker.
func placeholderPassword(pw string) bool {
	p := strings.ToLower(strings.TrimSpace(pw))
	return p == "" || p == "change-me" || p == "set_me" || (strings.HasPrefix(p, "<") && strings.HasSuffix(p, ">"))
}
