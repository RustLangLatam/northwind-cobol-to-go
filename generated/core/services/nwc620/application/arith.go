package application

import (
	"errors"
	"fmt"

	"github.com/RustLangLatam/cobolnum-go"

	"milton_prism/core/services/nwc620/domain"
)

// Every statement is evaluated with the ILE COBOL rules (the source is a CBLLE member
// compiled for IBM i): one context per statement, dmax from the receivers and the
// non-divisor operands.

var errInvalidNum = errors.New("nwc620: an arithmetic operand or result has no value")

var (
	lit100   = cobolnum.MustNum("100")
	lit10000 = cobolnum.MustNum("10000")
)

// store sets n into recv with the COBOL store rule of a statement without ROUNDED and
// without ON SIZE ERROR: decimals truncated, high-order digits dropped.
func store(recv cobolnum.Dec, n cobolnum.Num) (cobolnum.Dec, error) {
	if n.Invalid() {
		return recv, errInvalidNum
	}
	out, _ := recv.Set(n, cobolnum.Truncation, false)
	if !out.Valid() {
		return recv, errInvalidNum
	}
	return out, nil
}

// addTo is ADD a TO acc (no ROUNDED, no ON SIZE ERROR).
func addTo(acc, a cobolnum.Dec) (cobolnum.Dec, error) {
	ctx := cobolnum.ILE.Stmt(cobolnum.Dmax([]cobolnum.Pic{acc.Pic()}, false, acc.Num(), a.Num()))
	return store(acc, ctx.Add(acc.Num(), a.Num()))
}

// addOne is ADD 1 TO counter.
func addOne(counter cobolnum.Dec) (cobolnum.Dec, error) {
	one := cobolnum.MustNum("1")
	ctx := cobolnum.ILE.Stmt(cobolnum.Dmax([]cobolnum.Pic{counter.Pic()}, false, counter.Num(), one))
	return store(counter, ctx.Add(counter.Num(), one))
}

// discountPercent is COMPUTE WS-DISC-PCT = OD-DISC * 100. WS-DISC-PCT is PIC 999,
// unsigned: a negative discount is stored as its absolute value.
func discountPercent(disc cobolnum.Dec) (cobolnum.Dec, error) {
	ctx := cobolnum.ILE.Stmt(cobolnum.Dmax([]cobolnum.Pic{domain.PicDiscPct}, false, disc.Num(), lit100))
	return store(domain.PicDiscPct.Zero(), ctx.Mul(disc.Num(), lit100))
}

// totalDue is COMPUTE WS-DUE = OH-TOTL + OH-FRT (the 1991 rule: the order total is
// the items only, so the freight is added).
func totalDue(total, freight cobolnum.Dec) (cobolnum.Dec, error) {
	ctx := cobolnum.ILE.Stmt(cobolnum.Dmax([]cobolnum.Pic{domain.PicDue}, false, total.Num(), freight.Num()))
	return store(domain.PicDue.Zero(), ctx.Add(total.Num(), freight.Num()))
}

// todayMDY is ACCEPT WS-TODAY FROM DATE (YYMMDD) followed by
// COMPUTE WS-MDY6 = WS-T-MM * 10000 + WS-T-DD * 100 + WS-T-YY.
func todayMDY(year, month, day int) (cobolnum.Dec, error) {
	yy, _ := domain.PicTwo.Store(cobolnum.FromInt64(int64(year)), cobolnum.Truncation) // YY of YYMMDD
	mm, _ := domain.PicTwo.Store(cobolnum.FromInt64(int64(month)), cobolnum.Truncation)
	dd, _ := domain.PicTwo.Store(cobolnum.FromInt64(int64(day)), cobolnum.Truncation)
	ctx := cobolnum.ILE.Stmt(cobolnum.Dmax([]cobolnum.Pic{domain.PicMDY6}, false, mm.Num(), lit10000, dd.Num(), lit100, yy.Num()))
	expr := ctx.Add(ctx.Add(ctx.Mul(mm.Num(), lit10000), ctx.Mul(dd.Num(), lit100)), yy.Num())
	return store(domain.PicMDY6.Zero(), expr)
}

// cymdToMDY is MOVE OH-ODAT TO WS-CYMD and paragraph 7000-CYMD-TO-MDY (copied from
// NWC610): CYYMMDD to MMDDYY, zero for a zero date. The century digit is dropped.
func cymdToMDY(cymdSource cobolnum.Dec) (cobolnum.Dec, error) {
	mdy := domain.PicMDY6.Zero()
	cymd, err := store(domain.PicCYMD.Zero(), cymdSource.Num()) // 9(7): unsigned, absolute value
	if err != nil {
		return mdy, err
	}
	if cymd.Num().Sign() == 0 {
		return mdy, nil
	}
	// DIVIDE WS-CYMD BY 10000 GIVING WS-CYY REMAINDER WS-MMDD.
	cyy, rem, sizeErr := cobolnum.Divide(cymd.Num(), lit10000, domain.PicCYY)
	if sizeErr {
		return mdy, fmt.Errorf("nwc620: DIVIDE WS-CYMD BY 10000 raised a size error")
	}
	mmdd, err := store(domain.PicMMDD.Zero(), rem)
	if err != nil {
		return mdy, err
	}
	// DIVIDE WS-CYY BY 100 GIVING WS-CC REMAINDER WS-YY.
	_, rem, sizeErr = cobolnum.Divide(cyy.Num(), lit100, domain.PicCC)
	if sizeErr {
		return mdy, fmt.Errorf("nwc620: DIVIDE WS-CYY BY 100 raised a size error")
	}
	yy, err := store(domain.PicYY.Zero(), rem)
	if err != nil {
		return mdy, err
	}
	// COMPUTE WS-MDY6 = WS-MMDD * 100 + WS-YY.
	ctx := cobolnum.ILE.Stmt(cobolnum.Dmax([]cobolnum.Pic{domain.PicMDY6}, false, mmdd.Num(), lit100, yy.Num()))
	return store(mdy, ctx.Add(ctx.Mul(mmdd.Num(), lit100), yy.Num()))
}
