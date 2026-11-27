package db2fake

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/RustLangLatam/cobolnum-go/ebcdic"

	"milton_prism/pkg/db2access"
)

// The interpreter accepts exactly the grammar pkg/db2access builds and nothing else:
//
//	SELECT item {, item} FROM table [WHERE cond {AND cond}]
//	       [ORDER BY "C" [ASC|DESC] {, ...}] [FETCH FIRST n ROWS ONLY]
//	INSERT INTO table ("C" {, "C"}) VALUES (marker {, marker})
//	UPDATE table SET "C" = marker {, "C" = marker} WHERE cond {AND cond}
//	DELETE FROM table WHERE cond {AND cond}
//
//	item   = "C" | CAST("C" AS VARCHAR(n)) | CHAR("C", ISO)
//	table  = "T" | "S"."T"
//	cond   = "C" (= | <> | < | <= | > | >=) marker | "C" IS [NOT] NULL
//	marker = ? | CAST(? AS DECIMAL(p,s)) | CAST(CAST(? AS VARCHAR(n)) AS DATE|TIME|TIMESTAMP(p))
//
// Every identifier is delimited, as the builders emit it. Any other token, clause or
// statement is an error naming the fake, so a test never passes on SQL the fake did
// not actually evaluate.

type token struct {
	kind byte // 'i' delimited identifier, 'w' word, 'n' unsigned integer, 'p' punctuation
	s    string
}

func lex(q string) ([]token, error) {
	var out []token
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case c == '"':
			j := strings.IndexByte(q[i+1:], '"')
			if j <= 0 {
				return nil, fmt.Errorf("db2fake: unterminated or empty delimited identifier in %q", q)
			}
			out = append(out, token{'i', q[i+1 : i+1+j]})
			i += j + 2
			if i < len(q) && q[i] == '"' {
				return nil, fmt.Errorf("db2fake: escaped quote in an identifier is not something pkg/db2access builds: %q", q)
			}
		case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_':
			j := i
			for j < len(q) && (q[j] >= 'A' && q[j] <= 'Z' || q[j] >= 'a' && q[j] <= 'z' || q[j] == '_' || q[j] >= '0' && q[j] <= '9') {
				j++
			}
			out = append(out, token{'w', strings.ToUpper(q[i:j])})
			i = j
		case c >= '0' && c <= '9':
			j := i
			for j < len(q) && q[j] >= '0' && q[j] <= '9' {
				j++
			}
			out = append(out, token{'n', q[i:j]})
			i = j
		case strings.HasPrefix(q[i:], "<>"), strings.HasPrefix(q[i:], "<="), strings.HasPrefix(q[i:], ">="):
			out = append(out, token{'p', q[i : i+2]})
			i += 2
		case strings.IndexByte("(),.?=<>", c) >= 0:
			out = append(out, token{'p', string(c)})
			i++
		default:
			return nil, fmt.Errorf("db2fake: unexpected character %q in %q", c, q)
		}
	}
	return out, nil
}

type markerKind int

const (
	plainMark markerKind = iota
	decMark
	tempMark
)

type cond struct {
	col  string
	op   string // = <> < <= > >= IS NULL, IS NOT NULL
	mark markerKind
	arg  int
}

type item struct {
	col  string
	cast int  // > 0: CAST("C" AS VARCHAR(cast))
	iso  bool // CHAR("C", ISO)
}

type orderKey struct {
	col  string
	desc bool
}

type assign struct {
	col  string
	mark markerKind
	arg  int
}

type stmt struct {
	verb     string // SELECT, INSERT, UPDATE, DELETE
	table    string
	items    []item
	where    []cond
	order    []orderKey
	limit    int // FETCH FIRST n; valid when hasLimit
	hasLimit bool
	assign   []assign
}

type parser struct {
	q     string
	toks  []token
	i     int
	marks int
}

func (p *parser) errf(format string, a ...any) error {
	return fmt.Errorf("db2fake: "+format+" in %q (the fake evaluates only the SQL pkg/db2access builds)", append(a, p.q)...)
}

func (p *parser) peek() token {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return token{}
}

// accept consumes the next token when it is the word or punctuation s.
func (p *parser) accept(s string) bool {
	if t := p.peek(); (t.kind == 'w' || t.kind == 'p') && t.s == s {
		p.i++
		return true
	}
	return false
}

