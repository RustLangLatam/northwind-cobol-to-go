package db2access

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/RustLangLatam/cobolnum-go"
)

// Kind is how a column is read and bound.
type Kind int

const (
	KindText      Kind = iota + 1 // string
	KindBytes                     // []byte: FOR BIT DATA, CCSID 65535, BINARY, VARBINARY
	KindExactNum                  // DECIMAL / NUMERIC, read as cast text, exposed as Exact
	KindInt                       // SMALLINT, INTEGER, BIGINT as int64
	KindDate                      // DATE, read as ISO text, exposed as Date
	KindTime                      // TIME, read as ISO text, exposed as Time
	KindTimestamp                 // TIMESTAMP(p), p 0..12, read as text, exposed as Timestamp
)

// maxDBCLOBChars is the largest DBCLOB length in characters (1 G less one).
const maxDBCLOBChars = 1<<30 - 1

// MaxPrecision is the largest DECIMAL/NUMERIC precision Db2 for i accepts. cobolnum.Dec
// stops at cobolnum.MaxDigits (31); wider columns keep exact text in Exact.Text and
// have no Dec.
const MaxPrecision = 63

// BinaryCCSID is the CCSID that marks data the host must not translate.
const BinaryCCSID = 65535

// Column is one column of a table, built from the schema facts by ParseColumn. Its
// fields are unexported on purpose: the constructor is the only way in, so a column
// that was not validated cannot exist outside this package, and the SQL builders
// validate it again before they emit a name.
type Column struct {
	name      string
	kind      Kind
	precision int // KindExactNum; KindTimestamp: fraction digits
	scale     int // KindExactNum
	length    int
	ccsid     int
	// varying: VARCHAR or VARBINARY (no padding); binary: BINARY or VARBINARY (pads X'00')
	varying, binary bool
	// graphic: GRAPHIC, VARGRAPHIC or DBCLOB with CCSID 1200 or 13488; the fork binds and
	// returns them as UTF-16BE Go strings (PUB400 round 3). The length counts UTF-16 code units.
	graphic, dbclob bool
	// nullability and default, see column_attrs.go; the zero value is the DDS rule for
	// a field without ALWNULL and without DFT: NOT NULL WITH DEFAULT (blanks, zeros, ...)
	nullable bool
	dftSet   bool // an explicit default (WithDefault, WithoutDefault)
	dftRule  DefaultRule
	dft      string
	intBits  int // KindInt: 16, 32 or 64
}

// Name is the upper-case column name.
func (c Column) Name() string { return c.name }

// Kind is how the column is read and bound.
func (c Column) Kind() Kind { return c.kind }

// Precision is the DECIMAL precision, or the fractional-second digits of a TIMESTAMP
// (0 for other kinds).
func (c Column) Precision() int { return c.precision }

// Scale is the DECIMAL scale (0 for other kinds).
func (c Column) Scale() int { return c.scale }

// Graphic reports a GRAPHIC, VARGRAPHIC or DBCLOB column (CCSID 1200 or 13488), whose length
// counts UTF-16 code units.
func (c Column) Graphic() bool { return c.graphic }

// Length is the declared length of a character or binary column.
func (c Column) Length() int { return c.length }

// CCSID is the declared CCSID, 0 when none was written.
func (c Column) CCSID() int { return c.ccsid }

// Identifiers follow the Db2 for i rules: a letter, #, @ or $, then letters, digits,
// underscore, #, @ or $. Nothing else, so a validated name cannot carry SQL.
var identRE = regexp.MustCompile(`^[A-Za-z#@$][A-Za-z0-9_#@$]{0,127}$`)

func validIdent(s string) bool { return identRE.MatchString(s) }

