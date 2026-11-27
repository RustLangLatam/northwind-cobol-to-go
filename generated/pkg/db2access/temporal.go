package db2access

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Db2 DATE, TIME and TIMESTAMP(p) values (ADR-0064). Each is a civil value with no time
// zone, backed by its canonical text, like Exact: time.Time keeps nine fraction digits
// and a location, a Db2 for i TIMESTAMP keeps up to twelve and none.
//
// Text forms. The canonical text (String) is ISO 8601 without a zone: "2026-11-23",
// "10:20:30", "2026-11-23T10:20:30.123456789012" (fraction trailing zeros dropped, so
// == compares values). What the host reads and writes is its own ISO form: "10.20.30"
// and "2026-11-23-10.20.30.123456"; the SQL builders convert, and the host is told the
// format in every statement (CHAR(col, ISO), a VARCHAR marker), so the job's date
// format (*MDY, *DMY, *YMD, *JUL) and separators never apply.
//
// Range: years 0001 to 9999 (Db2's), the Gregorian calendar, seconds 0 to 59. Db2 also
// accepts the end of day 24:00:00 (fraction zero), kept as is and distinct from 00:00:00.

// MaxTimestampPrecision is the largest TIMESTAMP fractional precision on Db2 for i.
const MaxTimestampPrecision = 12

// DefaultTimestampPrecision is the precision of a TIMESTAMP declared without one.
const DefaultTimestampPrecision = 6

// Date is a Db2 DATE: year, month and day, no time zone. The zero value is no date (a
// NULL read gives it; binding it is an error).
type Date struct{ text string } // "YYYY-MM-DD"

// Time is a Db2 TIME: hour, minute and second, no fraction and no time zone.
type Time struct{ text string } // "HH:MM:SS"

// Timestamp is a Db2 TIMESTAMP(p), p up to 12: a civil date and time with no time zone.
type Timestamp struct{ text string } // "YYYY-MM-DDTHH:MM:SS[.f{1,12}]", no trailing zero

var (
	errDateForm      = errors.New("db2access: not an ISO date YYYY-MM-DD between 0001-01-01 and 9999-12-31")
	errTimeForm      = errors.New("db2access: not a time HH:MM:SS or HH.MM.SS between 00:00:00 and 24:00:00")
	errTimestampForm = errors.New("db2access: not a timestamp YYYY-MM-DDTHH:MM:SS[.f], YYYY-MM-DD HH:MM:SS[.f] or YYYY-MM-DD-HH.MM.SS[.f] with at most 12 fraction digits and no time zone")
	// ponytail: 24:00:00 is refused on bind until a real host shows it is stored as is
	// (task 3.1): a host that normalises it to 00:00:00 would change the value silently.
	// It is still read and parsed, since existing rows may hold it.
	errBindEndOfDay = errors.New("db2access: the end of day (hour 24) is not bound until a host is verified to keep it (it may be stored as midnight); bind midnight of the next day instead")
	errOutsideNanos = errors.New("db2access: the timestamp has non-zero digits below the nanosecond, which time.Time cannot hold")
)

// ParseDate reads "YYYY-MM-DD" (surrounding spaces allowed). Its errors never contain s.
func ParseDate(s string) (Date, error) {
	t := strings.TrimSpace(s)
	if _, _, _, ok := dateParts(t); !ok {
		return Date{}, errDateForm
	}
	return Date{text: t}, nil
}

// DateOf is the date year-month-day; a day the month does not have is an error, never
// normalised into the next month.
func DateOf(year int, month time.Month, day int) (Date, error) {
	if !validDate(year, int(month), day) {
		return Date{}, errDateForm
	}
	return Date{text: fmt.Sprintf("%04d-%02d-%02d", year, int(month), day)}, nil
}

// DateOfTime is the calendar date of t's wall clock in t's own location.
func DateOfTime(t time.Time) Date {
	y, m, d := t.Date()
	out, _ := DateOf(y, m, d) // a time.Time date is valid; outside 0001..9999 the result is the zero Date
	return out
}

func (d Date) String() string { return d.text }

