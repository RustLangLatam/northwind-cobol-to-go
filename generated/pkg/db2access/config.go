// Package db2access is the decimal-safe data-access layer a generated service uses
// for a customer's EXISTING Db2 schema (ADR-0056, phase 2).
//
// Rules, each pinned by a test:
//
//   - A DECIMAL or NUMERIC column is never read through the driver's own number
//     mapping. Every SELECT casts it to VARCHAR(p+3) and the text is parsed with
//     cobolnum-go into a Dec of the column's exact precision and scale. Parameters
//     are bound as exact decimal text under CAST(? AS DECIMAL(p,s)). No binary or
//     decimal floating point type appears anywhere in the package. The CAST is not
//     aliased, so ORDER BY sorts the number, not the text.
//   - FOR BIT DATA and CCSID 65535 columns come back as []byte, untranslated. Any
//     other text column is a string: the driver (the IBM i adapter, phase 3) is
//     responsible for translating the column's CCSID to UTF-8, and Row.EBCDIC decodes
//     bytes the caller knows are EBCDIC text through cobolnum-go/ebcdic.
//   - There is no DDL entry point. Table and Column have no exported fields, so the
//     constructors are the only way in; statements are built only by Select, Insert,
//     Update and Delete, which validate every name again; Stmt has no exported way to
//     carry caller-written SQL, and Update and Delete require a WHERE comparison.
//     Every identifier is delimited by the builder (see sqlIdent), never by the caller.
//   - The commit mode is required configuration. For any mode above *NONE the tables
//     the service writes must be journaled before Open returns, and writes go through
//     InTx at the mode's isolation: Store.Exec is refused under a commit mode, so a
//     write cannot run outside commitment control by accident.
//   - The connection is TLS with a verified certificate unless AllowPlaintext is set,
//     and the password is an opaque Secret.
//   - Text a column cannot hold is refused before sending, because the host stores X'3F'
//     without an error: against the column's CCSID, or Config.CCSID (DB2_CCSID, the job
//     CCSID) when it declares none; 7-bit ASCII only when that is unknown.
//   - Errors are typed (SQLSTATE, SQLCODE) with fixed messages; the driver's own text,
//     which can echo a connection string, is never copied into them.
//
// The package is a skeleton package: generated services import it. numgate lets a
// service import it only when the migration's store is Db2, and fails the gate if the
// package differs from the skeleton's.
package db2access

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/RustLangLatam/cobolnum-go/ebcdic"
)

// CommitMode is the IBM i commitment-control level (the COMMIT parameter).
type CommitMode string

const (
	CommitNone CommitMode = "*NONE" // no commitment control: every statement is final
	CommitChg  CommitMode = "*CHG"  // uncommitted reads allowed
	CommitCS   CommitMode = "*CS"   // cursor stability
	CommitAll  CommitMode = "*ALL"  // read stability
	CommitRR   CommitMode = "*RR"   // repeatable read
)

// ErrCommitModeRequired is returned when the configuration names no commit mode. A
// default would silently decide whether a service's writes can be rolled back.
var ErrCommitModeRequired = errors.New("db2access: commit mode is required (*NONE, *CHG, *CS, *ALL or *RR)")

// ParseCommitMode reads a configuration value.
func ParseCommitMode(s string) (CommitMode, error) {
	switch m := CommitMode(s); m {
	case CommitNone, CommitChg, CommitCS, CommitAll, CommitRR:
		return m, nil
	case "":
		return "", ErrCommitModeRequired
	}
	// The value is not echoed: it comes from the environment, and a swapped ConfigMap and
	// Secret mapping would print the password.
	return "", errors.New("db2access: unknown commit mode (want *NONE, *CHG, *CS, *ALL or *RR)")
}

// Transactional reports whether the mode runs under commitment control.
func (m CommitMode) Transactional() bool { return m != CommitNone }

// Isolation maps the mode to the database/sql isolation level. *NONE has no level.
func (m CommitMode) Isolation() sql.IsolationLevel {
	switch m {
	case CommitChg:
		return sql.LevelReadUncommitted
	case CommitCS:
		return sql.LevelReadCommitted
	case CommitAll:
		return sql.LevelRepeatableRead
	case CommitRR:
		return sql.LevelSerializable
	}
	return sql.LevelDefault
}

