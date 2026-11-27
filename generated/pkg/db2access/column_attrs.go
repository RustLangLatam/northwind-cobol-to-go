package db2access

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/RustLangLatam/cobolnum-go/ebcdic"
)

// DefaultRule is what Db2 stores in a column an INSERT does not name.
type DefaultRule int

const (
	// DefaultImplied is the DDS rule for a field without ALWNULL and without DFT (NOT
	// NULL WITH DEFAULT): blanks for CHAR, the empty string for VARCHAR, zero for a
	// number, X'40' for CHAR FOR BIT DATA, X'00' for BINARY, the current date, time or
	// timestamp for a temporal field (the no-DFT values the analysis records, ADR-0062).
	// ParseColumn's columns follow it.
	DefaultImplied DefaultRule = iota
	// DefaultNull: the column is nullable and has no DFT (ALWNULL, or SQL nullable).
	DefaultNull
	// DefaultLiteral: a DFT value, given by Default as the text the host stores.
	DefaultLiteral
	// DefaultCurrent: DFT CURRENT_DATE, CURRENT_TIME or CURRENT_TIMESTAMP.
	DefaultCurrent
	// DefaultNone: NOT NULL without any default (SQL DDL without WITH DEFAULT); an
	// INSERT must name the column.
	DefaultNone
)

// Nullable reports whether the column accepts NULL (DDS ALWNULL, SQL nullable).
func (c Column) Nullable() bool { return c.nullable }

// Binary reports a BINARY or VARBINARY column (it pads with X'00' and drops an excess of
// X'00' bytes on assignment), as opposed to a FOR BIT DATA character column.
func (c Column) Binary() bool { return c.binary }

// IntBits is 16, 32 or 64 for SMALLINT, INTEGER or BIGINT (0 for other kinds).
func (c Column) IntBits() int { return c.intBits }

// FixedLength reports a CHAR, CHAR FOR BIT DATA or BINARY column, which Db2 pads to
// Length with pad (a blank for text, X'40' for FOR BIT DATA, X'00' for BINARY).
func (c Column) FixedLength() (fixed bool, pad byte) {
	switch {
	case c.varying || (c.kind != KindText && c.kind != KindBytes):
		return false, 0
	case c.binary:
		return true, 0x00
	case c.kind == KindBytes:
		return true, 0x40
	}
	return true, ' '
}

// Default is the column's default rule and, for DefaultLiteral, the value as the host
// stores it: canonical decimal text, ISO host temporal text, the text itself, or the
// bytes as a string. It does not depend on the order AllowNull, WithDefault and
// WithoutDefault are called in: a nullable column without an explicit default (or
// declared WithoutDefault) defaults to NULL, a NOT NULL one to the implied DDS default
// (or to none after WithoutDefault).
func (c Column) Default() (DefaultRule, string) {
	if !c.dftSet {
		if c.nullable {
			return DefaultNull, ""
		}
		return DefaultImplied, ""
	}
	if c.dftRule == DefaultNone && c.nullable {
		return DefaultNull, ""
	}
	return c.dftRule, c.dft
}

// AllowNull returns the column made nullable (DDS ALWNULL). Without an explicit
// default it defaults to NULL, as in DDS.
func (c Column) AllowNull() Column {
	c.nullable = true
	return c
}

// WithoutDefault returns the column with no default (SQL NOT NULL without WITH
// DEFAULT): an INSERT that omits it fails, as on Db2 (SQLSTATE 23502).
func (c Column) WithoutDefault() Column {
	c.dftSet, c.dftRule, c.dft = true, DefaultNone, ""
	return c
}

// WithDefault returns the column with the default as the schema facts give it
// (SchemaColumn.DefaultValue): a DFT literal ('ABC', X'C1C2', 5, 12,50, *NULL) or the
// documented no-DFT words BLANKS (character), ZEROS (numeric), two single quotes (the
// empty string), NULL, CURRENT_DATE, CURRENT_TIME, CURRENT_TIMESTAMP. An empty value
// removes an explicit default. A value that does not fit the column, or a word of the
// wrong type, is an error; errors do not echo the value. A NULL default needs
// AllowNull first.
func (c Column) WithDefault(value string) (Column, error) {
	v := strings.TrimSpace(value)
	word := strings.ToUpper(v)
	isText := c.kind == KindText || c.kind == KindBytes
	isNumber := c.kind == KindExactNum || c.kind == KindInt
	switch word {
	case "":
		c.dftSet, c.dftRule, c.dft = false, DefaultImplied, ""
		return c, nil
	case "BLANKS", "''", "ZEROS":
		if (word == "ZEROS" && !isNumber) || (word != "ZEROS" && !isText) {
			return Column{}, fmt.Errorf("db2access: column %s: the default %s does not match the column type", c.name, word)
		}
		c.dftSet, c.dftRule, c.dft = true, DefaultImplied, ""
		return c, nil
	case "NULL", "*NULL":
		if !c.nullable {
			return Column{}, fmt.Errorf("db2access: column %s: a NULL default needs a nullable column (ALWNULL)", c.name)
		}
		c.dftSet, c.dftRule, c.dft = true, DefaultNull, ""
		return c, nil
	case "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP":
		want := map[string]Kind{"CURRENT_DATE": KindDate, "CURRENT_TIME": KindTime, "CURRENT_TIMESTAMP": KindTimestamp}[word]
		if c.kind != want {
			return Column{}, fmt.Errorf("db2access: column %s: the default %s does not match the column type", c.name, word)
		}
		c.dftSet, c.dftRule, c.dft = true, DefaultCurrent, ""
		return c, nil
	}
	lit, err := c.defaultLiteral(v)
	if err != nil {
		return Column{}, fmt.Errorf("db2access: column %s: the default is not a value of the column: %w", c.name, err)
	}
	c.dftSet, c.dftRule, c.dft = true, DefaultLiteral, lit
	return c, nil
}

