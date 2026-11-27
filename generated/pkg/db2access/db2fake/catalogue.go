package db2fake

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/RustLangLatam/cobolnum-go/ebcdic"

	"milton_prism/pkg/db2access"
)

// DefaultNow is the fake's clock until SetNow: the value a CURRENT_DATE, CURRENT_TIME or
// CURRENT_TIMESTAMP default (and the DDS default of a temporal field) takes, fixed so
// tests are deterministic.
var DefaultNow = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// declTable is one declared table: its columns in declaration order and by name.
type declTable struct {
	cols   []db2access.Column
	byName map[string]db2access.Column
	unique [][]string // declared unique keys (db2access.Table.WithUniqueKey)
	job    int        // the job CCSID the fake was given (Open, SetJobCCSID); 0 = unknown
}

// Declare gives the fake the catalogue of each table: its columns with type, length,
// scale, nullability and default, exactly as the service declares them for db2access
// (ParseColumn, ColumnFromFacts, AllowNull, WithDefault). A statement on a table that
// was not declared, or naming a column the declaration lacks, is an error. Declaring a
// table again replaces its declaration; rows already stored are kept.
func (f *Fake) Declare(tables ...db2access.Table) {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	for _, t := range tables {
		d := declTable{cols: t.Columns(), byName: map[string]db2access.Column{}, unique: t.UniqueKeys()}
		for _, c := range d.cols {
			d.byName[c.Name()] = c
		}
		f.st.tables[t.Qualified()] = d
	}
}

// SetNow sets the clock of CURRENT_* defaults.
func (f *Fake) SetNow(t time.Time) {
	f.st.mu.Lock()
	f.st.now = t
	f.st.mu.Unlock()
}

// declared returns the declaration of the table a statement names, checking that every
// column the statement uses is declared. The caller holds mu.
func (st *state) declared(s stmt, job int) (declTable, error) {
	d, ok := st.tables[s.table]
	if !ok {
		return declTable{}, fmt.Errorf("db2fake: table %s has no declared columns; call fake.Declare with the db2access.Table first", s.table)
	}
	use := func(n string) error {
		if _, ok := d.byName[n]; !ok {
			return fmt.Errorf("db2fake: table %s declares no column %s", s.table, n)
		}
		return nil
	}
	for _, it := range s.items {
		if err := use(it.col); err != nil {
			return declTable{}, err
		}
	}
	for _, c := range s.where {
		if err := use(c.col); err != nil {
			return declTable{}, err
		}
	}
	for _, o := range s.order {
		if err := use(o.col); err != nil {
			return declTable{}, err
		}
	}
	for _, a := range s.assign {
		if err := use(a.col); err != nil {
			return declTable{}, err
		}
	}
	d.job = job
	return d, nil
}

// The SQLSTATEs Db2 for i returns for a value its column cannot hold.
var (
	errNotNull  = DBError{SQLSTATE: "23502", SQLCODE: -407} // NULL into a NOT NULL column
	errTooLong  = DBError{SQLSTATE: "22001", SQLCODE: -404} // value longer than the column (SQL0404)
	errRange    = DBError{SQLSTATE: "22003", SQLCODE: -406} // number out of the column's range
	errDatetime = DBError{SQLSTATE: "22007", SQLCODE: -180} // temporal text of the wrong form (SQL0180)
	errDateVal  = DBError{SQLSTATE: "22007", SQLCODE: -181} // well-formed text that is no date or time (SQL0181)
	errUnique   = DBError{SQLSTATE: "23505", SQLCODE: -803} // duplicate key (SQL0803)
	// A character CCSID 37 cannot hold is NOT an error on the host: it stores X'3F' (PUB400,
	// section 6), so the host raises no 22021. The fake applies db2access's platform guard
	// (Column.CheckText) instead, with its own code.

	// errDecimalDigits is what the i400 driver does with a DECIMAL parameter that has more
	// fraction digits than the column: it refuses before sending, with no SQLSTATE (the
	// host would truncate, PUB400 section 5). db2access sends parameters only.
	// errParamTooLong is what the i400 driver does with a text parameter longer than the
	// column, excess blanks included: it refuses before sending, with no SQLSTATE (PUB400
	// round 1, section 6). The host accepts excess blanks in a literal.
	errParamTooLong = errors.New("db2fake: parameter value too long (the i400 driver refuses it before sending, even when the excess is blanks)")

	errDecimalDigits = errors.New("db2fake: parameter value has digits that DECIMAL cannot hold (the i400 driver refuses it before sending; the host would truncate it)")
)