// Secret is a credential that cannot be printed by accident: every formatting and
// encoding path returns "[redacted]". Only Reveal gives the value, and only the driver
// adapter should call it.
type Secret string

const redacted = "[redacted]"

func (Secret) String() string               { return redacted }
func (Secret) GoString() string             { return redacted }
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (Secret) LogValue() slog.Value         { return slog.StringValue(redacted) }

// Reveal returns the credential.
func (s Secret) Reveal() string { return string(s) }

// The IBM i host server ports.
const (
	PlaintextPort = 8471
	TLSPort       = 9471
)

// DefaultMaxRows caps a Select with no Limit (Config.MaxRows overrides it).
const DefaultMaxRows = 10000

// DefaultMinPasswordLevel is the minimum password level when Config names none: 2 means
// SHA-1 or PBKDF2, never DES.
const DefaultMinPasswordLevel = 2

// EffectiveMinPasswordLevel is MinPasswordLevel, or the default when it is 0.
func (c Config) EffectiveMinPasswordLevel() int {
	if c.MinPasswordLevel == 0 {
		return DefaultMinPasswordLevel
	}
	return c.MinPasswordLevel
}

// HardMaxRows is the largest Config.MaxRows Validate accepts. The cap is a memory bound
// (the rows are held in a slice), and the probe for "one row more than the cap" adds
// one to it, so the value must stay far from the integer limit.
const HardMaxRows = 1_000_000

// Config is the connection configuration.
//
// TLS is the default: the connection is encrypted unless AllowPlaintext is set, and
// the server certificate is always verified. There is no option to skip verification.
type Config struct {
	Host string
	Port int // 0 = 9471 (TLS) or 8471 (AllowPlaintext)
	// Library is REQUIRED: it is the connection's default schema. The driver connects with
	// SQL naming, under which an unqualified table name resolves in CURRENT SCHEMA, not in
	// the library list; with no library CURRENT SCHEMA is the user name, so a service would
	// read and write a schema nobody chose. (Confirmed on Db2 for i 7.5.)
	Library string
	User    string
	// Password is opaque: it prints, marshals and logs as [redacted].
	Password Secret
	// AllowPlaintext is the explicit opt-in to the unencrypted host server (port 8471):
	// credentials and data cross the network in clear. A warning is logged on every Open.
	AllowPlaintext bool
	// CAPool or CAFile (one of them) is the set of roots that verify the server
	// certificate; neither means the system roots. ServerName is the name the
	// certificate must carry (default Host).
	CAPool     *x509.CertPool
	CAFile     string
	ServerName string
	// CCSID declares the DEFAULT CCSID of the host job (273 on PUB400, 37 for US English,
	// and 37 also for a job whose CCSID is 65535). It is what a CHAR or VARCHAR column with
	// no CCSID clause gets (PUB400 round 3), and a character it lacks is stored as X'3F'
	// without an error (round 2), so the Store checks every text bound to such a column
	// against it. Open reads the host's value (Driver.DefaultCCSID) and refuses a
	// mismatch. Validate refuses 65535, 1200, 1208, 13488 and any CCSID it cannot check
	// text against. 0 is the explicit opt-out: only 7-bit ASCII is accepted for those
	// columns (SQLStateUnverifiableText); the mixed CCSIDs 930 and 5035 are accepted and
	// treated the same way. It declares, it does not convert: the driver translates by
	// each column's CCSID and has no override.
	CCSID int
	// MinPasswordLevel is the lowest password level (QPWDLVL) the server may announce:
	// 2, 3 or 4; 0 = DefaultMinPasswordLevel. The server chooses the level and the client
	// follows it, so without a minimum a hostile host could steer the sign-on to the DES
	// substitute (levels 0 and 1). The level is never lower than 2 here.
	MinPasswordLevel int
	CommitMode       CommitMode
	// MaxRows caps a Select with no Limit; 0 = DefaultMaxRows.
	MaxRows int
	// MaxLimit bounds Select.Limit: a larger Limit is refused. The row cap bounds the
	// memory of one result, not the load a read puts on the host; 0 = DefaultMaxLimit,
	// at most HardMaxRows. The store's own cap probe (MaxRows+1) is not a Limit.
	MaxLimit int
	// Pool limits. Every open connection is one IBM i host server job (QZDASOINIT), so
	// the pool is bounded, per Store and so per service instance (N replicas hold N times
	// these limits on the host; size DB2_MAX_OPEN_CONNS from the host's job budget divided
	// by the replicas):
	//   - MaxOpenConns 0 = DefaultMaxOpenConns, at most HardMaxOpenConns (64: with N
	//     replicas the host holds up to 64 x N jobs, each kept until its connection closes).
	//   - MaxIdleConns 0 = DefaultMaxIdleConns (never above the open limit); NoIdleConns
	//     (-1) keeps no idle connection, so every statement connects and the job ends
	//     with it. database/sql treats 0 as "none", which is why 0 cannot mean it here.
	//   - ConnMaxLifetime 0 = DefaultConnMaxLifetime; otherwise MinConnMaxLifetime to
	//     MaxConnMaxLifetime. A tiny lifetime would recycle every connection and churn
	//     host jobs, and none would keep a job for ever.
	//   - ConnMaxIdleTime 0 = DefaultConnMaxIdleTime; otherwise MinConnMaxIdleTime up to
	//     the lifetime. An idle connection keeps its host job, so it is closed sooner than
	//     the lifetime.
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	// QueryTimeout bounds every statement (and the connect check of Open) when the
	// caller's context has no earlier deadline; 0 = DefaultQueryTimeout, at most
	// MaxQueryTimeout (10 minutes: the deadline is the host-side cancel, measured to stop
	// the statement, so a longer one only holds a host job). A unit of work has no overall timeout of its own: each statement
	// in it has this one, and the caller's context bounds the whole.
	QueryTimeout time.Duration
}

