package db2access

import (
	"errors"
	"fmt"
	"strings"

	"github.com/RustLangLatam/cobolnum-go"
)

// Stmt is a built statement. Its SQL is unexported: the only way to obtain one is a
// builder below, so no caller-written SQL (and no DDL) can reach the database.
type Stmt struct {
	sql   string
	args  []any
	table Table
	cols  []Column // result columns of a SELECT
	// jobText are the text values bound to columns with no CCSID clause: the builders
	// cannot check them (the job CCSID is a Store setting), the Store does before sending.
	jobText []jobText
}

type jobText struct {
	col   Column
	value string
}

// noteJobText records a text value bound to a column that declares no CCSID.
func (s *Stmt) noteJobText(c Column, v any) {
	if str, ok := v.(string); ok && c.kind == KindText && c.ccsid == 0 {
		s.jobText = append(s.jobText, jobText{c, str})
	}
}

// checkJobText applies the text guard with the job CCSID (Config.CCSID) to the values
// the builders left unchecked. A guard failure is returned before anything is sent.
func (s Stmt) checkJobText(jobCCSID int) error {
	for _, jt := range s.jobText {
		if err := jt.col.CheckTextFor(jobCCSID, jt.value); err != nil {
			return err
		}
	}
	return nil
}

// SQL is the statement text, for logging and tests.
func (s Stmt) SQL() string { return s.sql }

// Args are the bound parameters: exact decimal text, IBM ISO temporal text, strings,
// []byte, int64 or nil.
// The slice and every []byte in it are copies.
func (s Stmt) Args() []any {
	out := make([]any, len(s.args))
	for i, a := range s.args {
		if b, ok := a.([]byte); ok {
			a = append([]byte{}, b...)
		}
		out[i] = a
	}
	return out
}

// Op is a comparison in a WHERE clause.
type Op string

const (
	Eq      Op = "="
	Ne      Op = "<>"
	Lt      Op = "<"
	Le      Op = "<="
	Gt      Op = ">"
	Ge      Op = ">="
	IsNull  Op = "IS NULL"
	NotNull Op = "IS NOT NULL"
)

// Cond is one predicate; conditions are joined with AND.
type Cond struct {
	Column string
	Op     Op
	Value  any // unused for IsNull and NotNull
}

// Assign sets one column to a value.
type Assign struct {
	Column string
	Value  any // nil writes NULL
}

// ErrNoWhere is returned for an UPDATE or DELETE whose WHERE has no comparison with a
// bound value: IS NULL and IS NOT NULL alone are not a filter.
var ErrNoWhere = errors.New("db2access: UPDATE and DELETE require at least one WHERE comparison with a value")

// sqlIdent is a validated, upper-case identifier as delimited SQL text. EVERY identifier
// (column, table, schema) is delimited, never only the reserved words: on Db2 for i 7.5 a
// bare SYSTEM_USER, CURRENT_SCHEMA, CURRENT_SERVER, CURRENT_PATH, CURRENT_TIMEZONE,
// LOCALDATE, LOCALTIME or LOCALTIMESTAMP silently returns the special register's value
// instead of the column, and ROW is a syntax error; no word list can be trusted to be
// complete. Delimiting is safe because names come from validIdent, which cannot carry a
// quote, and it is correct because delimited names are case-sensitive and the schema
// facts store them in the canonical upper case the catalogue uses.
func sqlIdent(n string) string { return `"` + n + `"` }

func (t Table) sqlName() string {
	if t.schema == "" {
		return sqlIdent(t.name)
	}
	return sqlIdent(t.schema) + "." + sqlIdent(t.name)
}

// bind converts a value to what the driver receives for the column: exact decimals as
// canonical text, DATE/TIME/TIMESTAMP as the host's ISO text, text as string, bytes as
// a copy of the []byte, integers as int64.
// Anything else, binary or decimal floating point included, is refused. Errors never
// contain the value.
func bind(c Column, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch c.kind {
	case KindExactNum:
		var text string
		switch x := v.(type) {
		case Exact:
			text = x.Text
		case cobolnum.Dec:
			text = x.Plain()
		case string:
			text = x
		default:
			return nil, fmt.Errorf("db2access: column %s is DECIMAL(%d,%d): bind an Exact, a cobolnum.Dec or exact decimal text, not %T", c.name, c.precision, c.scale, v)
		}
		e, err := newExact(c, text)
		if err != nil {
			return nil, err
		}
		return e.Text, nil
	case KindDate, KindTime, KindTimestamp:
		return hostTemporal(c, v)
	case KindText:
		if s, ok := v.(string); ok {
			if c.ccsid == 0 { // the job CCSID decides, and only the Store knows it
				return s, c.checkUTF8(s)
			}
			return s, c.CheckText(s)
		}
	case KindBytes:
		if b, ok := v.([]byte); ok {
			// The i400 fork v0.1.2 cuts a BINARY(n) value to n bytes with no error (and
			// matches a longer WHERE key against the cut value); refuse any excess here.
			if len(b) > c.length {
				return nil, &Error{Op: "bind", SQLState: SQLStateBinaryTooLong,
					Message: "column " + c.name + ": the value is longer than the column (platform guard, not a host SQLSTATE); the driver would cut it without an error"}
			}
			return append([]byte{}, b...), nil
		}
	case KindInt:
		switch x := v.(type) {
		case int:
			return int64(x), nil
		case int32:
			return int64(x), nil
		case int64:
			return x, nil
		}
	}
	return nil, fmt.Errorf("db2access: column %s: cannot bind a %T", c.name, v)
}