func (p *parser) expect(seq ...string) error {
	for _, s := range seq {
		if !p.accept(s) {
			return p.errf("expected %s, found %q", s, p.peek().s)
		}
	}
	return nil
}

func (p *parser) ident() (string, error) {
	if t := p.peek(); t.kind == 'i' {
		p.i++
		return t.s, nil
	}
	return "", p.errf("expected a delimited identifier, found %q", p.peek().s)
}

func (p *parser) num() (int, error) {
	if t := p.peek(); t.kind == 'n' {
		p.i++
		return strconv.Atoi(t.s)
	}
	return 0, p.errf("expected a number, found %q", p.peek().s)
}

func (p *parser) table() (string, error) {
	n, err := p.ident()
	if err != nil {
		return "", err
	}
	if p.accept(".") {
		m, err := p.ident()
		if err != nil {
			return "", err
		}
		return n + "." + m, nil
	}
	return n, nil
}

// marker parses a parameter marker and returns its kind and argument index.
func (p *parser) marker() (markerKind, int, error) {
	next := func() int { p.marks++; return p.marks - 1 }
	if p.accept("?") {
		return plainMark, next(), nil
	}
	if err := p.expect("CAST", "("); err != nil {
		return 0, 0, err
	}
	if p.accept("?") { // CAST(? AS DECIMAL(p,s))
		if err := p.expect("AS", "DECIMAL", "("); err != nil {
			return 0, 0, err
		}
		if _, err := p.num(); err != nil {
			return 0, 0, err
		}
		if err := p.expect(","); err != nil {
			return 0, 0, err
		}
		if _, err := p.num(); err != nil {
			return 0, 0, err
		}
		return decMark, next(), p.expect(")", ")")
	}
	// CAST(CAST(? AS VARCHAR(n)) AS DATE | TIME | TIMESTAMP(p))
	if err := p.expect("CAST", "(", "?", "AS", "VARCHAR", "("); err != nil {
		return 0, 0, err
	}
	if _, err := p.num(); err != nil {
		return 0, 0, err
	}
	if err := p.expect(")", ")", "AS"); err != nil {
		return 0, 0, err
	}
	switch {
	case p.accept("DATE"), p.accept("TIME"):
	case p.accept("TIMESTAMP"):
		if err := p.expect("("); err != nil {
			return 0, 0, err
		}
		if _, err := p.num(); err != nil {
			return 0, 0, err
		}
		if err := p.expect(")"); err != nil {
			return 0, 0, err
		}
	default:
		return 0, 0, p.errf("expected DATE, TIME or TIMESTAMP, found %q", p.peek().s)
	}
	return tempMark, next(), p.expect(")")
}

func (p *parser) where() ([]cond, error) {
	var out []cond
	for {
		col, err := p.ident()
		if err != nil {
			return nil, err
		}
		if p.accept("IS") {
			op := "IS NULL"
			if p.accept("NOT") {
				op = "IS NOT NULL"
			}
			if err := p.expect("NULL"); err != nil {
				return nil, err
			}
			out = append(out, cond{col: col, op: op})
		} else {
			t := p.peek()
			switch t.s {
			case "=", "<>", "<", "<=", ">", ">=":
				if t.kind != 'p' {
					return nil, p.errf("unsupported predicate %q", t.s)
				}
			default:
				return nil, p.errf("unsupported predicate %q", t.s)
			}
			p.i++
			k, a, err := p.marker()
			if err != nil {
				return nil, err
			}
			out = append(out, cond{col: col, op: t.s, mark: k, arg: a})
		}
		if !p.accept("AND") {
			return out, nil
		}
	}
}

func (p *parser) selectItem() (item, error) {
	if p.accept("CAST") {
		if err := p.expect("("); err != nil {
			return item{}, err
		}
		col, err := p.ident()
		if err != nil {
			return item{}, err
		}
		if err := p.expect("AS", "VARCHAR", "("); err != nil {
			return item{}, err
		}
		n, err := p.num()
		if err != nil {
			return item{}, err
		}
		return item{col: col, cast: n}, p.expect(")", ")")
	}
	if p.accept("CHAR") {
		if err := p.expect("("); err != nil {
			return item{}, err
		}
		col, err := p.ident()
		if err != nil {
			return item{}, err
		}
		return item{col: col, iso: true}, p.expect(",", "ISO", ")")
	}
	col, err := p.ident()
	return item{col: col}, err
}