func (c Column) validate() error {
	if !validIdent(c.name) {
		return fmt.Errorf("db2access: invalid column name %q", c.name)
	}
	switch c.kind {
	case KindText, KindBytes:
		// Db2 for i limits: CHAR and BINARY 32766, VARCHAR and VARBINARY 32740.
		maxLen := 32766
		if c.varying {
			maxLen = 32740
		}
		if c.graphic {
			maxLen = 16383
			if c.varying {
				maxLen = 16370
			}
			if c.dbclob {
				maxLen = maxDBCLOBChars
			}
			if c.ccsid != 1200 && c.ccsid != 13488 {
				return fmt.Errorf("db2access: column %s: GRAPHIC, VARGRAPHIC and DBCLOB need an explicit CCSID 1200 or 13488 (without one the host picks a CCSID that depends on the job)", c.name)
			}
		} else if unicodeCCSID(c.ccsid) && c.ccsid != utf8CCSID {
			return fmt.Errorf("db2access: column %s: CCSID 1200 and 13488 are valid only for graphic types", c.name)
		}
		if c.length < 1 || c.length > maxLen {
			return fmt.Errorf("db2access: column %s: length is outside 1..%d", c.name, maxLen)
		}
	case KindInt:
	case KindDate, KindTime:
		if c.precision != 0 || c.scale != 0 {
			return fmt.Errorf("db2access: column %s: DATE and TIME take no precision", c.name)
		}
	case KindTimestamp:
		if c.precision < 0 || c.precision > MaxTimestampPrecision || c.scale != 0 {
			return fmt.Errorf("db2access: column %s: TIMESTAMP(%d) is outside 0..%d", c.name, c.precision, MaxTimestampPrecision)
		}
	case KindExactNum:
		if c.precision < 1 || c.precision > MaxPrecision || c.scale < 0 || c.scale > c.precision {
			return fmt.Errorf("db2access: column %s: DECIMAL(%d,%d) is outside (1..%d, 0..precision)", c.name, c.precision, c.scale, MaxPrecision)
		}
	default:
		return fmt.Errorf("db2access: column %s has no valid kind", c.name)
	}
	return nil
}

var (
	decRE  = regexp.MustCompile(`^(?:DECIMAL|DEC|NUMERIC|NUM)(?:\s*\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\))?$`)
	charRE = regexp.MustCompile(`^(?:CHARACTER VARYING|CHAR VARYING|VARCHAR|CHARACTER|CHAR)(?:\s*\(\s*(\d+)\s*\))?(\s+FOR\s+BIT\s+DATA)?(?:\s+CCSID\s+(\d+))?$`)
	// graphicRE: GRAPHIC(n), VARGRAPHIC(n), DBCLOB(n[K|M|G]), each with an optional CCSID.
	graphicRE = regexp.MustCompile(`^(VARGRAPHIC|GRAPHIC|DBCLOB)\s*\(\s*(\d+)\s*([KMG])?\s*\)(?:\s+CCSID\s+(\d+))?$`)
	binRE     = regexp.MustCompile(`^(?:VARBINARY|BINARY)\s*\(\s*(\d+)\s*\)$`)
	intRE     = regexp.MustCompile(`^(?:SMALLINT|INTEGER|INT|BIGINT)$`)
	// TIMESTAMP WITH TIME ZONE is not a civil value (and not a Db2 for i type): refused.
	tsRE    = regexp.MustCompile(`^TIMESTAMP(?:\s*\(\s*(\d+)\s*\))?(?: WITHOUT TIME ZONE)?$`)
	spaceRE = regexp.MustCompile(`\s+`)
)

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1 // out of range, rejected by validate
	}
	return n
}