// effectiveCCSID is the column's CCSID, or the job's when it declares none.
func effectiveCCSID(c db2access.Column, job int) int {
	if c.CCSID() != 0 {
		return c.CCSID()
	}
	return job
}

// textLength is the length of s as the column counts it: bytes for UTF-8 (CCSID 1208),
// bytes of the EBCDIC encoding for a known EBCDIC CCSID (a character outside it is an
// error), characters otherwise.
func textLength(c db2access.Column, job int, s string) int {
	id := effectiveCCSID(c, job)
	if c.Graphic() { // UTF-16 code units: a character beyond the BMP counts 2 (round 3)
		return len(utf16.Encode([]rune(s)))
	}
	if id == 1208 {
		return len(s)
	}
	if cp, ok := ebcdic.Lookup(id); ok && !cp.PassThrough() {
		if b, err := cp.Strict().Encode(s); err == nil { // db2access.Column.CheckText ran first
			return len(b)
		}
	}
	return utf8.RuneCountInString(s)
}

// store converts a bound value to what Db2 holds in the column: NULL only where the
// column allows it, the marker kind matching the column kind, a number within the
// column's precision (at its scale) or integer range, temporal text of the column's
// form, CHAR and BINARY padded to their length, nothing longer than the column.
func store(c db2access.Column, job int, param bool, v any) (any, error) {
	if v == nil {
		if !c.Nullable() {
			return nil, errNotNull
		}
		return nil, nil
	}
	mismatch := fmt.Errorf("db2fake: column %s: a %T does not match its declared type", c.Name(), v)
	fixed, pad := c.FixedLength()
	switch c.Kind() {
	case db2access.KindExactNum:
		d, ok := v.(decCell)
		if !ok {
			return nil, mismatch
		}
		return canonDecimal(c, string(d))
	case db2access.KindDate, db2access.KindTime, db2access.KindTimestamp:
		t, ok := v.(tempCell)
		if !ok {
			return nil, mismatch
		}
		if len(t) != temporalWidth(c) {
			return nil, errDatetime
		}
		if err := checkTemporal(c, string(t)); err != nil {
			return nil, err
		}
		return t, nil
	case db2access.KindInt:
		n, ok := v.(int64)
		if !ok {
			return nil, mismatch
		}
		if lo, hi := c.IntRange(); n < lo || n > hi {
			return nil, errRange
		}
		return n, nil
	case db2access.KindText:
		s, ok := v.(string)
		if !ok {
			return nil, mismatch
		}
		if err := c.CheckTextFor(job, s); err != nil {
			return nil, err
		}
		n := textLength(c, job, s)
		if n > c.Length() {
			if param { // the driver refuses any text parameter over the length, before the host sees it
				return nil, errParamTooLong
			}
			s = strings.TrimRight(s, " ") // a literal: blanks beyond the length are dropped
			if n = textLength(c, job, s); n > c.Length() {
				return nil, errTooLong
			}
		}
		if fixed {
			s += strings.Repeat(" ", c.Length()-n)
		}
		return s, nil
	case db2access.KindBytes:
		b, ok := v.([]byte)
		if !ok {
			return nil, mismatch
		}
		if len(b) > c.Length() {
			if param && c.Binary() { // the fork refuses it before sending, BINARY as VARBINARY (round 3, A46/F01)
				return nil, errParamTooLong
			}
			// A literal (a default): BINARY and VARBINARY drop an excess that is only X'00'
			// silently (round 2, A27b); any other excess is SQL0404.
			if !c.Binary() || strings.Trim(string(b[c.Length():]), "\x00") != "" {
				return nil, errTooLong
			}
			b = b[:c.Length()]
		}
		out := make([]byte, len(b), max(len(b), c.Length()))
		copy(out, b)
		for fixed && len(out) < c.Length() {
			out = append(out, pad)
		}
		return out, nil
	}
	return nil, mismatch
}