// unquote reads a quoted literal in which a quote is written twice; a lone quote inside
// it is malformed.
func unquote(v string) (string, bool) {
	if len(v) < 2 || v[0] != '\'' || v[len(v)-1] != '\'' {
		return "", false
	}
	inner := v[1 : len(v)-1]
	if strings.Contains(strings.ReplaceAll(inner, "''", ""), "'") {
		return "", false
	}
	return strings.ReplaceAll(inner, "''", "'"), true
}

func (c Column) defaultLiteral(v string) (string, error) {
	switch c.kind {
	case KindText:
		s, ok := unquote(v)
		if !ok {
			return "", errors.New("a character default is a quoted literal")
		}
		if err := c.checkUTF8(s); err != nil {
			return "", err
		}
		if c.ccsid != 0 { // no CCSID clause: the job CCSID is not known here; Open checks it (A43)
			if err := c.CheckText(s); err != nil {
				return "", err
			}
		}
		n := utf8.RuneCountInString(s)
		switch {
		case c.ccsid == 1208: // the host counts the bytes of UTF-8 text
			n = len(s)
		case c.graphic: // UTF-16 code units: a character beyond the BMP counts 2
			n = len(utf16.Encode([]rune(s)))
		}
		if n > c.length {
			return "", errors.New("longer than the column")
		}
		return s, nil
	case KindBytes:
		if len(v) < 3 || (v[0] != 'X' && v[0] != 'x') {
			return "", errors.New("a binary default is a hexadecimal literal X'..'")
		}
		h, ok := unquote(v[1:])
		if !ok {
			return "", errors.New("a binary default is a hexadecimal literal X'..'")
		}
		b, err := hex.DecodeString(h)
		if err != nil || len(b) > c.length {
			return "", errors.New("not hexadecimal, or longer than the column")
		}
		return string(b), nil
	case KindExactNum:
		if strings.Count(v, ",")+strings.Count(v, ".") > 1 {
			return "", errors.New("more than one decimal separator")
		}
		return canonExact(c, strings.Replace(v, ",", ".", 1))
	case KindInt:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return "", errors.New("not an integer")
		}
		if lo, hi := c.IntRange(); n < lo || n > hi {
			return "", errors.New("outside the range of the integer type")
		}
		return strconv.FormatInt(n, 10), nil
	case KindDate, KindTime, KindTimestamp:
		s, ok := unquote(v)
		if !ok {
			return "", errors.New("a temporal default is a quoted literal")
		}
		if c.kind == KindTime && len(s) == 8 { // DDS writes TIME as HH.MM.SS
			s = strings.ReplaceAll(s, ".", ":")
		}
		return hostTemporal(c, s)
	}
	return "", errors.New("the column type takes no default here")
}

// IntRange is the range of a SMALLINT, INTEGER or BIGINT column.
func (c Column) IntRange() (lo, hi int64) {
	switch c.intBits {
	case 16:
		return math.MinInt16, math.MaxInt16
	case 32:
		return math.MinInt32, math.MaxInt32
	}
	return math.MinInt64, math.MaxInt64
}

// ColumnFromFacts builds a column from one row of the schema facts (dbtarget
// SchemaColumn): SQLType as declaredType, Nullability (NOT_NULL or NULLABLE),
// HasDefault and DefaultValue. It is how a generated service declares a table so the
// column matches what the analysis measured from the DDS or the SQL DDL. An UNKNOWN
// nullability is an error: the declaration must not guess it. NOT_NULL with
// hasDefault false is an SQL DDL column without WITH DEFAULT (WithoutDefault); a DDS
// field always has a default.
func ColumnFromFacts(name, declaredType, nullability string, hasDefault bool, defaultValue string) (Column, error) {
	c, err := ParseColumn(name, declaredType)
	if err != nil {
		return Column{}, err
	}
	switch nullability {
	case "NOT_NULL":
	case "NULLABLE":
		c = c.AllowNull()
	default:
		return Column{}, fmt.Errorf("db2access: column %s: the nullability is not NOT_NULL or NULLABLE; declare it from the DDS (ALWNULL) or the DDL", c.name)
	}
	if !hasDefault {
		return c.WithoutDefault(), nil // nullable: NULL; NOT NULL: a value is required
	}
	return c.WithDefault(defaultValue)
}