func (p *parser) parseSelect(s *stmt) error {
	for {
		it, err := p.selectItem()
		if err != nil {
			return err
		}
		s.items = append(s.items, it)
		if !p.accept(",") {
			break
		}
	}
	var err error
	if err = p.expect("FROM"); err != nil {
		return err
	}
	if s.table, err = p.table(); err != nil {
		return err
	}
	if p.accept("WHERE") {
		if s.where, err = p.where(); err != nil {
			return err
		}
	}
	if p.accept("ORDER") {
		if err := p.expect("BY"); err != nil {
			return err
		}
		for {
			col, err := p.ident()
			if err != nil {
				return err
			}
			k := orderKey{col: col}
			if p.accept("DESC") {
				k.desc = true
			} else {
				p.accept("ASC")
			}
			s.order = append(s.order, k)
			if !p.accept(",") {
				break
			}
		}
	}
	if p.accept("FETCH") {
		if err := p.expect("FIRST"); err != nil {
			return err
		}
		if s.limit, err = p.num(); err != nil {
			return err
		}
		s.hasLimit = true // FETCH FIRST 0 ROWS ONLY is valid: no rows, no error (PUB400 round 1, section 4)
		if err := p.expect("ROWS", "ONLY"); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) parseInsert(s *stmt) error {
	var err error
	if err = p.expect("INTO"); err != nil {
		return err
	}
	if s.table, err = p.table(); err != nil {
		return err
	}
	if err = p.expect("("); err != nil {
		return err
	}
	for {
		col, err := p.ident()
		if err != nil {
			return err
		}
		s.assign = append(s.assign, assign{col: col})
		if !p.accept(",") {
			break
		}
	}
	if err = p.expect(")", "VALUES", "("); err != nil {
		return err
	}
	for i := range s.assign {
		if i > 0 {
			if err := p.expect(","); err != nil {
				return err
			}
		}
		if s.assign[i].mark, s.assign[i].arg, err = p.marker(); err != nil {
			return err
		}
	}
	return p.expect(")")
}

func (p *parser) parseUpdate(s *stmt) error {
	var err error
	if s.table, err = p.table(); err != nil {
		return err
	}
	if err = p.expect("SET"); err != nil {
		return err
	}
	for {
		col, err := p.ident()
		if err != nil {
			return err
		}
		if err := p.expect("="); err != nil {
			return err
		}
		k, a, err := p.marker()
		if err != nil {
			return err
		}
		s.assign = append(s.assign, assign{col: col, mark: k, arg: a})
		if !p.accept(",") {
			break
		}
	}
	if err = p.expect("WHERE"); err != nil {
		return err
	}
	s.where, err = p.where()
	return err
}

func (p *parser) parseDelete(s *stmt) error {
	var err error
	if err = p.expect("FROM"); err != nil {
		return err
	}
	if s.table, err = p.table(); err != nil {
		return err
	}
	if err = p.expect("WHERE"); err != nil {
		return err
	}
	s.where, err = p.where()
	return err
}

// parse reads one statement and checks it binds exactly nargs parameters.
func parse(q string, nargs int) (stmt, error) {
	toks, err := lex(q)
	if err != nil {
		return stmt{}, err
	}
	p := &parser{q: q, toks: toks}
	var s stmt
	switch {
	case p.accept("SELECT"):
		s.verb, err = "SELECT", p.parseSelect(&s)
	case p.accept("INSERT"):
		s.verb, err = "INSERT", p.parseInsert(&s)
	case p.accept("UPDATE"):
		s.verb, err = "UPDATE", p.parseUpdate(&s)
	case p.accept("DELETE"):
		s.verb, err = "DELETE", p.parseDelete(&s)
	default:
		return stmt{}, p.errf("unsupported statement")
	}
	if err != nil {
		return stmt{}, err
	}
	if p.i != len(p.toks) {
		return stmt{}, p.errf("unsupported text from %q on", p.peek().s)
	}
	if p.marks != nargs {
		return stmt{}, p.errf("%d parameter markers but %d arguments", p.marks, nargs)
	}
	seen := map[string]bool{}
	for _, a := range s.assign {
		if seen[a.col] {
			return stmt{}, p.errf("column %s assigned twice", a.col)
		}
		seen[a.col] = true
	}
	return s, nil
}

// bindArgs converts every bound argument once, by the kind of its marker, so a mistyped
// argument fails the statement even when no row reaches its condition.
func bindArgs(s stmt, args []driver.NamedValue) ([]any, error) {
	vals := make([]any, len(args))
	conv := func(k markerKind, i int) (err error) {
		vals[i], err = cell(k, args[i].Value)
		return err
	}
	for _, c := range s.where {
		if c.op != "IS NULL" && c.op != "IS NOT NULL" {
			if err := conv(c.mark, c.arg); err != nil {
				return nil, err
			}
		}
	}
	for _, a := range s.assign {
		if err := conv(a.mark, a.arg); err != nil {
			return nil, err
		}
	}
	return vals, nil
}

// decText is the exact decimal text the builders bind: an optional sign, digits and an
// optional fraction. No exponent, no fraction bar, no hexadecimal (big.Rat takes all three).
var decText = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)$`)

// cell converts a bound argument to what the fake stores for the marker's kind.
func cell(k markerKind, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	s, isText := v.(string)
	switch k {
	case decMark:
		if !isText {
			return nil, fmt.Errorf("db2fake: a DECIMAL marker received a %T, not exact decimal text", v)
		}
		if len(s) > 80 || !decText.MatchString(s) {
			return nil, fmt.Errorf("db2fake: a DECIMAL marker received text that is not an exact decimal number")
		}
		return decCell(s), nil
	case tempMark:
		if !isText {
			return nil, fmt.Errorf("db2fake: a DATE, TIME or TIMESTAMP marker received a %T, not ISO text", v)
		}
		return tempCell(s), nil
	}
	if b, ok := v.([]byte); ok {
		return append([]byte{}, b...), nil
	}
	return v, nil
}

// textBytes is s as the host compares it under the *HEX sort sequence (PUB400 round 1,
// section 1): the bytes of the column's CCSID. A known EBCDIC CCSID orders blank < lower
// case < upper case < digits; CCSID 1208 is UTF-8 byte order. A column with no CCSID takes
// the job's (round 2, A29); with the job CCSID unknown it is ordered as CCSID 37, which
// agrees with every EBCDIC CCSID on blank, letters and digits (the guard keeps such a
// column to 7-bit ASCII). Any other CCSID falls back to UTF-8 bytes. The sort sequence is
// always *HEX: the driver cannot set another (round 2: *LANGIDUNQ and *LANGIDSHR exist
// only inside a routine compiled with them), so the fake has no other.
func textBytes(c db2access.Column, job int, s string) []byte {
	if c.Graphic() { // UTF-16BE: byte order is code unit order, not code point order (round 2, A27a)
		u := utf16.Encode([]rune(s))
		b := make([]byte, 0, 2*len(u))
		for _, x := range u {
			b = append(b, byte(x>>8), byte(x))
		}
		return b
	}
	id := effectiveCCSID(c, job)
	if id == 0 {
		id = 37
	}
	if cp, ok := ebcdic.Lookup(id); ok && !cp.PassThrough() {
		if b, err := cp.Strict().Encode(s); err == nil {
			return b
		}
	}
	return []byte(s)
}

// padCompare compares text the way Db2 does for CHAR and VARCHAR alike: the shorter
// operand is padded with blanks (the blank of the CCSID), so trailing blanks never decide
// equality.
func padCompare(c db2access.Column, job int, a, b string) int {
	ba, bb := textBytes(c, job, a), textBytes(c, job, b)
	blank := textBytes(c, job, " ")
	for len(ba) < len(bb) {
		ba = append(ba, blank...)
	}
	for len(bb) < len(ba) {
		bb = append(bb, blank...)
	}
	return bytes.Compare(ba, bb)
}

// compare orders two non-NULL stored values of one column, or reports that they are
// not comparable (a column holding mixed kinds, or a DECIMAL against plain text).
func compare(c db2access.Column, job int, a, b any) (int, error) {
	col := c.Name()
	switch x := a.(type) {
	case decCell:
		if y, ok := b.(decCell); ok {
			ra, _ := new(big.Rat).SetString(string(x))
			rb, _ := new(big.Rat).SetString(string(y))
			return ra.Cmp(rb), nil
		}
	case tempCell:
		if y, ok := b.(tempCell); ok {
			return strings.Compare(string(x), string(y)), nil
		}
	case string:
		if y, ok := b.(string); ok {
			return padCompare(c, job, x, y), nil
		}
	case int64:
		if y, ok := b.(int64); ok {
			switch {
			case x < y:
				return -1, nil
			case x > y:
				return 1, nil
			}
			return 0, nil
		}
	case []byte:
		if y, ok := b.([]byte); ok {
			return bytes.Compare(x, y), nil
		}
	}
	return 0, fmt.Errorf("db2fake: column %s: cannot compare a %T with a %T (a DECIMAL or temporal column is compared only through its CAST marker)", col, a, b)
}

// orderCompare is compare for ORDER BY. Text equal only through blank padding is NOT a tie
// there (PUB400, section 3): the value with more trailing blanks sorts first.
func orderCompare(c db2access.Column, job int, a, b any) (int, error) {
	n, err := compare(c, job, a, b)
	if n != 0 || err != nil {
		return n, err
	}
	if x, ok := a.(string); ok && !c.Graphic() { // VARGRAPHIC values that differ in trailing blanks tie (round 2, A27a)
		y, _ := b.(string)
		switch {
		case len(x) > len(y):
			return -1, nil
		case len(x) < len(y):
			return 1, nil
		}
	}
	return 0, nil
}

// match evaluates the AND of conds on one row. A comparison with NULL is unknown, so the
// row does not match, exactly as on Db2.
func match(d declTable, row map[string]any, conds []cond, vals []any) (bool, error) {
	for _, c := range conds {
		v := row[c.col]
		switch c.op {
		case "IS NULL":
			if v != nil {
				return false, nil
			}
			continue
		case "IS NOT NULL":
			if v == nil {
				return false, nil
			}
			continue
		}
		arg := vals[c.arg]
		if v == nil || arg == nil {
			return false, nil
		}
		n, err := compare(d.byName[c.col], d.job, v, arg)
		if err != nil {
			return false, err
		}
		ok := map[string]bool{"=": n == 0, "<>": n != 0, "<": n < 0, "<=": n <= 0, ">": n > 0, ">=": n >= 0}[c.op]
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// sortRows applies ORDER BY, stable so ties keep their order. NULL sorts high.
func sortRows(d declTable, rows []map[string]any, keys []orderKey) error {
	var err error
	sort.SliceStable(rows, func(i, j int) bool {
		for _, k := range keys {
			a, b := rows[i][k.col], rows[j][k.col]
			var n int
			switch {
			case a == nil && b == nil:
				continue
			case a == nil:
				n = 1
			case b == nil:
				n = -1
			default:
				var e error
				if n, e = orderCompare(d.byName[k.col], d.job, a, b); e != nil && err == nil {
					err = e
				}
			}
			if k.desc {
				n = -n
			}
			if n != 0 {
				return n < 0
			}
		}
		return false
	})
	return err
}

// project reads one select item from a stored row.
func project(it item, row map[string]any) (driver.Value, error) {
	v := row[it.col]
	if v == nil {
		return nil, nil
	}
	switch {
	case it.cast > 0:
		var s string
		switch x := v.(type) {
		case decCell:
			s = string(x)
		case tempCell:
			s = string(x)
		default:
			return nil, fmt.Errorf("db2fake: CAST(%s AS VARCHAR) of a %T: pkg/db2access casts only DECIMAL and TIMESTAMP columns", it.col, v)
		}
		if len(s) > it.cast {
			return nil, fmt.Errorf("db2fake: CAST(%s AS VARCHAR(%d)) truncates the value (SQLSTATE 22001 on Db2)", it.col, it.cast)
		}
		return s, nil
	case it.iso:
		if x, ok := v.(tempCell); ok {
			return string(x), nil
		}
		return nil, fmt.Errorf("db2fake: CHAR(%s, ISO) of a %T: pkg/db2access reads only DATE and TIME that way", it.col, v)
	}
	switch x := v.(type) {
	case decCell:
		return nil, fmt.Errorf("db2fake: DECIMAL column %s selected without CAST(... AS VARCHAR(n)): a statement not built by pkg/db2access", it.col)
	case tempCell:
		return nil, fmt.Errorf("db2fake: DATE, TIME or TIMESTAMP column %s selected without CHAR(..., ISO) or CAST(... AS VARCHAR(n)): a statement not built by pkg/db2access", it.col)
	case []byte:
		return append([]byte{}, x...), nil
	}
	return v, nil
}