// IsZero reports the zero Date (a NULL read).
func (d Date) IsZero() bool { return d.text == "" }

// Date returns year, month and day (zeros for the zero Date).
func (d Date) Date() (year int, month time.Month, day int) {
	y, m, dd, _ := dateParts(d.text)
	return y, time.Month(m), dd
}

// In is midnight of the date in loc.
// The zero Date gives the zero time.Time.
func (d Date) In(loc *time.Location) time.Time {
	if d.IsZero() {
		return time.Time{}
	}
	y, m, dd := d.Date()
	return time.Date(y, m, dd, 0, 0, 0, 0, loc)
}

// ParseTime reads "HH:MM:SS" (ISO 8601) or "HH.MM.SS" (the IBM i ISO form).
func ParseTime(s string) (Time, error) {
	t := strings.TrimSpace(s)
	if len(t) != 8 {
		return Time{}, errTimeForm
	}
	h, m, sec, ok := clockParts(t, t[2])
	if !ok || (t[2] != ':' && t[2] != '.') {
		return Time{}, errTimeForm
	}
	return Time{text: fmt.Sprintf("%02d:%02d:%02d", h, m, sec)}, nil
}

// TimeOf is hour:minute:second; 24:00:00 is allowed, as in Db2.
func TimeOf(hour, minute, second int) (Time, error) {
	if !validClock(hour, minute, second) {
		return Time{}, errTimeForm
	}
	return Time{text: fmt.Sprintf("%02d:%02d:%02d", hour, minute, second)}, nil
}

// TimeOfTime is the wall-clock time of t in t's own location, to the second.
func TimeOfTime(t time.Time) Time {
	h, m, s := t.Clock()
	out, _ := TimeOf(h, m, s)
	return out
}

func (t Time) String() string { return t.text }

// IsZero reports the zero Time (a NULL read); midnight is "00:00:00", not zero.
func (t Time) IsZero() bool { return t.text == "" }

// Clock returns hour, minute and second.
func (t Time) Clock() (hour, minute, second int) {
	h, m, s, _ := clockParts(t.text, ':')
	return h, m, s
}

// ParseTimestamp reads "YYYY-MM-DDTHH:MM:SS[.f]", the same with a space for the T, or
// the IBM i form "YYYY-MM-DD-HH.MM.SS[.f]", with 1 to 12 fraction digits and no zone.
func ParseTimestamp(s string) (Timestamp, error) {
	ts, _, _, err := parseTimestamp(strings.TrimSpace(s))
	return ts, err
}

// TimestampOf is t's wall clock in t's own location, to the nanosecond, with no zone:
// convert t to the location the column means (t.In(loc)) before calling it.
func TimestampOf(t time.Time) Timestamp {
	frac := strings.TrimRight(fmt.Sprintf("%09d", t.Nanosecond()), "0")
	text := t.Format("2006-01-02T15:04:05")
	if frac != "" {
		text += "." + frac
	}
	if _, _, _, err := parseTimestamp(text); err != nil {
		return Timestamp{} // a year outside 0001..9999
	}
	return Timestamp{text: text}
}

func (ts Timestamp) String() string { return ts.text }

// IsZero reports the zero Timestamp (a NULL read).
func (ts Timestamp) IsZero() bool { return ts.text == "" }

// Date is the date part.
func (ts Timestamp) Date() Date {
	if ts.text == "" {
		return Date{}
	}
	return Date{text: ts.text[:10]}
}

// Time is the time part, to the second.
func (ts Timestamp) Time() Time {
	if ts.text == "" {
		return Time{}
	}
	return Time{text: ts.text[11:19]}
}

// Fraction is the fraction of the second as its digits, trailing zeros dropped ("" for
// a whole second): up to 12 digits, picoseconds.
func (ts Timestamp) Fraction() string {
	if len(ts.text) <= 20 {
		return ""
	}
	return ts.text[20:]
}