// Pool and statement defaults (Config fields at 0).
const (
	DefaultMaxOpenConns    = 10
	DefaultMaxIdleConns    = 2
	DefaultConnMaxLifetime = 30 * time.Minute
	DefaultConnMaxIdleTime = 5 * time.Minute
	DefaultQueryTimeout    = 30 * time.Second
	DefaultMaxLimit        = 100_000
	HardMaxOpenConns       = 64
	MaxQueryTimeout        = 10 * time.Minute
	MinConnMaxLifetime     = time.Minute
	MaxConnMaxLifetime     = 24 * time.Hour
	MinConnMaxIdleTime     = 10 * time.Second
	// NoIdleConns is the MaxIdleConns value that keeps no idle connection.
	NoIdleConns = -1
)

type poolLimits struct {
	maxOpen, maxIdle                       int
	maxLimit                               int
	maxLifetime, maxIdleTime, queryTimeout time.Duration
}

// limits are the effective pool and statement limits.
func (c Config) limits() poolLimits {
	p := poolLimits{maxOpen: c.MaxOpenConns, maxIdle: c.MaxIdleConns, maxLimit: c.MaxLimit,
		maxLifetime: c.ConnMaxLifetime, maxIdleTime: c.ConnMaxIdleTime, queryTimeout: c.QueryTimeout}
	if p.maxOpen == 0 {
		p.maxOpen = DefaultMaxOpenConns
	}
	switch p.maxIdle {
	case 0:
		p.maxIdle = min(DefaultMaxIdleConns, p.maxOpen)
	case NoIdleConns:
		p.maxIdle = 0
	}
	if p.maxLimit == 0 {
		p.maxLimit = DefaultMaxLimit
	}
	if p.maxLifetime == 0 {
		p.maxLifetime = DefaultConnMaxLifetime
	}
	if p.maxIdleTime == 0 {
		p.maxIdleTime = min(DefaultConnMaxIdleTime, p.maxLifetime)
	}
	if p.queryTimeout == 0 {
		p.queryTimeout = DefaultQueryTimeout
	}
	return p
}

