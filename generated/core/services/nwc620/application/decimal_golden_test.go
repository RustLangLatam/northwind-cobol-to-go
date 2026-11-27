package application

import (
	"testing"

	"github.com/RustLangLatam/cobolnum-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"milton_prism/core/services/nwc620/domain"
)

func parse(t *testing.T, p cobolnum.Pic, s string) cobolnum.Dec {
	t.Helper()
	v, sizeErr, err := p.ParseDec(s, cobolnum.Truncation)
	require.NoError(t, err)
	require.False(t, sizeErr, s)
	return v
}

// LK-ORD-NO PIC S9(7) COMP-3 (the contract's only COBOL item, COBOL digits=7).
func TestGolden_LkOrdNo(t *testing.T) {
	t.Parallel()
	p := domain.PicOrderNo
	// The PIC limits.
	assert.Equal(t, "9999999", parse(t, p, "9999999").Plain())
	assert.Equal(t, "-9999999", parse(t, p, "-9999999").Plain())
	// Store of an eighth digit drops the high-order one, with a size error.
	v, sizeErr := p.Store(cobolnum.FromInt64(10000000), cobolnum.Truncation)
	assert.True(t, sizeErr)
	assert.Equal(t, "0", v.Plain())
	v, sizeErr = p.Store(cobolnum.FromInt64(12345678), cobolnum.Truncation)
	assert.True(t, sizeErr)
	assert.Equal(t, "2345678", v.Plain())
	// Fractions are truncated on store, rounded only with ROUNDED.
	v, _, err := p.ParseDec("10248.9", cobolnum.Truncation)
	require.NoError(t, err)
	assert.Equal(t, "10248", v.Plain())
	v, _, err = p.ParseDec("10248.5", cobolnum.Rounded)
	require.NoError(t, err)
	assert.Equal(t, "10249", v.Plain())
	v, _, err = p.ParseDec("-10248.5", cobolnum.Rounded)
	require.NoError(t, err)
	assert.Equal(t, "-10249", v.Plain())
	// The packed image is 4 bytes (byte_len=4) and round-trips.
	packed := parse(t, p, "10248").Packed()
	assert.Len(t, packed, 4)
	back, err := p.UnpackPacked(packed)
	require.NoError(t, err)
	assert.Equal(t, "10248", back.Plain())
}

// The general store rules on the pictures the program computes with.
func TestGolden_StoreRules(t *testing.T) {
	t.Parallel()
	// Truncation on store: 12.349 into scale 2 is 12.34.
	v, err := store(domain.PicItems.Zero(), cobolnum.MustNum("12.349"))
	require.NoError(t, err)
	assert.Equal(t, "12.34", v.Plain())
	// ROUNDED is half away from zero.
	r, _ := domain.PicItems.Zero().Set(cobolnum.MustNum("12.345"), cobolnum.Rounded, false)
	assert.Equal(t, "12.35", r.Plain())
	r, _ = domain.PicItems.Zero().Set(cobolnum.MustNum("-12.345"), cobolnum.Rounded, false)
	assert.Equal(t, "-12.35", r.Plain())
	r, _ = domain.PicItems.Zero().Set(cobolnum.MustNum("12.344"), cobolnum.Rounded, false)
	assert.Equal(t, "12.34", r.Plain())
	// A negative value keeps its sign in a signed picture...
	v, err = store(domain.PicDue.Zero(), cobolnum.MustNum("-67.62"))
	require.NoError(t, err)
	assert.Equal(t, "-67.62", v.Plain())
	// ...and loses it in an unsigned one (WS-DISC-PCT, WS-CYMD).
	v, err = store(domain.PicDiscPct.Zero(), cobolnum.MustNum("-25"))
	require.NoError(t, err)
	assert.Equal(t, "25", v.Plain())
	// ON SIZE ERROR leaves the receiver unchanged.
	recv := parse(t, domain.PicDue, "1.00")
	kept, sizeErr := recv.Set(cobolnum.MustNum("1000000000.00"), cobolnum.Truncation, true)
	assert.True(t, sizeErr)
	assert.Equal(t, "1.00", kept.Plain())
	// Without ON SIZE ERROR (the source's case) the high-order digit is dropped.
	cut, sizeErr := recv.Set(cobolnum.MustNum("1000000000.00"), cobolnum.Truncation, false)
	assert.True(t, sizeErr)
	assert.Equal(t, "0.00", cut.Plain())
	// A zero divisor leaves the receiver unchanged.
	ctx := cobolnum.ILE.Stmt(cobolnum.Dmax([]cobolnum.Pic{domain.PicDue}, false, recv.Num()))
	kept, sizeErr = recv.Set(ctx.Div(recv.Num(), cobolnum.MustNum("0")), cobolnum.Truncation, false)
	assert.True(t, sizeErr)
	assert.Equal(t, "1.00", kept.Plain())
	_, _, sizeErr = cobolnum.Divide(cobolnum.MustNum("1000704"), cobolnum.MustNum("0"), domain.PicCYY)
	assert.True(t, sizeErr)
	// An invalid operand is never stored as zero.
	_, err = store(recv, cobolnum.Dec{}.Num())
	assert.ErrorIs(t, err, errInvalidNum)
}

