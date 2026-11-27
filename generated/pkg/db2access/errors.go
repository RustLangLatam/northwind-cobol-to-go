package db2access

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Error is a database failure with its SQLSTATE and SQLCODE and a fixed message.
// The driver's own text is dropped on purpose: it can carry a connection string.
type Error struct {
	Op       string
	SQLState string
	SQLCode  int
	Message  string
	Tables   []string // set for the journaling failure
	// Unknown is set when a COMMIT was interrupted: the unit of work may or may not
	// have been committed. Read back before retrying.
	Unknown bool
	ctxErr  error // context.Canceled or context.DeadlineExceeded, for errors.Is
}

func (e *Error) Error() string {
	s := fmt.Sprintf("db2access: %s: %s", e.Op, e.Message)
	if e.SQLState != "" || e.SQLCode != 0 {
		s += fmt.Sprintf(" (SQLSTATE %s, SQLCODE %d)", e.SQLState, e.SQLCode)
	}
	if len(e.Tables) > 0 {
		s += ": " + strings.Join(e.Tables, ", ")
	}
	return s
}

// Is makes errors.Is(err, context.Canceled) and context.DeadlineExceeded true for an
// Error that came from a context ending, without carrying the wrapped error's text.
func (e *Error) Is(target error) bool { return e.ctxErr != nil && target == e.ctxErr }

// Retryable reports a lock timeout or deadlock: the unit of work may be retried.
func (e *Error) Retryable() bool {
	return e.SQLState == "40001" || e.SQLState == "57033" || e.SQLCode == -911 || e.SQLCode == -913
}

// Journal reports the SQL7008 (SQLSTATE 55019) condition.
func (e *Error) Journal() bool { return e.SQLCode == -7008 || e.SQLState == "55019" }

const msgNotJournaled = "SQL7008: a table is not journaled (reason code 3), so it cannot be changed under commitment control; journal the physical file (STRJRNPF) or run with commit mode *NONE"

type sqlStateError interface{ SQLState() string }
type sqlCodeError interface{ SQLCode() int }

var byCode = map[int]string{
	-7008:  msgNotJournaled,
	-204:   "table or object not found (check the library list and the table name)",
	-206:   "column not found",
	-803:   "duplicate key: a row with this unique key already exists",
	-530:   "foreign key violation: the parent row does not exist",
	-532:   "foreign key violation: dependent rows exist",
	-407:   "null value in a NOT NULL column",
	-433:   "value too long for the column",
	-302:   "value does not fit the column or has the wrong type",
	-420:   "value is not a valid number or date for the column",
	-551:   "not authorized to the object",
	-552:   "not authorized to the operation",
	-913:   "lock timeout: another job holds the row",
	-911:   "deadlock or lock timeout: the unit of work was rolled back",
	-104:   "SQL syntax error",
	-30082: "authentication failed or the user is not allowed to connect",
}

var byState = map[string]string{
	"08001": "cannot connect to the database", "08004": "the database refused the connection",
	"08S01": "connection to the database was lost", "08003": "connection to the database is closed",
	"28000": "authentication failed", "42501": "not authorized to the object",
	"23505": byCode[-803], "40001": byCode[-911], "57033": byCode[-913], "55019": msgNotJournaled,
}

// Classify turns any error from the driver or database/sql into an *Error with a fixed
// message. It never returns the driver's error or a wrapper around it, because its
// text can carry a DSN or a password: a context error becomes an Error that answers
// errors.Is for the context kind; an error that reports no SQLSTATE or SQLCODE gets a
// generic message. nil stays nil, and sql.ErrNoRows is returned as the sentinel itself.
func Classify(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed // ours: fixed text; the wrapper around it is dropped
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Op: op, Message: "the operation timed out", ctxErr: context.DeadlineExceeded}
	case errors.Is(err, context.Canceled):
		return &Error{Op: op, Message: "the operation was canceled", ctxErr: context.Canceled}
	}
	e := &Error{Op: op}
	var st sqlStateError
	if errors.As(err, &st) {
		e.SQLState = st.SQLState()
	}
	var cd sqlCodeError
	if errors.As(err, &cd) {
		e.SQLCode = cd.SQLCode()
	}
	switch {
	case byCode[e.SQLCode] != "":
		e.Message = byCode[e.SQLCode]
	case byState[e.SQLState] != "":
		e.Message = byState[e.SQLState]
	case e.SQLState == "" && e.SQLCode == 0:
		e.Message = "the database reported an error without SQLSTATE or SQLCODE"
	default:
		e.Message = "the database rejected the statement"
	}
	return e
}

// classifyCommit is Classify for COMMIT: an interrupted commit has an unknown outcome.
func classifyCommit(err error) error {
	err = Classify("commit", err)
	var e *Error
	if errors.As(err, &e) && e.ctxErr != nil {
		return &Error{Op: "commit", Unknown: true, ctxErr: e.ctxErr,
			Message: "commit outcome unknown: the context ended during COMMIT, so the unit of work may or may not be committed; read it back before retrying"}
	}
	return err
}

// SQLStateNotRepresentable is the SQLSTATE-like code of the platform guard that refuses a
// character its column's CCSID cannot represent. It is NOT a host code: the host (measured
// on PUB400, CCSID 37) accepts such a character silently and stores the substitution
// character X'3F', so db2access refuses before sending. "PG" is an implementation-defined
// class (first character I-Z), so it cannot clash with a standard one.
const SQLStateNotRepresentable = "PG001"

// SQLStateUnverifiableText is the platform guard's code for text it cannot verify: the
// column's CCSID (its own, or the job CCSID of Config.CCSID / DB2_CCSID when it has none)
// is unknown, multibyte or mixed (930, 5035), so only 7-bit ASCII is let through. Set
// DB2_CCSID to the host's job CCSID to write anything else. Like PG001 it is not a host
// code.
const SQLStateUnverifiableText = "PG002"

// SQLStateInvalidText is the platform guard's code for text that is not valid UTF-8 (a
// lone surrogate, a cut sequence). The driver turns every bad byte into U+FFFD and sends
// it with no warning (PUB400 round 3), so the value that is stored is not the one the
// caller gave. Not a host code.
const SQLStateInvalidText = "PG003"

// SQLStateBinaryTooLong is the platform guard's code for a []byte longer than its column.
// The i400 fork v0.1.2 cuts a BINARY(n) value to n bytes without an error and matches a
// longer WHERE key against the cut value (PUB400 round 3, F01); the host itself would
// raise 22001 for a non-zero excess. Not a host code.
const SQLStateBinaryTooLong = "PG004"

// SQLStateCCSIDMismatch is the code of the Open-time refusal when DB2_CCSID is not the
// default CCSID of the host job (the CCSID a new column without a CCSID clause gets). A
// wrong value would make the text guard wrong. Not a host code.
const SQLStateCCSIDMismatch = "PG005"