// In is the timestamp as a time.Time in loc. It fails when a digit below the
// nanosecond is not zero: time.Time would drop it. 24:00:00 becomes 00:00:00 of the
// next day, the same instant.
func (ts Timestamp) In(loc *time.Location) (time.Time, error) {
	if ts.IsZero() {
		return time.Time{}, errors.New("db2access: the zero Timestamp is no timestamp")
	}
	frac := ts.Fraction()
	if len(frac) > 9 {
		return time.Time{}, errOutsideNanos
	}
	ns := 0
	for _, c := range frac + strings.Repeat("0", 9-len(frac)) {
		ns = ns*10 + int(c-'0')
	}
	y, m, d := ts.Date().Date()
	h, mi, s := ts.Time().Clock()
	return time.Date(y, m, d, h, mi, s, ns, loc), nil
}

// parseTimestamp validates t (already trimmed) and returns the value, whether it was in
// the IBM i form, and the number of fraction digits as written.
func parseTimestamp(t string) (ts Timestamp, ibmForm bool, digits int, err error) {
	if len(t) < 19 {
		return Timestamp{}, false, 0, errTimestampForm
	}
	if _, _, _, ok := dateParts(t[:10]); !ok {
		return Timestamp{}, false, 0, errTimestampForm
	}
	var sep byte
	switch t[10] {
	case 'T', ' ':
		sep = ':'
	case '-':
		sep = '.'
	default:
		return Timestamp{}, false, 0, errTimestampForm
	}
	h, m, s, ok := clockParts(t[11:19], sep)
	if !ok {
		return Timestamp{}, false, 0, errTimestampForm
	}
	frac := ""
	if len(t) > 19 {
		if t[19] != '.' || len(t) == 20 || len(t)-20 > MaxTimestampPrecision || !allDigits(t[20:]) {
			return Timestamp{}, false, 0, errTimestampForm
		}
		frac = t[20:]
	}
	written := len(frac)
	frac = strings.TrimRight(frac, "0")
	if h == 24 && frac != "" {
		return Timestamp{}, false, 0, errTimestampForm
	}
	text := fmt.Sprintf("%sT%02d:%02d:%02d", t[:10], h, m, s)
	if frac != "" {
		text += "." + frac
	}
	return Timestamp{text: text}, sep == '.', written, nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// num reads a fixed run of ASCII digits; -1 when any byte is not one.
func num(s string) int {
	if !allDigits(s) {
		return -1
	}
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func dateParts(s string) (y, m, d int, ok bool) {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return 0, 0, 0, false
	}
	y, m, d = num(s[:4]), num(s[5:7]), num(s[8:])
	return y, m, d, validDate(y, m, d)
}

func validDate(y, m, d int) bool {
	if y < 1 || y > 9999 || m < 1 || m > 12 || d < 1 {
		return false
	}
	// time.Date normalises an impossible day into the next month: compare.
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC).Day() == d
}

func clockParts(s string, sep byte) (h, m, sec int, ok bool) {
	if len(s) != 8 || s[2] != sep || s[5] != sep {
		return 0, 0, 0, false
	}
	h, m, sec = num(s[:2]), num(s[3:5]), num(s[6:])
	return h, m, sec, validClock(h, m, sec)
}

func validClock(h, m, s int) bool {
	if h == 24 {
		return m == 0 && s == 0
	}
	return h >= 0 && h <= 23 && m >= 0 && m <= 59 && s >= 0 && s <= 59
}

// timestampWidth is the length of the IBM i text of a TIMESTAMP(p).
func timestampWidth(p int) int {
	if p == 0 {
		return 19
	}
	return 20 + p
}