// Each column picture read from Db2 at its limits.
func TestGolden_ColumnPictures(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		pic       cobolnum.Pic
		max, trnc string
		in        string
	}{
		{domain.PicLineNo, "999", "12", "12.9"},
		{domain.PicProductNo, "99999", "42", "42.5"},
		{domain.PicQty, "99999", "10", "10.99"},
		{domain.PicPrice, "9999999.99", "12.34", "12.349"},
		{domain.PicDisc, "9.99", "0.05", "0.059"},
		{domain.PicExt, "999999999.99", "12.34", "12.349"},
		{domain.PicFreight, "99999.99", "32.38", "32.389"},
		{domain.PicTotal, "999999999.99", "440.00", "440.009"},
		{domain.PicDate7, "9999999", "1000704", "1000704.9"},
	} {
		assert.Equal(t, c.max, parse(t, c.pic, c.max).Plain())
		assert.Equal(t, "-"+c.max, parse(t, c.pic, "-"+c.max).Plain())
		v, _, err := c.pic.ParseDec(c.in, cobolnum.Truncation)
		require.NoError(t, err)
		assert.Equal(t, c.trnc, v.Plain())
	}
}

// ADD OD-EXT TO WS-ITEMS.
func TestGolden_AddExtendedToItems(t *testing.T) {
	t.Parallel()
	items := domain.PicItems.Zero()
	var err error
	for _, ext := range []string{"168.00", "93.10", "174.00", "-0.01"} {
		items, err = addTo(items, parse(t, domain.PicExt, ext))
		require.NoError(t, err)
	}
	assert.Equal(t, "435.09", items.Plain())
	// S9(9)V99 overflows silently (no ON SIZE ERROR): the high-order digit is lost.
	items = parse(t, domain.PicItems, "999999999.99")
	items, err = addTo(items, parse(t, domain.PicExt, "0.02"))
	require.NoError(t, err)
	assert.Equal(t, "0.01", items.Plain())
}

// COMPUTE WS-DISC-PCT = OD-DISC * 100 (999, no ROUNDED).
func TestGolden_DiscountPercent(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"0.05": "5", "0.00": "0", "9.99": "999", "-0.25": "25", "0.10": "10"} {
		v, err := discountPercent(parse(t, domain.PicDisc, in))
		require.NoError(t, err)
		assert.Equal(t, want, v.Plain(), in)
	}
}

// COMPUTE WS-DUE = OH-TOTL + OH-FRT.
func TestGolden_TotalDue(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ total, freight, want string }{
		{"431.60", "32.38", "463.98"},
		{"-100.00", "32.38", "-67.62"},
		{"0.00", "0.00", "0.00"},
		{"999999999.99", "99999.99", "99999.98"}, // 1000099999.98: the tenth digit is dropped
		{"-999999999.99", "-99999.99", "-99999.98"},
	} {
		v, err := totalDue(parse(t, domain.PicTotal, c.total), parse(t, domain.PicFreight, c.freight))
		require.NoError(t, err)
		assert.Equal(t, c.want, v.Plain(), c.total)
	}
}

