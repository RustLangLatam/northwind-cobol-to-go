package db2access

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/RustLangLatam/cobolnum-go/ebcdic"
)

// Result memory. A result is held in a slice, so the row cap alone does not bound it:
// the bounds below count bytes, with the slice and Row overhead and not only the payload.
const (
	// HardMaxResultBytes bounds one result.
	HardMaxResultBytes = 32 << 20
	// HardMaxProcessResultBytes bounds the results being built at once by every Store of
	// the process (256 connections at HardMaxResultBytes each would be 8 GiB). It bounds
	// the scan, not what a caller keeps afterwards: rows a caller holds are its own.
	HardMaxProcessResultBytes = 256 << 20
	// RowOverheadBytes and ValueOverheadBytes are charged per row and per value on top of
	// the text, bytes or number itself (the Row header, the interface of each value).
	RowOverheadBytes   = 64
	ValueOverheadBytes = 16
)

// ErrResultTooLarge is returned when one result exceeds HardMaxResultBytes.
var ErrResultTooLarge = errors.New("db2access: the result is larger than the memory bound (HardMaxResultBytes); set a smaller Select.Limit, select fewer columns or narrow the WHERE")

// ErrResultMemoryBusy is returned when the results being built by the whole process
// would exceed HardMaxProcessResultBytes. It is transient: retry later or read less.
// The process budget is a non-blocking counter, not a queue: two large scans running
// together can both get it, and nothing is held for the one that failed. The retry
// contract is the caller's: it is safe to repeat a read (nothing was changed), with a
// backoff, or to page it with a smaller Select.Limit. It does not bound the rows a
// caller keeps after the query returns.
var ErrResultMemoryBusy = errors.New("db2access: too many large results are being read at once (HardMaxProcessResultBytes); retry later or read less per query")

// ErrNull is returned by a plain accessor (Text, Int, ...) on a NULL value: use the
// Nullable accessor and check its second result.
var ErrNull = errors.New("db2access: the column is NULL; read it with the Nullable accessor")

// resultBudget bounds the bytes of the results being built at once.
type resultBudget struct {
	mu        sync.Mutex
	max, used int
}

func newResultBudget(max int) *resultBudget { return &resultBudget{max: max} }

// processResultBudget is shared by every Store; its cap never changes.
var processResultBudget = newResultBudget(HardMaxProcessResultBytes)

func (b *resultBudget) reserve(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+n > b.max {
		return false
	}
	b.used += n
	return true
}

func (b *resultBudget) release(n int) {
	b.mu.Lock()
	b.used -= n
	b.mu.Unlock()
}

// InUse is the bytes currently reserved.
func (b *resultBudget) InUse() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// rowShape is what the rows of one result share: the columns and the name index.
type rowShape struct {
	cols []Column
	idx  map[string]int // upper-case names
	// alias remembers the spellings of valid names that are not upper case ("amt"), so a
	// lower-case accessor does not upper-case on every call. Only names that resolve are
	// stored, and no more than maxAliases of them.
	alias   sync.Map
	aliases atomic.Int32
}

const maxAliases = 64

func newRowShape(cols []Column) *rowShape {
	s := &rowShape{cols: cols, idx: make(map[string]int, len(cols))}
	for i, c := range cols {
		s.idx[c.name] = i
	}
	return s
}

// Row is one result row, typed by the columns of the statement that produced it.
type Row struct {
	shape *rowShape
	vals  []any // nil (NULL), string, []byte, Exact, int64, Date, Time or Timestamp
}

// index resolves a column name (case-insensitive) against the shared index.
func (r Row) index(name string) (int, error) {
	if r.shape != nil {
		if i, ok := r.shape.idx[name]; ok {
			return i, nil
		}
		if i, ok := r.shape.alias.Load(name); ok {
			return i.(int), nil
		}
		if i, ok := r.shape.idx[strings.ToUpper(name)]; ok {
			// the counter stops at the cap: a CAS, so concurrent callers never exceed it
			if n := r.shape.aliases.Load(); n < maxAliases && r.shape.aliases.CompareAndSwap(n, n+1) {
				r.shape.alias.Store(name, i)
			}
			return i, nil
		}
	}
	return 0, fmt.Errorf("db2access: column %q is not in the row", name)
}

// value reads a column of the wanted kind; valid is false for NULL.
func value[T any](r Row, name string, want Kind) (v T, valid bool, err error) {
	i, err := r.index(name)
	if err != nil {
		return v, false, err
	}
	if c := r.shape.cols[i]; c.kind != want {
		return v, false, fmt.Errorf("db2access: column %s has a different kind than the accessor used", c.name)
	}
	if r.vals[i] == nil {
		return v, false, nil
	}
	v, _ = r.vals[i].(T)
	return v, true, nil
}

// required is value for the plain accessors: NULL is ErrNull.
func required[T any](r Row, name string, want Kind) (T, error) {
	v, valid, err := value[T](r, name, want)
	if err == nil && !valid {
		err = fmt.Errorf("%w: %s", ErrNull, strings.ToUpper(name))
	}
	return v, err
}

// IsNull reports whether the column is NULL. An unknown column is an error, not NULL.
func (r Row) IsNull(name string) (bool, error) {
	i, err := r.index(name)
	if err != nil {
		return false, err
	}
	return r.vals[i] == nil, nil
}