// hostTemporal is the text bound for a temporal column: the host's ISO form, a
// TIMESTAMP padded to exactly the column's precision. A value that does not fit (a
// fraction beyond the precision) is refused, never truncated. Errors never contain v.
func hostTemporal(c Column, v any) (string, error) {
	switch c.kind {
	case KindDate:
		d, ok := v.(Date)
		if s, isText := v.(string); isText {
			var err error
			if d, err = ParseDate(s); err != nil {
				return "", fmt.Errorf("db2access: column %s DATE: %w", c.name, err)
			}
			ok = true
		}
		if !ok {
			return "", fmt.Errorf("db2access: column %s is DATE: bind a db2access.Date or ISO text YYYY-MM-DD, not %T", c.name, v)
		}
		if d.IsZero() {
			return "", fmt.Errorf("db2access: column %s DATE: the zero Date (unset, or a year outside 0001..9999) is no date; bind nil for NULL", c.name)
		}
		return d.text, nil
	case KindTime:
		t, ok := v.(Time)
		if s, isText := v.(string); isText {
			var err error
			if t, err = ParseTime(s); err != nil {
				return "", fmt.Errorf("db2access: column %s TIME: %w", c.name, err)
			}
			ok = true
		}
		if !ok {
			return "", fmt.Errorf("db2access: column %s is TIME: bind a db2access.Time or text HH:MM:SS, not %T", c.name, v)
		}
		if t.IsZero() {
			return "", fmt.Errorf("db2access: column %s TIME: the zero Time (unset) is no time; bind nil for NULL", c.name)
		}
		if t.text[:2] == "24" {
			return "", fmt.Errorf("db2access: column %s TIME: %w", c.name, errBindEndOfDay)
		}
		return strings.ReplaceAll(t.text, ":", "."), nil
	case KindTimestamp:
		ts, ok := v.(Timestamp)
		if s, isText := v.(string); isText {
			var err error
			if ts, err = ParseTimestamp(s); err != nil {
				return "", fmt.Errorf("db2access: column %s TIMESTAMP(%d): %w", c.name, c.precision, err)
			}
			ok = true
		}
		if !ok {
			return "", fmt.Errorf("db2access: column %s is TIMESTAMP(%d): bind a db2access.Timestamp (TimestampOf for a time.Time) or ISO text, not %T", c.name, c.precision, v)
		}
		if ts.IsZero() {
			return "", fmt.Errorf("db2access: column %s TIMESTAMP(%d): the zero Timestamp (unset, or a year outside 0001..9999) is no timestamp; bind nil for NULL", c.name, c.precision)
		}
		if ts.text[11:13] == "24" {
			return "", fmt.Errorf("db2access: column %s TIMESTAMP(%d): %w", c.name, c.precision, errBindEndOfDay)
		}
		frac := ts.Fraction()
		if len(frac) > c.precision {
			return "", fmt.Errorf("db2access: column %s TIMESTAMP(%d): the value has more than %d fraction digits and would be truncated", c.name, c.precision, c.precision)
		}
		out := ts.text[:10] + "-" + strings.ReplaceAll(ts.text[11:19], ":", ".")
		if c.precision > 0 {
			out += "." + frac + strings.Repeat("0", c.precision-len(frac))
		}
		return out, nil
	}
	return "", fmt.Errorf("db2access: column %s is not a temporal column", c.name)
}

// readTemporal parses what the SELECT of a temporal column returns: exactly the host's
// ISO form, a TIMESTAMP with exactly the column's precision of fraction digits. Anything
// else (a job date format, another digit count) is an error that does not echo s.
// No padding is trimmed: the host returns none, so padding is a driver or CCSID anomaly.
func readTemporal(c Column, t string) (any, error) {
	switch c.kind {
	case KindDate:
		if len(t) == 10 {
			if d, err := ParseDate(t); err == nil {
				return d, nil
			}
		}
		return nil, fmt.Errorf("db2access: column %s DATE: the database returned text that is not an ISO date", c.name)
	case KindTime:
		if len(t) == 8 && t[2] == '.' {
			if v, err := ParseTime(t); err == nil {
				return v, nil
			}
		}
		return nil, fmt.Errorf("db2access: column %s TIME: the database returned text that is not an ISO time HH.MM.SS", c.name)
	case KindTimestamp:
		ts, ibmForm, digits, err := parseTimestamp(t)
		if err == nil && ibmForm && digits == c.precision {
			return ts, nil
		}
		return nil, fmt.Errorf("db2access: column %s TIMESTAMP(%d): the database returned text that is not YYYY-MM-DD-HH.MM.SS with exactly %d fraction digits", c.name, c.precision, c.precision)
	}
	return nil, fmt.Errorf("db2access: column %s is not a temporal column", c.name)
}