func (c Config) validateLimits() error {
	switch {
	case c.MaxOpenConns < 0 || c.MaxOpenConns > HardMaxOpenConns:
		return fmt.Errorf("db2access: MaxOpenConns %d is outside 0..%d (each connection is one IBM i host server job)", c.MaxOpenConns, HardMaxOpenConns)
	case c.MaxIdleConns < NoIdleConns:
		return fmt.Errorf("db2access: MaxIdleConns %d is below %d (NoIdleConns)", c.MaxIdleConns, NoIdleConns)
	case c.MaxIdleConns > c.limits().maxOpen:
		return fmt.Errorf("db2access: MaxIdleConns %d is above the open-connection limit %d", c.MaxIdleConns, c.limits().maxOpen)
	case c.ConnMaxLifetime != 0 && (c.ConnMaxLifetime < MinConnMaxLifetime || c.ConnMaxLifetime > MaxConnMaxLifetime):
		return fmt.Errorf("db2access: ConnMaxLifetime %s is outside %s..%s (a shorter one churns IBM i host server jobs, a longer one keeps them for ever)", c.ConnMaxLifetime, MinConnMaxLifetime, MaxConnMaxLifetime)
	case c.ConnMaxIdleTime != 0 && (c.ConnMaxIdleTime < MinConnMaxIdleTime || c.ConnMaxIdleTime > c.limits().maxLifetime):
		return fmt.Errorf("db2access: ConnMaxIdleTime %s is outside %s..the connection lifetime %s", c.ConnMaxIdleTime, MinConnMaxIdleTime, c.limits().maxLifetime)
	case c.MaxLimit < 0 || c.MaxLimit > HardMaxRows:
		return fmt.Errorf("db2access: MaxLimit %d is outside 0..%d", c.MaxLimit, HardMaxRows)
	case c.QueryTimeout < 0 || c.QueryTimeout > MaxQueryTimeout:
		return fmt.Errorf("db2access: QueryTimeout %s is outside 0..%s", c.QueryTimeout, MaxQueryTimeout)
	}
	return nil
}

// MaxPasswordChars is the longest IBM i password (QPWDMAXLEN 128, counted in characters).
// A longer one fails the sign-on, and every failed sign-on counts against the profile's
// QMAXSIGN.
const MaxPasswordChars = 128

// validPassword refuses a password the host cannot have: invalid UTF-8, over MaxPasswordChars
// characters, or one carrying a character a paste or a mounted secret smuggles in. A control
// character is refused first (a mounted Secret often ends in a newline, and the message says
// so), then a format character (zero-width, BOM, bidi controls, soft hyphen), then the line
// and paragraph separators (U+2028, U+2029). A blank inside the password is a legitimate
// character, but one at either end (a space, no-break space or ideographic space) is
// nearly always a paste or mount artefact, and a wrong password repeats at every sign-on
// (QMAXSIGN), so it is refused (A59). Errors name the variable and the problem, never the value.
var errBlankAtEnd = errors.New("db2access: DB2_PASSWORD begins or ends with a blank (check how it was pasted or mounted); a wrong password repeats at every sign-on and can disable the IBM i profile (QMAXSIGN)")

func validPassword(pw string) error {
	if !utf8.ValidString(pw) {
		return errors.New("db2access: the password is not valid UTF-8 (check how DB2_PASSWORD was written); the driver would send a different one")
	}
	if strings.IndexFunc(pw, unicode.IsControl) >= 0 {
		return errors.New("db2access: the password holds a control character (DB2_PASSWORD from a mounted Secret often ends in a newline: write the file without one); a wrong password repeats at every sign-on and can disable the IBM i profile (QMAXSIGN)")
	}
	if strings.IndexFunc(pw, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) >= 0 {
		return errors.New("db2access: the password holds an invisible format character (check how DB2_PASSWORD was pasted or mounted); a wrong password repeats at every sign-on and can disable the IBM i profile (QMAXSIGN)")
	}
	if strings.ContainsAny(pw, "\u2028\u2029") {
		return errors.New("db2access: DB2_PASSWORD holds a line or paragraph separator (check how it was pasted or mounted); a wrong password repeats at every sign-on and can disable the IBM i profile (QMAXSIGN)")
	}
	if first, _ := utf8.DecodeRuneInString(pw); unicode.IsSpace(first) || unicode.Is(unicode.Zs, first) {
		return errBlankAtEnd
	}
	if last, _ := utf8.DecodeLastRuneInString(pw); unicode.IsSpace(last) || unicode.Is(unicode.Zs, last) {
		return errBlankAtEnd
	}
	if utf8.RuneCountInString(pw) > MaxPasswordChars {
		return fmt.Errorf("db2access: the password is longer than %d characters (the IBM i maximum); a longer one fails the sign-on and counts against the profile's QMAXSIGN", MaxPasswordChars)
	}
	return nil
}