// canonDecimal checks a DECIMAL value against the column and returns it at the column's
// scale. Too many integer digits are SQLSTATE 22003 / -406. Fraction digits past the scale
// are an error unless they are all zero: the host truncates them silently (12.349 is stored
// as 12.34, PUB400 section 5), but the i400 driver refuses such a parameter before sending
// and db2access only sends parameters, so the fake refuses too.
func canonDecimal(c db2access.Column, s string) (decCell, error) {
	neg := strings.HasPrefix(s, "-")
	t := strings.TrimLeft(s, "+-")
	ip, fp, _ := strings.Cut(t, ".")
	ip = strings.TrimLeft(ip, "0")
	if len(ip) > c.Precision()-c.Scale() {
		return "", errRange
	}
	if len(fp) > c.Scale() {
		if strings.Trim(fp[c.Scale():], "0") != "" {
			return "", errDecimalDigits
		}
		fp = fp[:c.Scale()]
	}
	fp += strings.Repeat("0", c.Scale()-len(fp))
	if ip == "" {
		ip = "0"
	}
	out := ip
	if c.Scale() > 0 {
		out += "." + fp
	}
	if neg && strings.Trim(ip+fp, "0") != "" {
		out = "-" + out
	}
	return decCell(out), nil
}

// temporalWidth is the length of the host's ISO text of a DATE, TIME or TIMESTAMP(p).
func temporalWidth(c db2access.Column) int {
	switch c.Kind() {
	case db2access.KindDate:
		return 10
	case db2access.KindTime:
		return 8
	}
	if c.Precision() == 0 {
		return 19
	}
	return 20 + c.Precision()
}

// defaultOf is what Db2 stores in a column an INSERT omits.
func defaultOf(c db2access.Column, job int, now time.Time) (any, error) {
	rule, text := c.Default()
	switch rule {
	case db2access.DefaultNull:
		return nil, nil
	case db2access.DefaultNone:
		return nil, errNotNull
	case db2access.DefaultLiteral:
		switch c.Kind() {
		case db2access.KindExactNum:
			return store(c, job, false, decCell(text))
		case db2access.KindDate, db2access.KindTime, db2access.KindTimestamp:
			return store(c, job, false, tempCell(text))
		case db2access.KindInt:
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("db2fake: column %s: the declared default is not an integer", c.Name())
			}
			return store(c, job, false, n)
		case db2access.KindBytes:
			return store(c, job, false, []byte(text))
		}
		return store(c, job, false, text)
	case db2access.DefaultCurrent:
		return tempCell(hostNow(c, now)), nil
	}
	// DefaultImplied: blanks, the empty string, zeros, the current date or time.
	switch c.Kind() {
	case db2access.KindText:
		return store(c, job, false, "")
	case db2access.KindBytes:
		return store(c, job, false, []byte{})
	case db2access.KindExactNum:
		return store(c, job, false, decCell("0"))
	case db2access.KindInt:
		return int64(0), nil
	}
	return tempCell(hostNow(c, now)), nil
}

// hostNow is now as the host's ISO text for the temporal column.
func hostNow(c db2access.Column, now time.Time) string {
	switch c.Kind() {
	case db2access.KindDate:
		return now.Format("2006-01-02")
	case db2access.KindTime:
		return now.Format("15.04.05")
	}
	s := now.Format("2006-01-02-15.04.05")
	if p := c.Precision(); p > 0 {
		frac := fmt.Sprintf("%09d", now.Nanosecond()) + "000"
		s += "." + frac[:p]
	}
	return s
}

// boundFor converts the arguments (bindArgs) and pads those compared with a
// fixed-length binary column.
func boundFor(d declTable, s stmt, args []driver.NamedValue) ([]any, error) {
	vals, err := bindArgs(s, args)
	if err != nil {
		return nil, err
	}
	for _, c := range s.where {
		if c.op != "IS NULL" && c.op != "IS NOT NULL" {
			col := d.byName[c.col]
			if err := checkWhereLength(col, vals[c.arg]); err != nil {
				return nil, err
			}
			vals[c.arg] = padArg(col, vals[c.arg])
		}
	}
	return vals, nil
}