// Text returns a text column; a NULL is ErrNull (use NullableText).
func (r Row) Text(name string) (string, error) { return required[string](r, name, KindText) }

// NullableText is Text for a column that may be NULL: valid is false for NULL.
func (r Row) NullableText(name string) (v string, valid bool, err error) {
	return value[string](r, name, KindText)
}

// Bytes returns a copy of a FOR BIT DATA, CCSID 65535, BINARY or VARBINARY column,
// untranslated: changing it does not change the row.
func (r Row) Bytes(name string) ([]byte, error) {
	b, err := required[[]byte](r, name, KindBytes)
	return append([]byte{}, b...), err // non-nil even when empty, as NullableBytes returns it
}

// NullableBytes is Bytes for a column that may be NULL.
func (r Row) NullableBytes(name string) (v []byte, valid bool, err error) {
	v, valid, err = value[[]byte](r, name, KindBytes)
	if valid {
		v = append([]byte{}, v...) // non-nil even when empty: an empty value is not NULL
	}
	return v, valid, err
}

// Exact returns a DECIMAL/NUMERIC column.
func (r Row) Exact(name string) (Exact, error) { return required[Exact](r, name, KindExactNum) }

// NullableExact is Exact for a column that may be NULL.
func (r Row) NullableExact(name string) (v Exact, valid bool, err error) {
	return value[Exact](r, name, KindExactNum)
}

// Int returns a SMALLINT, INTEGER or BIGINT column.
func (r Row) Int(name string) (int64, error) { return required[int64](r, name, KindInt) }

// NullableInt is Int for a column that may be NULL.
func (r Row) NullableInt(name string) (v int64, valid bool, err error) {
	return value[int64](r, name, KindInt)
}

// Date returns a DATE column.
func (r Row) Date(name string) (Date, error) { return required[Date](r, name, KindDate) }

// NullableDate is Date for a column that may be NULL.
func (r Row) NullableDate(name string) (v Date, valid bool, err error) {
	return value[Date](r, name, KindDate)
}

// Time returns a TIME column.
func (r Row) Time(name string) (Time, error) { return required[Time](r, name, KindTime) }

// NullableTime is Time for a column that may be NULL.
func (r Row) NullableTime(name string) (v Time, valid bool, err error) {
	return value[Time](r, name, KindTime)
}

// Timestamp returns a TIMESTAMP column with every fraction digit.
func (r Row) Timestamp(name string) (Timestamp, error) {
	return required[Timestamp](r, name, KindTimestamp)
}

// NullableTimestamp is Timestamp for a column that may be NULL.
func (r Row) NullableTimestamp(name string) (v Timestamp, valid bool, err error) {
	return value[Timestamp](r, name, KindTimestamp)
}

// EBCDIC decodes a bytes column the caller knows holds EBCDIC text of the given
// CCSID (the host did not translate it). It is the only place text is decoded here.
func (r Row) EBCDIC(name string, ccsid int) (string, error) {
	b, err := r.Bytes(name)
	if err != nil {
		return "", err
	}
	cp, ok := ebcdic.Lookup(ccsid)
	if !ok {
		return "", fmt.Errorf("db2access: CCSID %d is not an EBCDIC code page known to cobolnum", ccsid)
	}
	return cp.Decode(b)
}

// scanRows reads every row, charging the bytes against the per-result bound and the
// process budget, which it releases when the scan ends.
func scanRows(rows *sql.Rows, cols []Column, l limits) ([]Row, error) {
	var out []Row
	shape := newRowShape(cols)
	size, reserved := 0, 0
	defer func() { l.budget.release(reserved) }()
	for rows.Next() {
		dest := make([]any, len(cols))
		for i, c := range cols {
			switch c.kind {
			case KindBytes:
				dest[i] = new([]byte)
			case KindInt:
				dest[i] = new(sql.NullInt64)
			default: // text, and exact numerics and temporals as their cast text
				dest[i] = new(sql.NullString)
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, Classify("scan", err)
		}
		n := RowOverheadBytes + len(dest)*ValueOverheadBytes
		for _, d := range dest {
			switch d := d.(type) {
			case *[]byte:
				n += len(*d)
			case *sql.NullString:
				n += len(d.String)
			}
		}
		if size += n; size > l.maxResultBytes {
			return nil, ErrResultTooLarge
		}
		if !l.budget.reserve(n) {
			return nil, ErrResultMemoryBusy
		}
		reserved += n
		vals := make([]any, len(cols))
		for i, c := range cols {
			switch d := dest[i].(type) {
			case *[]byte:
				if *d != nil {
					vals[i] = append([]byte{}, *d...) // non-nil when empty: an empty value is not NULL
				}
			case *sql.NullInt64:
				if d.Valid {
					vals[i] = d.Int64
				}
			case *sql.NullString:
				if !d.Valid {
					continue
				}
				switch c.kind {
				case KindExactNum:
					e, err := newExact(c, d.String)
					if err != nil {
						return nil, err
					}
					vals[i] = e
				case KindDate, KindTime, KindTimestamp:
					v, err := readTemporal(c, d.String)
					if err != nil {
						return nil, err
					}
					vals[i] = v
				default:
					vals[i] = d.String
				}
			}
		}
		out = append(out, Row{shape: shape, vals: vals})
	}
	return out, Classify("read rows", rows.Err())
}