// validJobCCSID refuses a job CCSID that would switch the text guard off or make it wrong.
// 0 is the explicit opt-out (7-bit ASCII only). 65535 means "no conversion" (and a job with
// it gives new columns CCSID 37, so declare 37), and 1208, 1200 and 13488 are Unicode: the
// guard holds anything for them, which is right only for a column that says so itself. A
// CCSID this package has no table for, other than the mixed 930 and 5035 it limits to ASCII,
// would be pass-through. No error carries the value.
func validJobCCSID(id int) error {
	switch {
	case id == 0 || mixedCCSID(id):
		return nil
	case id < 0 || id > BinaryCCSID:
		return errors.New("db2access: DB2_CCSID is outside 0..65535")
	case id == BinaryCCSID || unicodeCCSID(id):
		return errors.New("db2access: DB2_CCSID must be the job's default CCSID, an EBCDIC single-byte one (37, 273, 500, ...): the no-conversion CCSID (a job set to it gives new columns 37) and the Unicode CCSIDs would switch the text guard off; use 0 to accept 7-bit ASCII only")
	}
	if cp, ok := ebcdic.Lookup(id); !ok || cp.PassThrough() {
		return errors.New("db2access: DB2_CCSID is not a CCSID this package can check text against (single-byte EBCDIC, or 930/5035 which accept ASCII only); use 0 to accept 7-bit ASCII only")
	}
	return nil
}

// UseTLS reports whether the connection is encrypted.
func (c Config) UseTLS() bool { return !c.AllowPlaintext }

// EffectivePort is Port, or the default of the chosen transport.
func (c Config) EffectivePort() int {
	switch {
	case c.Port > 0:
		return c.Port
	case c.UseTLS():
		return TLSPort
	}
	return PlaintextPort
}

// Validate checks the configuration without touching the network.
func (c Config) Validate() error {
	if _, err := ParseCommitMode(string(c.CommitMode)); err != nil {
		return err
	}
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("db2access: host is required")
	}
	if !validHost(c.Host) {
		return errors.New("db2access: host must be a DNS name or an IP address (letters, digits, '.', '-', '_'; no '@', '/', '?', '#', ':' or spaces)")
	}
	if strings.TrimSpace(c.Library) == "" {
		return errors.New("db2access: library is required (the default schema: under SQL naming an unqualified table resolves in it, and with none CURRENT SCHEMA would be the user name)")
	}
	if !validLibrary(c.Library) {
		return errors.New("db2access: library is not an IBM i library name (1 to 10 characters: A-Z, 0-9, $, #, @, _; the first is a letter, $, # or @)")
	}
	if strings.TrimSpace(c.User) == "" {
		return errors.New("db2access: user is required")
	}
	if c.Password == "" {
		return errors.New("db2access: password is required (DB2_PASSWORD is empty); an empty one only fails at sign-on and counts against the profile's QMAXSIGN")
	}
	if err := validPassword(string(c.Password)); err != nil {
		return err
	}
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("db2access: port %d is outside 0..65535", c.Port)
	}
	if err := validJobCCSID(c.CCSID); err != nil {
		return err
	}
	if c.MinPasswordLevel != 0 && (c.MinPasswordLevel < 2 || c.MinPasswordLevel > 4) {
		return errors.New("db2access: MinPasswordLevel must be 2, 3 or 4 (0 = the default, 2): level 0 and 1 are the DES substitute")
	}
	if c.MaxRows < 0 {
		return errors.New("db2access: MaxRows must not be negative")
	}
	if c.MaxRows > HardMaxRows {
		return fmt.Errorf("db2access: MaxRows %d is above the hard cap of %d rows", c.MaxRows, HardMaxRows)
	}
	if err := c.validateLimits(); err != nil {
		return err
	}
	switch p := c.EffectivePort(); {
	case c.UseTLS() && p == PlaintextPort:
		return fmt.Errorf("db2access: port %d is the plaintext host server and TLS is on (the default); use port %d, or set AllowPlaintext to accept an unencrypted connection", PlaintextPort, TLSPort)
	case !c.UseTLS() && p == TLSPort:
		return fmt.Errorf("db2access: AllowPlaintext is set but port %d is the TLS host server", TLSPort)
	}
	if c.CAPool != nil && c.CAFile != "" {
		return errors.New("db2access: set CAPool or CAFile, not both")
	}
	return nil
}