// checkWhereLength is what the fork does with a comparison parameter longer than its column:
// it sizes the parameter from the column and refuses the excess before sending, for BINARY and
// VARBINARY (round 3, F01: a longer key matched the cut value under fork v0.1.2) and for graphic
// text (blanks included).
func checkWhereLength(c db2access.Column, v any) error {
	switch x := v.(type) {
	case []byte:
		if c.Binary() && len(x) > c.Length() {
			return errParamTooLong
		}
	case string:
		if c.Graphic() && textLength(c, 0, x) > c.Length() {
			return errParamTooLong
		}
	}
	return nil
}

// padArg pads a comparison argument of a fixed-length FOR BIT DATA column with X'40'. A
// BINARY comparison does NOT pad: the shorter operand stays shorter and never equals
// the padded value (PUB400 round 2, A27b: B = BX'01' matched nothing). FOR BIT DATA
// padding is carried over unmeasured.
func padArg(c db2access.Column, v any) any {
	b, ok := v.([]byte)
	fixed, pad := c.FixedLength()
	if !ok || !fixed || c.Binary() || c.Kind() != db2access.KindBytes || len(b) >= c.Length() {
		return v
	}
	out := append([]byte{}, b...)
	for len(out) < c.Length() {
		out = append(out, pad)
	}
	return out
}

// checkTemporal refuses well-formed text that is no date or time (SQL0181, SQLSTATE 22007:
// 2026-02-30, month 13, year 0). The host also accepts looser forms ('2026-1-5',
// '11/24/2026'); db2access sends strict ISO only, and the fake accepts nothing else.
func checkTemporal(c db2access.Column, t string) error {
	layout := "2006-01-02"
	switch c.Kind() {
	case db2access.KindTime:
		layout = "15.04.05"
	case db2access.KindTimestamp:
		layout, t = "2006-01-02-15.04.05", t[:19]
	}
	if _, err := time.Parse(layout, t); err != nil || strings.HasPrefix(t, "0000") {
		return errDateVal
	}
	return nil
}

// uniqueKeyOf is the comparison key of a stored value: CHAR equality ignores trailing
// blanks (PUB400, section 6: 'u1' and 'u1   ' are the same CHAR(5) value).
func uniqueKeyOf(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimRight(x, " ")
	case []byte:
		return string(x)
	}
	return fmt.Sprint(v)
}

// keyOf is the identity of the row under a unique key, or false when a key column is
// NULL (a NULL makes the row distinct: PUB400 section 6). Each part is length-prefixed,
// so no part can forge the boundary of another.
func keyOf(key []string, r map[string]any) (string, bool) {
	var sb strings.Builder
	for _, k := range key {
		if r[k] == nil {
			return "", false
		}
		part := uniqueKeyOf(r[k])
		sb.WriteString(strconv.Itoa(len(part)))
		sb.WriteByte(':')
		sb.WriteString(part)
	}
	return sb.String(), true
}

// checkUnique is SQLSTATE 23505 / -803 when two rows of the table agree on every column
// of a declared unique key.
func checkUnique(d declTable, rows []map[string]any) error {
	for _, key := range d.unique {
		seen := make(map[string]bool, len(rows))
		for _, r := range rows {
			if id, ok := keyOf(key, r); ok {
				if seen[id] {
					return errUnique
				}
				seen[id] = true
			}
		}
	}
	return nil
}

// checkUniqueNew is checkUnique for one added row: it scans the table once per key and
// copies nothing.
func checkUniqueNew(d declTable, rows []map[string]any, row map[string]any) error {
	for _, key := range d.unique {
		id, ok := keyOf(key, row)
		if !ok {
			continue
		}
		for _, r := range rows {
			if other, ok := keyOf(key, r); ok && other == id {
				return errUnique
			}
		}
	}
	return nil
}