// ParseColumn builds a column from the declared type exactly as the schema facts give
// it (Db2Column.declared_type), for example "DECIMAL(12, 2)", "CHAR(4) FOR BIT DATA".
// An omitted DECIMAL argument takes the documented Db2 default (5,0), the case the
// facts flag with TYPE_ARGS_DEFAULTED; an omitted TIMESTAMP precision is 6. DATE,
// TIME and TIMESTAMP(0..12) are read exactly (ADR-0064). A type the layer does not read
// exactly (DOUBLE, REAL, DECFLOAT, CLOB, ...) is an error: mark that access `// PORT-GAP`.
func ParseColumn(name, declaredType string) (Column, error) {
	t := strings.ToUpper(spaceRE.ReplaceAllString(strings.TrimSpace(declaredType), " "))
	c := Column{name: strings.ToUpper(name)}
	switch {
	case decRE.MatchString(t):
		m := decRE.FindStringSubmatch(t)
		c.kind, c.precision, c.scale = KindExactNum, 5, 0
		if m[1] != "" {
			c.precision = atoi(m[1])
		}
		if m[2] != "" {
			c.scale = atoi(m[2])
		}
	case charRE.MatchString(t):
		m := charRE.FindStringSubmatch(t)
		c.length = 1
		if m[1] != "" {
			c.length = atoi(m[1])
		}
		if m[3] != "" {
			c.ccsid = atoi(m[3])
		}
		c.kind = KindText
		if m[2] != "" || c.ccsid == BinaryCCSID {
			c.kind = KindBytes
		}
		c.varying = strings.HasPrefix(t, "VAR") || strings.Contains(t, "VARYING")
	case graphicRE.MatchString(t):
		m := graphicRE.FindStringSubmatch(t)
		c.kind, c.graphic, c.length = KindText, true, atoi(m[2])
		c.varying, c.dbclob = m[1] != "GRAPHIC", m[1] == "DBCLOB"
		if m[3] != "" && !c.dbclob {
			return Column{}, fmt.Errorf("db2access: column %s: a K, M or G length suffix is valid only for DBCLOB", name)
		}
		if mult := map[string]int{"K": 1 << 10, "M": 1 << 20, "G": 1 << 30}[m[3]]; mult > 0 && c.length > 0 {
			if c.length > maxDBCLOBChars/mult { // beyond the cap, and checked before the multiplication can overflow (A56)
				return Column{}, fmt.Errorf("db2access: column %s: DBCLOB is longer than the maximum", name)
			}
			c.length *= mult
		}
		if m[4] != "" {
			c.ccsid = atoi(m[4])
		}
	case binRE.MatchString(t):
		c.kind = KindBytes
		c.length = atoi(binRE.FindStringSubmatch(t)[1])
		c.binary, c.varying = true, strings.HasPrefix(t, "VAR")
	case intRE.MatchString(t):
		c.kind = KindInt
		c.intBits = map[string]int{"SMALLINT": 16, "INTEGER": 32, "INT": 32, "BIGINT": 64}[t]
	case t == "DATE":
		c.kind = KindDate
	case t == "TIME":
		c.kind = KindTime
	case tsRE.MatchString(t):
		c.kind, c.precision = KindTimestamp, DefaultTimestampPrecision
		if m := tsRE.FindStringSubmatch(t); m[1] != "" {
			c.precision = atoi(m[1])
		}
	default:
		return Column{}, fmt.Errorf("db2access: column %s: type %q is not read by this layer", name, declaredType)
	}
	if err := c.validate(); err != nil {
		return Column{}, err
	}
	return c, nil
}

// Table is a table of the existing schema with its known columns, built by NewTable.
// Its fields are unexported for the same reason as Column's.
//
// Unqualified names. When the schema is empty the name is unqualified. The IBM i driver
// connects with SQL naming, so the host resolves it in CURRENT SCHEMA (Config.Library),
// for the SQL and for the journaling check alike; the library list is not used for
// tables. Set the schema whenever the facts give one.
type Table struct {
	schema  string
	name    string
	columns []Column
	unique  [][]string // declared unique keys (WithUniqueKey), column names upper-case
}

// WithUniqueKey returns the table with a unique key (a PRIMARY KEY or UNIQUE constraint
// of the host) over the columns. The host enforces it; a fake reads it to raise
// SQLSTATE 23505 on a duplicate. Declare what the schema facts say, the builders do not
// use it.
func (t Table) WithUniqueKey(cols []string) (Table, error) {
	if len(cols) == 0 {
		return Table{}, errors.New("db2access: a unique key needs at least one column")
	}
	key := make([]string, len(cols))
	seen := map[string]bool{}
	for i, n := range cols {
		c, err := t.column(n)
		if err != nil {
			return Table{}, err
		}
		if seen[c.name] {
			return Table{}, fmt.Errorf("db2access: unique key names column %s twice", c.name)
		}
		seen[c.name] = true
		key[i] = c.name
	}
	t.unique = append(append([][]string(nil), t.unique...), key)
	return t, nil
}

// UniqueKeys returns a copy of the declared unique keys.
func (t Table) UniqueKeys() [][]string {
	out := make([][]string, len(t.unique))
	for i, k := range t.unique {
		out[i] = append([]string(nil), k...)
	}
	return out
}

// NewTable validates the names and returns the table; names are upper-cased, column
// names are unique.
func NewTable(schema, name string, cols ...Column) (Table, error) {
	t := Table{schema: strings.ToUpper(schema), name: strings.ToUpper(name)}
	for _, c := range cols {
		c.name = strings.ToUpper(c.name)
		t.columns = append(t.columns, c)
	}
	if err := t.validate(); err != nil {
		return Table{}, err
	}
	return t, nil
}