// COMPUTE WS-MDY6 = WS-T-MM * 10000 + WS-T-DD * 100 + WS-T-YY.
func TestGolden_TodayMDY(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		y, m, d int
		want    string
	}{
		{99, 12, 31, "123199"}, {0, 1, 1, "10100"}, {26, 11, 25, "112526"}, {0, 2, 29, "22900"},
	} {
		v, err := todayMDY(c.y, c.m, c.d)
		require.NoError(t, err)
		assert.Equal(t, c.want, v.Plain())
	}
}

// 7000-CYMD-TO-MDY: the two DIVIDE ... REMAINDER and COMPUTE WS-MDY6.
func TestGolden_CymdToMDY(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"0":        "0",
		"991231":   "123199", // 1999-12-31
		"1000101":  "10100",  // 2000-01-01
		"1991231":  "123199", // 2099-12-31
		"2000101":  "10100",  // 2100-01-01
		"9999999":  "999999",
		"-1000704": "70400", // MOVE to 9(7) keeps the absolute value
		"1":        "100",   // MMDD 0001 and YY 00
	} {
		v, err := cymdToMDY(parse(t, domain.PicDate7, in))
		require.NoError(t, err)
		assert.Equal(t, want, v.Plain(), in)
	}
}

// The edited pictures at their limits (edit-mask overflow drops the high-order digits).
func TestGolden_EditedPictures(t *testing.T) {
	t.Parallel()
	p, err := newPictures()
	require.NoError(t, err)
	for _, c := range []struct {
		pic  string
		dec  cobolnum.Dec
		want string
	}{
		{"ZZ9", parse(t, domain.PicPage, "999"), "999"},
		{"ZZ9", parse(t, domain.PicPage, "0"), "  0"},
		{"Z(6)9", parse(t, domain.PicOrderNo, "9999999"), "9999999"},
		{"Z(6)9", parse(t, domain.PicOrderNo, "-10248"), "  10248"},
		{"99/99/99", parse(t, domain.PicMDY6, "70400"), "07/04/00"},
		{"ZZ,ZZ9", parse(t, domain.PicQty, "99999"), "99,999"},
		{"ZZ,ZZ9", parse(t, domain.PicQty, "999"), "   999"},
		{"Z,ZZZ,ZZ9.99", parse(t, domain.PicPrice, "9999999.99"), "9,999,999.99"},
		{"Z,ZZZ,ZZ9.99", parse(t, domain.PicPrice, "0.05"), "        0.05"},
		{"ZZZ,ZZZ,ZZ9.99-", parse(t, domain.PicExt, "999999999.99"), "999,999,999.99 "},
		{"ZZZ,ZZZ,ZZ9.99-", parse(t, domain.PicExt, "-999999999.99"), "999,999,999.99-"},
		{"ZZZ,ZZZ,ZZ9.99-", parse(t, domain.PicExt, "-0.01"), "          0.01-"},
	} {
		var pic = map[string]func() (string, error){
			"ZZ9": func() (string, error) { return move(p.zz9, c.dec) }, "Z(6)9": func() (string, error) { return move(p.z6n9, c.dec) },
			"99/99/99": func() (string, error) { return move(p.mdy, c.dec) }, "ZZ,ZZ9": func() (string, error) { return move(p.qty, c.dec) },
			"Z,ZZZ,ZZ9.99":    func() (string, error) { return move(p.price, c.dec) },
			"ZZZ,ZZZ,ZZ9.99-": func() (string, error) { return move(p.amount, c.dec) },
		}[c.pic]
		got, err := pic()
		require.NoError(t, err)
		assert.Equal(t, c.want, got, c.pic+" "+c.dec.Plain())
	}
	// A value wider than the edit mask keeps its low-order digits (MOVE rule).
	wide, _ := p.qty.MoveNum(cobolnum.MustNum("123456"))
	assert.Equal(t, "23,456", wide)
}

func TestGolden_Alnum(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "AB   ", alnum("AB", 5))
	assert.Equal(t, "ABCDE", alnum("ABCDEFG", 5))
	assert.Equal(t, "ABCDE", alnum("ABCDE", 5))
	assert.Equal(t, "Zürch", alnum("Zürcher", 5))
	assert.Len(t, record("x"), domain.PrintRecordLength)
}