// TLSConfig builds the verified TLS configuration the driver must use, or nil for an
// AllowPlaintext connection. It never sets InsecureSkipVerify.
func (c Config) TLSConfig() (*tls.Config, error) {
	if !c.UseTLS() {
		return nil, nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ServerName, RootCAs: c.CAPool}
	if tc.ServerName == "" {
		tc.ServerName = c.Host
	}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("db2access: cannot read the CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("db2access: the CA file holds no PEM certificate")
		}
		tc.RootCAs = pool
	}
	return tc, nil
}

// String redacts the credential.
func (c Config) String() string {
	return fmt.Sprintf("db2access.Config{host=%s port=%d library=%s user=%s password=%s tls=%t ccsid=%d commit=%s}",
		c.Host, c.EffectivePort(), c.Library, c.User, redacted, c.UseTLS(), c.CCSID, c.CommitMode)
}

// GoString keeps %#v from printing the password.
func (c Config) GoString() string { return c.String() }

// LogValue keeps slog from printing the password.
func (c Config) LogValue() slog.Value { return slog.StringValue(c.String()) }

// Driver is the port the IBM i adapter implements (phase 3, the pinned i400 fork); a
// fake implements it in tests.
//
// Contract:
//   - Open returns a pool for cfg. It uses cfg.TLSConfig() when cfg.UseTLS(), reads the
//     password only through cfg.Password.Reveal(), and must not return it (or a DSN) in
//     errors.
//   - The pool MUST honour sql.TxOptions.Isolation: InTx begins every transaction with
//     the isolation level its commit mode maps to, and a driver that ignores it would
//     silently run *CS or *RR work at its own default. Phase 3 proves it on a real host.
//   - Journaled reports whether table t is journaled, so a write under commitment
//     control will not fail with SQL7008. When t.Schema() is empty the name resolves the
//     way the SQL does for the connection: under SQL naming (the IBM i driver's mode)
//     through CURRENT SCHEMA, which Config.Library sets; the adapter must check the file
//     that resolution finds. A driver for a database
//     without the notion (Db2 LUW, z/OS) returns true.
//   - DefaultCCSID reports the DEFAULT CCSID of the host job behind db: the CCSID a new
//     column without a CCSID clause gets (PUB400 round 3: 37 when the job CCSID is 65535,
//     not the job's own value). Open compares it with Config.CCSID and refuses a mismatch.
type Driver interface {
	Open(cfg Config) (*sql.DB, error)
	Journaled(ctx context.Context, db *sql.DB, t Table) (bool, error)
	DefaultCCSID(ctx context.Context, db *sql.DB) (int, error)
}

// validHost accepts a DNS name or an IP address and nothing that could rewrite where the
// connector dials: no user-info, path, query, fragment, port or whitespace.
func validHost(h string) bool {
	if h == "" || len(h) > 255 {
		return false
	}
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		return net.ParseIP(h[1:len(h)-1]) != nil
	}
	if net.ParseIP(h) != nil {
		return true
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// validLibrary is the IBM i library name rule for a simple (unquoted) name.
func validLibrary(l string) bool {
	l = strings.ToUpper(l)
	if len(l) < 1 || len(l) > 10 {
		return false
	}
	for i, r := range l {
		letter := r >= 'A' && r <= 'Z'
		special := r == '$' || r == '#' || r == '@'
		digitOrUnderscore := (r >= '0' && r <= '9') || r == '_'
		if i == 0 && !(letter || special) || i > 0 && !(letter || special || digitOrUnderscore) {
			return false
		}
	}
	return true
}