// validate is run by the constructor and again by every builder.
func (t Table) validate() error {
	if !validIdent(t.name) || (t.schema != "" && !validIdent(t.schema)) {
		return fmt.Errorf("db2access: invalid table name %q.%q", t.schema, t.name)
	}
	if len(t.columns) == 0 {
		return fmt.Errorf("db2access: table %s has no columns", t.name)
	}
	seen := map[string]bool{}
	for _, c := range t.columns {
		if err := c.validate(); err != nil {
			return fmt.Errorf("table %s: %w", t.name, err)
		}
		u := strings.ToUpper(c.name)
		if seen[u] {
			return fmt.Errorf("db2access: table %s: duplicate column %q", t.name, c.name)
		}
		seen[u] = true
	}
	return nil
}

// Schema is the upper-case schema, "" when none.
func (t Table) Schema() string { return t.schema }

// Name is the upper-case table name.
func (t Table) Name() string { return t.name }

// Columns returns a copy of the columns.
func (t Table) Columns() []Column { return append([]Column(nil), t.columns...) }

// Qualified is SCHEMA.NAME, or NAME with no schema (for messages and the driver port).
func (t Table) Qualified() string {
	if t.schema == "" {
		return t.name
	}
	return t.schema + "." + t.name
}

func (t Table) column(name string) (Column, error) {
	n := strings.ToUpper(name)
	for _, c := range t.columns {
		if c.name == n {
			return c, nil
		}
	}
	return Column{}, fmt.Errorf("db2access: table %s has no column %q", t.Qualified(), name)
}

// Exact is a DECIMAL/NUMERIC value. Text is the canonical plain text at the column's
// scale ("-0.50", never an exponent). Dec is set when the precision fits cobolnum
// (up to 31 digits); wider values (up to 63) are exact text only.
type Exact struct {
	Text   string
	Dec    cobolnum.Dec
	HasDec bool
}

func (e Exact) String() string { return e.Text }

var errNoDigits = errors.New("no digits")

// canonExact validates plain decimal text against the column and returns it at the
// column's scale. It rejects what a store would have to truncate: more fraction digits
// than the scale (unless all zero) or more integer digits than precision-scale. A
// negative zero becomes plain zero. Its errors never contain the value.
func canonExact(c Column, s string) (string, error) {
	t := strings.TrimSpace(s)
	neg := false
	if t != "" && (t[0] == '+' || t[0] == '-') {
		neg = t[0] == '-'
		t = t[1:]
	}
	ip, fp, _ := strings.Cut(t, ".")
	if ip == "" && fp == "" {
		return "", errNoDigits
	}
	for _, r := range ip + fp {
		if r < '0' || r > '9' {
			return "", errors.New("not plain decimal text")
		}
	}
	ip = strings.TrimLeft(ip, "0")
	if len(fp) > c.scale {
		if strings.Trim(fp[c.scale:], "0") != "" {
			return "", fmt.Errorf("more than %d fraction digits", c.scale)
		}
		fp = fp[:c.scale]
	}
	if len(ip) > c.precision-c.scale {
		return "", fmt.Errorf("more than %d integer digits", c.precision-c.scale)
	}
	fp += strings.Repeat("0", c.scale-len(fp))
	if ip == "" {
		ip = "0"
	}
	out := ip
	if c.scale > 0 {
		out += "." + fp
	}
	if neg && strings.Trim(ip+fp, "0") != "" {
		out = "-" + out
	}
	return out, nil
}

// newExact parses text for a column with cobolnum. Its errors name the column, its
// precision and scale, and the reason; never the value.
func newExact(c Column, s string) (Exact, error) {
	canon, err := canonExact(c, s)
	if err != nil {
		return Exact{}, fmt.Errorf("db2access: column %s DECIMAL(%d,%d): the value is not exact decimal text that fits: %w", c.name, c.precision, c.scale, err)
	}
	if _, err := cobolnum.ParseNum(canon); err != nil {
		return Exact{}, fmt.Errorf("db2access: column %s DECIMAL(%d,%d): the value is not valid decimal text", c.name, c.precision, c.scale)
	}
	if c.precision > cobolnum.MaxDigits {
		return Exact{Text: canon}, nil
	}
	pic := cobolnum.Pic{Digits: c.precision, Scale: c.scale, Signed: true}
	d, sizeErr, err := pic.ParseDec(canon, cobolnum.Prohibited)
	if err != nil || sizeErr {
		return Exact{}, fmt.Errorf("db2access: column %s DECIMAL(%d,%d): the value does not fit", c.name, c.precision, c.scale)
	}
	return Exact{Text: d.Plain(), Dec: d, HasDec: true}, nil
}
