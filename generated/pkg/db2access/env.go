package db2access

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The environment variables ConfigFromEnv reads. The names are the ones the generated
// deliverable writes into .env.example and the Kubernetes ConfigMap and Secret.
const (
	EnvHost             = "DB2_HOST"
	EnvPort             = "DB2_PORT"
	EnvLibrary          = "DB2_LIBRARY"
	EnvUser             = "DB2_USER"
	EnvPassword         = "DB2_PASSWORD"
	EnvCommitMode       = "DB2_COMMIT_MODE"
	EnvCCSID            = "DB2_CCSID"
	EnvCAFile           = "DB2_CA_FILE"
	EnvServerName       = "DB2_SERVER_NAME"
	EnvAllowPlaintext   = "DB2_ALLOW_PLAINTEXT"
	EnvMinPasswordLevel = "DB2_MIN_PASSWORD_LEVEL"
	EnvMaxRows          = "DB2_MAX_ROWS"
	EnvMaxOpenConns     = "DB2_MAX_OPEN_CONNS"
	EnvMaxIdleConns     = "DB2_MAX_IDLE_CONNS"
	EnvMaxLimit         = "DB2_MAX_LIMIT"
	EnvConnMaxLifetime  = "DB2_CONN_MAX_LIFETIME"  // a Go duration: 30m, 1h
	EnvConnMaxIdleTime  = "DB2_CONN_MAX_IDLE_TIME" // a Go duration: 5m
	EnvQueryTimeout     = "DB2_QUERY_TIMEOUT"      // a Go duration: 30s, 2m
)

// ConfigFromEnv reads the connection configuration from the environment (pass
// os.Getenv) and validates it. It is the one loader every generated Db2 service uses,
// so no service can forget a variable. An unset or blank variable takes the Config
// default; DB2_COMMIT_MODE has none and is required. A variable that is set must mean
// something: an explicit zero for a port, row cap, limit, timeout, pool size or
// duration is refused (leave it unset for the default), except DB2_MAX_IDLE_CONNS=0,
// which means no idle connection. Every value is trimmed except the password, which is
// taken as it is. No error carries a value read from the environment, only the variable
// name, so none can echo the password.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("db2access: no environment reader: ConfigFromEnv needs os.Getenv or an equivalent")
	}
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	var errs []string
	// num reads an integer; set tells an explicit value (even 0) from an unset one.
	num := func(k string) (n int, set bool) {
		v := get(k)
		if v == "" {
			return 0, false
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, k+" must be an integer")
		}
		return n, true
	}
	positive := func(k string) int {
		n, set := num(k)
		if set && n == 0 {
			errs = append(errs, k+" must be greater than zero (leave it unset for the default)")
		}
		return n
	}
	optional := func(k string) int { n, _ := num(k); return n } // 0 is meaningful: the default level
	// DB2_CCSID: the generated .env.example carries SET_ME, which startup refuses (A48), so a
	// service cannot run with a CCSID nobody chose. 0 is the explicit opt-out (ASCII only).
	ccsid := func() int {
		if strings.EqualFold(get(EnvCCSID), "SET_ME") {
			errs = append(errs, EnvCCSID+" is still the SET_ME placeholder of .env.example: set the host job's default CCSID (273, 37, ...), or 0 to accept 7-bit ASCII only")
			return 0
		}
		return optional(EnvCCSID)
	}
	dur := func(k string) time.Duration {
		v := get(k)
		if v == "" {
			return 0
		}
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			errs = append(errs, k+" must be a duration such as 30s or 10m")
		case d == 0:
			errs = append(errs, k+" must be greater than zero (leave it unset for the default)")
		}
		return d
	}
	idle, idleSet := num(EnvMaxIdleConns)
	switch {
	case idleSet && idle == 0:
		idle = NoIdleConns
	case idle < 0:
		errs = append(errs, EnvMaxIdleConns+" must be zero (no idle connection) or more")
	}
	c := Config{
		Host: get(EnvHost), Port: positive(EnvPort), Library: get(EnvLibrary), User: get(EnvUser),
		Password: Secret(getenv(EnvPassword)), CAFile: get(EnvCAFile), ServerName: get(EnvServerName),
		CCSID: ccsid(), MinPasswordLevel: optional(EnvMinPasswordLevel), MaxRows: positive(EnvMaxRows),
		MaxLimit:     positive(EnvMaxLimit),
		CommitMode:   CommitMode(get(EnvCommitMode)),
		MaxOpenConns: positive(EnvMaxOpenConns), MaxIdleConns: idle,
		ConnMaxLifetime: dur(EnvConnMaxLifetime), ConnMaxIdleTime: dur(EnvConnMaxIdleTime), QueryTimeout: dur(EnvQueryTimeout),
	}
	// Only the words true and false: the plaintext opt-in must not be set by a stray 1 or T.
	switch v := strings.ToLower(get(EnvAllowPlaintext)); v {
	case "", "false":
	case "true":
		c.AllowPlaintext = true
	default:
		errs = append(errs, EnvAllowPlaintext+" must be true or false")
	}
	if len(errs) > 0 {
		return Config{}, fmt.Errorf("db2access: %s", strings.Join(errs, "; "))
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}