// utf8CCSID is CCSID 1208: UTF-8 holds any character.
const utf8CCSID = 1208

// CheckText is CheckTextFor with no job CCSID: a column that declares none takes only
// 7-bit ASCII. Statements built by this package are checked by the Store with
// Config.CCSID, so this is for callers that guard a value themselves.
func (c Column) CheckText(s string) error { return c.CheckTextFor(0, s) }

// checkUTF8 is the A50 guard, applied before any CCSID rule, on the builders' path and the Store's.
func (c Column) checkUTF8(s string) error {
	if utf8.ValidString(s) {
		return nil
	}
	return &Error{Op: "bind", SQLState: SQLStateInvalidText,
		Message: "column " + c.name + ": the text is not valid UTF-8 (a lone surrogate or a cut sequence); the driver would send U+FFFD with no warning (platform guard, not a host SQLSTATE)"}
}

// unicodeCCSID reports a CCSID whose text is Unicode, so any valid character is held:
// UTF-8 (1208), UTF-16 (1200) and UCS-2 (13488, which also takes a surrogate pair).
func unicodeCCSID(id int) bool { return id == utf8CCSID || id == 1200 || id == 13488 }

// mixedVariantChars are the ASCII characters that move between EBCDIC code pages. PUB400
// round 4 measured letters, digits and . , - _ / ( ) for 930 and 5035, not these (A61).
// CheckTextFor refuses them under every CCSID ebcdic.Lookup does not resolve (A62).
const mixedVariantChars = `\~[]^|`

// mixedCCSID reports the mixed single/double-byte EBCDIC CCSIDs measured on PUB400 (930
// and 5035). Their conversion tables are not available offline, so what they can hold
// cannot be checked; Validate accepts them as job CCSIDs on that basis.
func mixedCCSID(id int) bool { return id == 930 || id == 5035 }

// CheckTextFor refuses text the column cannot hold faithfully. The text must be valid
// UTF-8 first: the driver sends each bad byte as U+FFFD with no warning (PUB400 round 3),
// so a lone surrogate is stored as another value (SQLStateInvalidText). Db2 for i also
// accepts a character the CCSID lacks without error and stores the EBCDIC substitution
// character (X'3F'): silent data corruption, so the check is done here, before the
// statement is sent. The CCSID is the column's own or, when it declares none, jobCCSID: a
// CHAR or VARCHAR with no CCSID clause takes the job's DEFAULT CCSID (273 on PUB400,
// where the euro sign and CJK text became X'3F'). Unicode CCSIDs (1208, 1200, 13488) hold
// any valid text. A mixed CCSID (930, 5035) or one unknown here cannot be verified, so
// only 7-bit ASCII passes (SQLStateUnverifiableText); that is the documented limit, no
// offline table checks what such a CCSID can encode. The error never carries the value.
func (c Column) CheckTextFor(jobCCSID int, s string) error {
	if err := c.checkUTF8(s); err != nil {
		return err
	}
	id := c.ccsid
	if id == 0 {
		id = jobCCSID
	}
	if unicodeCCSID(id) {
		return nil
	}
	cp, ok := ebcdic.Lookup(id)
	if ok && cp.PassThrough() {
		return nil
	}
	if !ok {
		if isASCII(s) && !strings.ContainsAny(s, mixedVariantChars) {
			return nil
		}
		if isASCII(s) { // A61, A62: a variant character under any CCSID Lookup does not resolve
			return &Error{Op: "bind", SQLState: SQLStateUnverifiableText,
				Message: "column " + c.name + ": the value holds a variant character (backslash, tilde, brackets, caret or bar) that has not been measured for this CCSID (mixed, double-byte or unknown, or the job CCSID 0 opt-out), where such characters move between code pages (platform guard, not a host SQLSTATE)"}
		}
		return &Error{Op: "bind", SQLState: SQLStateUnverifiableText,
			Message: "column " + c.name + ": the CCSID of its text is mixed (930, 5035), unknown or multibyte, so only 7-bit ASCII is accepted; no offline table verifies what it can encode (platform guard, not a host SQLSTATE)"}
	}
	if _, err := cp.Strict().Encode(s); err != nil {
		return &Error{Op: "bind", SQLState: SQLStateNotRepresentable,
			Message: "column " + c.name + " (CCSID " + strconv.Itoa(id) + ") cannot represent a character of the value; the host would store a substitution character silently (platform guard, not a host SQLSTATE)"}
	}
	return nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