// marker is the parameter marker for a column: decimals are cast so the host parses
// the exact text itself. A temporal marker is typed VARCHAR, so the driver sends the
// ISO text untouched (no date-parameter formatting, no truncation to the column) and the
// host converts it: ISO text is accepted whatever the job's date format.
func marker(c Column) string {
	switch c.kind {
	case KindExactNum:
		return fmt.Sprintf("CAST(? AS DECIMAL(%d,%d))", c.precision, c.scale)
	case KindDate:
		return "CAST(CAST(? AS VARCHAR(10)) AS DATE)"
	case KindTime:
		return "CAST(CAST(? AS VARCHAR(8)) AS TIME)"
	case KindTimestamp:
		return fmt.Sprintf("CAST(CAST(? AS VARCHAR(%d)) AS TIMESTAMP(%d))", timestampWidth(c.precision), c.precision)
	}
	return "?"
}

// selectItem casts every exact numeric column to text, whatever the driver would
// otherwise map it to. The CAST is NOT aliased to the column name: an alias would make
// ORDER BY <column> resolve to the text and sort "10.00" before "9.00". Results are
// read by position. p+3 holds sign, point and the leading zero of -0.50 in
// DECIMAL(2,2).
//
// DATE and TIME are read with CHAR(col, ISO): the format is named in the statement, so
// neither the job's date format (*MDY, *DMY, *YMD, *JUL) nor its separators apply, nor
// the driver's date mapping. A TIMESTAMP's character form has one format on Db2 for i
// (YYYY-MM-DD-HH.MM.SS.f), so it is a CAST to VARCHAR of exactly its width.
func selectItem(c Column) string {
	switch c.kind {
	case KindExactNum:
		return fmt.Sprintf("CAST(%s AS VARCHAR(%d))", sqlIdent(c.name), c.precision+3)
	case KindDate, KindTime:
		return fmt.Sprintf("CHAR(%s, ISO)", sqlIdent(c.name))
	case KindTimestamp:
		return fmt.Sprintf("CAST(%s AS VARCHAR(%d))", sqlIdent(c.name), timestampWidth(c.precision))
	}
	return sqlIdent(c.name)
}

func hasValuePredicate(conds []Cond) bool {
	for _, c := range conds {
		switch c.Op {
		case Eq, Ne, Lt, Le, Gt, Ge:
			return true
		}
	}
	return false
}

func (t Table) where(conds []Cond, st *Stmt) (string, error) {
	parts := make([]string, 0, len(conds))
	for _, cd := range conds {
		c, err := t.column(cd.Column)
		if err != nil {
			return "", err
		}
		switch cd.Op {
		case IsNull, NotNull:
			parts = append(parts, sqlIdent(c.name)+" "+string(cd.Op))
		case Eq, Ne, Lt, Le, Gt, Ge:
			if cd.Value == nil {
				return "", fmt.Errorf("db2access: column %s: comparing with nil never matches; use IsNull", c.name)
			}
			v, err := bind(c, cd.Value)
			if err != nil {
				return "", err
			}
			st.args = append(st.args, v)
			st.noteJobText(c, v)
			parts = append(parts, sqlIdent(c.name)+" "+string(cd.Op)+" "+marker(c))
		default:
			return "", fmt.Errorf("db2access: unknown operator %q", cd.Op)
		}
	}
	return " WHERE " + strings.Join(parts, " AND "), nil
}

// Select describes a SELECT on one table.
//
// Limit and the store's row cap: with Limit 0 the read is capped at Config.MaxRows
// (DB2_MAX_ROWS) and one row more is ErrTooManyRows. A Limit > 0 is FETCH FIRST n ROWS
// ONLY and replaces the cap for that query, above or below it. A read whose legitimate
// row count is bounded by its key must set Limit from the key's DDS range, so that an
// operator's lower DB2_MAX_ROWS cannot break it: the lines of one order keyed by
// ODLIN# 3P 0 and read with ODLIN# >= 0 are at most 1000 rows, so Limit: 1000.
type Select struct {
	Table   Table
	Columns []string // nil = every declared column; no column twice
	Where   []Cond
	// OrderBy lists the sort keys ({Column: "ODLIN#"}, {Column: "ODLIN#", Desc: true}).
	// NULL sorts high, as on Db2: last ascending, first descending.
	OrderBy []Order
	// Limit: 0 = the store's row cap; 1..HardMaxRows = FETCH FIRST n ROWS ONLY; anything
	// else is an error (the rows are held in memory).
	Limit int
	// capProbe marks the store's own "one row more than the cap" Limit (cap + 1).
	capProbe bool
}

// Order is one ORDER BY key: a declared column and a direction (ascending unless Desc).
type Order struct {
	Column string
	Desc   bool
}

func (t Table) orderKey(o Order) (string, error) {
	c, err := t.column(o.Column)
	if err != nil {
		return "", err
	}
	if o.Desc {
		return sqlIdent(c.name) + " DESC", nil
	}
	return sqlIdent(c.name), nil
}

// Build returns the statement.
func (s Select) Build() (Stmt, error) {
	if err := s.Table.validate(); err != nil {
		return Stmt{}, err
	}
	if s.Limit < 0 {
		return Stmt{}, errors.New("db2access: Select.Limit must not be negative")
	}
	if s.Limit > HardMaxRows && !(s.capProbe && s.Limit == HardMaxRows+1) {
		return Stmt{}, fmt.Errorf("db2access: Select.Limit %d is above the hard cap of %d rows", s.Limit, HardMaxRows)
	}
	cols := s.Table.columns
	if len(s.Columns) > 0 {
		cols = nil
		seen := map[string]bool{}
		for _, n := range s.Columns {
			c, err := s.Table.column(n)
			if err != nil {
				return Stmt{}, err
			}
			if seen[c.name] {
				return Stmt{}, fmt.Errorf("db2access: column %s is selected twice", c.name)
			}
			seen[c.name] = true
			cols = append(cols, c)
		}
	}
	items := make([]string, len(cols))
	for i, c := range cols {
		items[i] = selectItem(c)
	}
	st := Stmt{table: s.Table, cols: cols}
	sb := "SELECT " + strings.Join(items, ", ") + " FROM " + s.Table.sqlName()
	if len(s.Where) > 0 {
		w, err := s.Table.where(s.Where, &st)
		if err != nil {
			return Stmt{}, err
		}
		sb += w
	}
	if len(s.OrderBy) > 0 {
		names := make([]string, len(s.OrderBy))
		for i, n := range s.OrderBy {
			key, err := s.Table.orderKey(n)
			if err != nil {
				return Stmt{}, err
			}
			names[i] = key
		}
		sb += " ORDER BY " + strings.Join(names, ", ")
	}
	if s.Limit > 0 {
		sb += fmt.Sprintf(" FETCH FIRST %d ROWS ONLY", s.Limit)
	}
	st.sql = sb
	return st, nil
}

// Insert builds an INSERT of the given columns.
func (t Table) Insert(set []Assign) (Stmt, error) {
	if err := t.validate(); err != nil {
		return Stmt{}, err
	}
	if len(set) == 0 {
		return Stmt{}, errors.New("db2access: INSERT with no columns")
	}
	st := Stmt{table: t}
	names, marks := make([]string, len(set)), make([]string, len(set))
	for i, a := range set {
		c, err := t.column(a.Column)
		if err != nil {
			return Stmt{}, err
		}
		v, err := bind(c, a.Value)
		if err != nil {
			return Stmt{}, err
		}
		st.args = append(st.args, v)
		st.noteJobText(c, v)
		names[i], marks[i] = sqlIdent(c.name), marker(c)
	}
	st.sql = "INSERT INTO " + t.sqlName() + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
	return st, nil
}

// Update builds an UPDATE; a WHERE with at least one value comparison is required.
func (t Table) Update(set []Assign, where []Cond) (Stmt, error) {
	if err := t.validate(); err != nil {
		return Stmt{}, err
	}
	if len(set) == 0 {
		return Stmt{}, errors.New("db2access: UPDATE with no columns")
	}
	if !hasValuePredicate(where) {
		return Stmt{}, ErrNoWhere
	}
	st := Stmt{table: t}
	parts := make([]string, len(set))
	for i, a := range set {
		c, err := t.column(a.Column)
		if err != nil {
			return Stmt{}, err
		}
		v, err := bind(c, a.Value)
		if err != nil {
			return Stmt{}, err
		}
		st.args = append(st.args, v)
		st.noteJobText(c, v)
		parts[i] = sqlIdent(c.name) + " = " + marker(c)
	}
	w, err := t.where(where, &st)
	if err != nil {
		return Stmt{}, err
	}
	st.sql = "UPDATE " + t.sqlName() + " SET " + strings.Join(parts, ", ") + w
	return st, nil
}

// Delete builds a DELETE; a WHERE with at least one value comparison is required.
func (t Table) Delete(where []Cond) (Stmt, error) {
	if err := t.validate(); err != nil {
		return Stmt{}, err
	}
	if !hasValuePredicate(where) {
		return Stmt{}, ErrNoWhere
	}
	st := Stmt{table: t}
	w, err := t.where(where, &st)
	if err != nil {
		return Stmt{}, err
	}
	st.sql = "DELETE FROM " + t.sqlName() + w
	return st, nil
}
