package application_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RustLangLatam/cobolnum-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"milton_prism/core/services/nwc620/application"
	"milton_prism/core/services/nwc620/domain"
	"milton_prism/core/services/nwc620/mocks"
	"milton_prism/core/shared/domainerr"
)

var dallas = mustLoc("America/Chicago")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func d(p cobolnum.Pic, s string) cobolnum.Dec {
	v, _, err := p.ParseDec(s, cobolnum.Truncation)
	if err != nil {
		panic(err)
	}
	return v
}

func header() domain.OrderHeader {
	return domain.OrderHeader{
		OrderNo: d(domain.PicOrderNo, "10248"), Customer: "VINET", OrderDate: d(domain.PicDate7, "1000704"),
		ShipVia: "3", Freight: d(domain.PicFreight, "32.38"), ShipName: "Vins et alcools Chevalier",
		ShipAddress: "59 rue de l'Abbaye", ShipCity: "Reims", ShipCountry: "France", Total: d(domain.PicTotal, "431.60"),
	}
}

func line(no, prd, qty, price, disc, ext string) domain.OrderLine {
	return domain.OrderLine{LineNo: d(domain.PicLineNo, no), ProductNo: d(domain.PicProductNo, prd),
		Qty: d(domain.PicQty, qty), Price: d(domain.PicPrice, price), Disc: d(domain.PicDisc, disc),
		Extended: d(domain.PicExt, ext)}
}

type fixture struct {
	repo    *mocks.MockInvoiceRepository
	printer *mocks.MockInvoicePrinter
	metrics *mocks.MockMetrics
	svc     *application.Service
	printed *domain.SpoolFile
}

func newFixture(t *testing.T, now time.Time) *fixture {
	t.Helper()
	f := &fixture{repo: &mocks.MockInvoiceRepository{}, printer: &mocks.MockInvoicePrinter{}, metrics: &mocks.MockMetrics{}}
	svc, err := application.NewService(f.repo, f.printer, mocks.FixedClock{T: now}, f.metrics)
	require.NoError(t, err)
	f.svc = svc
	return f
}

// standard expects a found order with customer, shipper and every product on file.
func (f *fixture) standard(h domain.OrderHeader, lines []domain.OrderLine) {
	f.repo.On("GetOrderHeader", mock.Anything, mock.Anything).Return(h, true, nil)
	f.repo.On("GetCustomer", mock.Anything, h.Customer).Return(domain.Customer{Name: "Vins et alcools Chevalier",
		Address: "59 rue de l'Abbaye", City: "Reims", Country: "France"}, true, nil)
	f.repo.On("GetCodeDescription", mock.Anything, "SHP", "3         ").Return("Federal Shipping", true, nil)
	f.repo.On("ListOrderLines", mock.Anything, mock.Anything).Return(lines, nil)
	f.repo.On("GetProductName", mock.Anything, mock.Anything).Return("Queso Cabrales", true, nil)
	f.expectPrint()
}

func (f *fixture) expectPrint() {
	f.printer.On("Print", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		sf := args.Get(1).(domain.SpoolFile)
		f.printed = &sf
	}).Return(nil)
	f.metrics.On("InvoicePrinted").Return()
}

func (f *fixture) lines(t *testing.T) []string {
	t.Helper()
	require.NotNil(t, f.printed)
	out := make([]string, len(f.printed.Records))
	for i, r := range f.printed.Records {
		require.Len(t, r.Line, domain.PrintRecordLength, "record %d keeps its 132 bytes", i)
		out[i] = strings.TrimRight(r.Line, " ")
	}
	return out
}

var july = time.Date(2026, time.November, 25, 9, 30, 0, 0, dallas)

func TestPrintInvoice_OK(t *testing.T) {
	t.Parallel()
	f := newFixture(t, july)
	f.standard(header(), []domain.OrderLine{
		line("1", "11", "12", "14.00", "0.00", "168.00"),
		line("2", "42", "10", "9.80", "0.05", "93.10"),
		line("3", "72", "5", "34.80", "0.00", "174.00"),
	})
	res, err := f.svc.PrintInvoice(context.Background(), 10248)
	require.NoError(t, err)
	assert.Equal(t, application.Result{Status: "0", OrderNo: 10248}, res)

	require.NotNil(t, f.printed)
	assert.Equal(t, "NWINVOICE", f.printed.Name)
	assert.Equal(t, "INVOICE", f.printed.FormType)
	want := []string{
		"NORTHWIND TRADERS - DALLAS" + strings.Repeat(" ", 14+60) + "INVOICE   PAGE     1",
		"2101 COMMERCE ST.  DALLAS TX 75201" + strings.Repeat(" ", 6+60) + "DATE  11/25/26",
		"ORDER NO.   10248     ORDERED   07/04/00     SHIP VIA  Federal Shipping",
		"SOLD TO   VINET Vins et alcools Chevalier                   SHIP TO   Vins et alcools Chevalier",
		"                59 rue de l'Abbaye                                    59 rue de l'Abbaye",
		"                Reims           France                                Reims           France",
		"LN  PROD#  DESCRIPTION                                QTY     UNIT PRICE  DISC      EXTENDED",
		"  1    11  Queso Cabrales                               12         14.00    0%         168.00",
		"  2    42  Queso Cabrales                               10          9.80    5%          93.10",
		"  3    72  Queso Cabrales                                5         34.80    0%         174.00",
		strings.Repeat(" ", 64) + "ITEMS                  435.10",
		strings.Repeat(" ", 64) + "FREIGHT                 32.38",
		strings.Repeat(" ", 64) + "TOTAL DUE              463.98",
	}
	assert.Equal(t, want, f.lines(t))
	adv := make([]domain.Advance, len(f.printed.Records))
	for i, r := range f.printed.Records {
		adv[i] = r.Advance
	}
	assert.Equal(t, []domain.Advance{{Page: true}, {Lines: 1}, {Lines: 2}, {Lines: 2}, {Lines: 1}, {Lines: 1}, {Lines: 2},
		{Lines: 1}, {Lines: 1}, {Lines: 1}, {Lines: 2}, {Lines: 1}, {Lines: 2}}, adv)
	f.metrics.AssertCalled(t, "InvoicePrinted")
}

// The first WRITE is AFTER ADVANCING PAGE: the spooled file starts with a page eject.
func TestPrintInvoice_LeadingPageEject(t *testing.T) {
	t.Parallel()
	f := newFixture(t, july)
	f.standard(header(), nil)
	_, err := f.svc.PrintInvoice(context.Background(), 10248)
	require.NoError(t, err)
	require.NotEmpty(t, f.printed.Records)
	assert.Equal(t, domain.Advance{Page: true}, f.printed.Records[0].Advance)
}

// An order with no lines still prints the headings and the totals.
func TestPrintInvoice_NoLines(t *testing.T) {
	t.Parallel()
	f := newFixture(t, july)
	f.standard(header(), nil)
	_, err := f.svc.PrintInvoice(context.Background(), 10248)
	require.NoError(t, err)
	got := f.lines(t)
	require.Len(t, got, 10)
	assert.Equal(t, strings.Repeat(" ", 64)+"ITEMS                    0.00", got[7])
	assert.Equal(t, strings.Repeat(" ", 64)+"TOTAL DUE              463.98", got[9])
	f.repo.AssertNotCalled(t, "GetProductName", mock.Anything, mock.Anything)
}

func TestPrintInvoice_OrderNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t, july)
	f.repo.On("GetOrderHeader", mock.Anything, mock.Anything).Return(domain.OrderHeader{}, false, nil)
	f.metrics.On("OrderNotFound").Return()
	_, err := f.svc.PrintInvoice(context.Background(), 10250)
	require.Error(t, err)
	assert.True(t, domainerr.Is(err, domainerr.NotFound))
	assert.Contains(t, err.Error(), domain.ErrCodeOrderNotFound)
	// No WRITE: the printer file was opened and closed empty, so nothing is spooled.
	f.printer.AssertNotCalled(t, "Print", mock.Anything, mock.Anything)
	f.repo.AssertNotCalled(t, "GetCustomer", mock.Anything, mock.Anything)
	f.metrics.AssertCalled(t, "OrderNotFound")
}

func TestPrintInvoice_ValidationFailures(t *testing.T) {
	t.Parallel()
	for _, n := range []int64{0, -1, -9999999, 10000000} {
		f := newFixture(t, july)
		_, err := f.svc.PrintInvoice(context.Background(), n)
		require.Error(t, err)
		assert.True(t, domainerr.Is(err, domainerr.InvalidArgument), "order %d", n)
		_, msg := domainerr.KindOf(err)
		assert.Contains(t, msg, "1 to 9999999")
		f.repo.AssertNotCalled(t, "GetOrderHeader", mock.Anything, mock.Anything)
	}
	for _, n := range []int64{1, 9999999} {
		f := newFixture(t, july)
		h := header()
		h.OrderNo = d(domain.PicOrderNo, "9999999")
		f.standard(h, nil)
		res, err := f.svc.PrintInvoice(context.Background(), n)
		require.NoError(t, err, "order %d", n)
		assert.Equal(t, n, res.OrderNo)
		assert.Contains(t, f.lines(t)[2], "ORDER NO. 9999999")
	}
}

func TestPrintInvoice_CustomerAndShipperNotOnFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t, july)
	h := header()
	f.repo.On("GetOrderHeader", mock.Anything, mock.Anything).Return(h, true, nil)
	f.repo.On("GetCustomer", mock.Anything, "VINET").Return(domain.Customer{}, false, nil)
	f.repo.On("GetCodeDescription", mock.Anything, "SHP", "3         ").Return("", false, nil)
	f.repo.On("ListOrderLines", mock.Anything, mock.Anything).Return([]domain.OrderLine(nil), nil)
	f.expectPrint()
	_, err := f.svc.PrintInvoice(context.Background(), 10248)
	require.NoError(t, err)
	got := f.lines(t)
	assert.Equal(t, "ORDER NO.   10248     ORDERED   07/04/00     SHIP VIA", got[2])
	assert.Equal(t, "SOLD TO   VINET *** CUSTOMER NOT ON FILE ***                SHIP TO   Vins et alcools Chevalier", got[3])
	assert.Equal(t, strings.Repeat(" ", 70)+"59 rue de l'Abbaye", got[4])
	assert.Equal(t, strings.Repeat(" ", 70)+"Reims           France", got[5])
}

// A product on several lines is read once; one not on file prints *NOT ON FILE.
func TestPrintInvoice_ProductLookupMemoizedAndNotOnFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t, july)
	h := header()
	f.repo.On("GetOrderHeader", mock.Anything, mock.Anything).Return(h, true, nil)
	f.repo.On("GetCustomer", mock.Anything, "VINET").Return(domain.Customer{Name: "X"}, true, nil)
	f.repo.On("GetCodeDescription", mock.Anything, "SHP", "3         ").Return("Federal Shipping", true, nil)
	f.repo.On("ListOrderLines", mock.Anything, mock.Anything).Return([]domain.OrderLine{
		line("1", "11", "1", "1.00", "0", "1.00"), line("2", "99", "1", "1.00", "0", "1.00"),
		line("3", "11", "1", "1.00", "0", "1.00"), line("4", "99", "1", "1.00", "0", "1.00"),
	}, nil)
	f.repo.On("GetProductName", mock.Anything, d(domain.PicProductNo, "11")).Return("Queso Cabrales", true, nil).Once()
	f.repo.On("GetProductName", mock.Anything, d(domain.PicProductNo, "99")).Return("", false, nil).Once()
	f.expectPrint()
	_, err := f.svc.PrintInvoice(context.Background(), 10248)
	require.NoError(t, err)
	got := f.lines(t)
	assert.Contains(t, got[7], "Queso Cabrales")
	assert.Contains(t, got[8], "*NOT ON FILE ")
	assert.Contains(t, got[10], "*NOT ON FILE ")
	f.repo.AssertNumberOfCalls(t, "GetProductName", 2)
}

func manyLines(n int) []domain.OrderLine {
	out := make([]domain.OrderLine, n)
	for i := range out {
		out[i] = line(strconv.Itoa(i+1), "11", "1", "1.00", "0", "1.00")
	}
	return out
}

func pageEjects(f *fixture) int {
	n := 0
	for _, r := range f.printed.Records {
		if r.Advance.Page {
			n++
		}
	}
	return n
}

// The detail lines overflow when WS-LINE-CNT > 55: 45 lines fit on page 1 (44 and
// 45 lines leave only the totals for page 2, WS-LINE-CNT being above 50), the 46th
// line starts page 2 under the full headings.
func TestPrintInvoice_DetailPageBreak(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		lines     int
		firstOnP2 string
	}{{44, "ITEMS"}, {45, "ITEMS"}, {46, " 46 "}} {
		f := newFixture(t, july)
		f.standard(header(), manyLines(c.lines))
		_, err := f.svc.PrintInvoice(context.Background(), 10248)
		require.NoError(t, err)
		assert.Equal(t, 2, pageEjects(f), "%d lines", c.lines)
		onPage1 := min(c.lines, 45)
		got := f.lines(t)
		assert.Equal(t, domain.Advance{Page: true}, f.printed.Records[7+onPage1].Advance, "%d lines", c.lines)
		assert.True(t, strings.HasSuffix(got[7+onPage1], "PAGE     2"), "%d lines", c.lines)
		assert.Contains(t, got[7+onPage1+7], c.firstOnP2, "%d lines", c.lines)
		assert.True(t, strings.HasPrefix(got[7+onPage1-1], fmt.Sprintf("%3d ", onPage1)), "%d lines", c.lines)
	}
}

// The totals need WS-LINE-CNT <= 50: 39 detail lines leave it at 50, 40 at 51, which
// moves the totals to a new page.
func TestPrintInvoice_TotalsThreshold(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ lines, pages int }{{39, 1}, {40, 2}, {41, 2}} {
		f := newFixture(t, july)
		f.standard(header(), manyLines(c.lines))
		_, err := f.svc.PrintInvoice(context.Background(), 10248)
		require.NoError(t, err)
		assert.Equal(t, c.pages, pageEjects(f), "%d lines", c.lines)
		recs := f.printed.Records
		if c.pages == 2 {
			// the headings of page 2 come right before the ITEMS line
			assert.Equal(t, domain.Advance{Page: true}, recs[len(recs)-3-7].Advance)
		}
	}
}

func TestPrintInvoice_OrderDateAcrossCenturies(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ cymd, want string }{
		{"991231", "12/31/99"},   // 1999-12-31: century digit 0
		{"1000101", "01/01/00"},  // 2000-01-01: century digit 1
		{"1991231", "12/31/99"},  // 2099-12-31
		{"2000101", "01/01/00"},  // 2100-01-01: century digit 2, dropped
		{"0", "00/00/00"},        // no date
		{"-1000704", "07/04/00"}, // MOVE to the unsigned 9(7) drops the sign
	} {
		f := newFixture(t, july)
		h := header()
		h.OrderDate = d(domain.PicDate7, c.cymd)
		f.standard(h, nil)
		_, err := f.svc.PrintInvoice(context.Background(), 10248)
		require.NoError(t, err)
		assert.Equal(t, "ORDER NO.   10248     ORDERED   "+c.want+"     SHIP VIA  Federal Shipping", f.lines(t)[2], c.cymd)
	}
}

func TestPrintInvoice_RunDateAcrossCenturies(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		now  time.Time
		want string
	}{
		{time.Date(1999, 12, 31, 23, 59, 0, 0, dallas), "12/31/99"},
		{time.Date(2000, 1, 1, 0, 0, 0, 0, dallas), "01/01/00"},
		{time.Date(2099, 12, 31, 12, 0, 0, 0, dallas), "12/31/99"},
		{time.Date(2100, 1, 1, 12, 0, 0, 0, dallas), "01/01/00"},
	} {
		f := newFixture(t, c.now)
		f.standard(header(), nil)
		_, err := f.svc.PrintInvoice(context.Background(), 10248)
		require.NoError(t, err)
		assert.True(t, strings.HasSuffix(f.lines(t)[1], "DATE  "+c.want), c.want)
	}
}

// The edited amounts at the limits of their pictures, and the truncation of the
// accumulators on overflow (no ON SIZE ERROR in the source).
func TestPrintInvoice_AmountLimitsAndOverflow(t *testing.T) {
	t.Parallel()
	f := newFixture(t, july)
	h := header()
	h.Total = d(domain.PicTotal, "999999999.99")
	h.Freight = d(domain.PicFreight, "99999.99")
	f.standard(h, []domain.OrderLine{
		line("999", "99999", "99999", "9999999.99", "9.99", "999999999.99"),
		line("2", "1", "1", "0.01", "-0.25", "999999999.99"),
		line("3", "1", "1", "0.01", "0.01", "-0.01"),
	})
	_, err := f.svc.PrintInvoice(context.Background(), 10248)
	require.NoError(t, err)
	got := f.lines(t)
	assert.Equal(t, "999 99999  Queso Cabrales                           99,999  9,999,999.99  999% 999,999,999.99", got[7])
	assert.Equal(t, "  2     1  Queso Cabrales                                1          0.01   25% 999,999,999.99", got[8])
	assert.Equal(t, "  3     1  Queso Cabrales                                1          0.01    1%           0.01-", got[9])
	// WS-ITEMS: 999999999.99 + 999999999.99 = 1999999999.98, high-order digit dropped,
	// then - 0.01.
	assert.Equal(t, strings.Repeat(" ", 64)+"ITEMS          999,999,999.97", got[10])
	assert.Equal(t, strings.Repeat(" ", 64)+"FREIGHT             99,999.99", got[11])
	// WS-DUE: 999999999.99 + 99999.99 = 1000099999.98 into S9(9)V99 keeps 000099999.98.
	assert.Equal(t, strings.Repeat(" ", 64)+"TOTAL DUE           99,999.98", got[12])
}

func TestPrintInvoice_NegativeTotalDue(t *testing.T) {
	t.Parallel()
	f := newFixture(t, july)
	h := header()
	h.Total = d(domain.PicTotal, "-100.00")
	h.Freight = d(domain.PicFreight, "32.38")
	f.standard(h, nil)
	_, err := f.svc.PrintInvoice(context.Background(), 10248)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat(" ", 64)+"TOTAL DUE               67.62-", f.lines(t)[9])
}

func TestPrintInvoice_RepositoryErrors(t *testing.T) {
	t.Parallel()
	boom := domainerr.Wrap(domainerr.Unavailable, "down", errors.New("db"))
	steps := []string{"GetOrderHeader", "GetCustomer", "GetCodeDescription", "ListOrderLines", "GetProductName"}
	for failAt := range steps {
		f := newFixture(t, july)
		h := header()
		ret := func(i int, ok ...any) []any {
			if i == failAt {
				return append(ok[:len(ok)-1:len(ok)-1], boom)
			}
			return ok
		}
		f.repo.On("GetOrderHeader", mock.Anything, mock.Anything).Return(ret(0, h, true, nil)...)
		f.repo.On("GetCustomer", mock.Anything, mock.Anything).Return(ret(1, domain.Customer{}, true, nil)...)
		f.repo.On("GetCodeDescription", mock.Anything, mock.Anything, mock.Anything).Return(ret(2, "", true, nil)...)
		f.repo.On("ListOrderLines", mock.Anything, mock.Anything).Return(ret(3, []domain.OrderLine{line("1", "1", "1", "1", "0", "1")}, nil)...)
		f.repo.On("GetProductName", mock.Anything, mock.Anything).Return(ret(4, "P", true, nil)...)
		_, err := f.svc.PrintInvoice(context.Background(), 10248)
		require.Error(t, err, steps[failAt])
		assert.True(t, domainerr.Is(err, domainerr.Unavailable), steps[failAt])
		f.printer.AssertNotCalled(t, "Print", mock.Anything, mock.Anything)
	}
}

func TestPrintInvoice_PrinterError(t *testing.T) {
	t.Parallel()
	f := newFixture(t, july)
	h := header()
	f.repo.On("GetOrderHeader", mock.Anything, mock.Anything).Return(h, true, nil)
	f.repo.On("GetCustomer", mock.Anything, mock.Anything).Return(domain.Customer{}, true, nil)
	f.repo.On("GetCodeDescription", mock.Anything, mock.Anything, mock.Anything).Return("", true, nil)
	f.repo.On("ListOrderLines", mock.Anything, mock.Anything).Return([]domain.OrderLine(nil), nil)
	f.printer.On("Print", mock.Anything, mock.Anything).Return(errors.New("disk full"))
	_, err := f.svc.PrintInvoice(context.Background(), 10248)
	require.Error(t, err)
	assert.True(t, domainerr.Is(err, domainerr.Unavailable))
	assert.Contains(t, err.Error(), domain.ErrCodeOutputUnavailable)
	f.metrics.AssertNotCalled(t, "InvoicePrinted")
}

func TestPrintInvoice_CanceledBeforePrint(t *testing.T) {
	t.Parallel()
	f := newFixture(t, july)
	h := header()
	ctx, cancel := context.WithCancel(context.Background())
	f.repo.On("GetOrderHeader", mock.Anything, mock.Anything).Return(h, true, nil)
	f.repo.On("GetCustomer", mock.Anything, mock.Anything).Return(domain.Customer{}, true, nil)
	f.repo.On("GetCodeDescription", mock.Anything, mock.Anything, mock.Anything).Return("", true, nil)
	f.repo.On("ListOrderLines", mock.Anything, mock.Anything).Run(func(mock.Arguments) { cancel() }).
		Return([]domain.OrderLine(nil), nil)
	_, err := f.svc.PrintInvoice(ctx, 10248)
	assert.ErrorIs(t, err, context.Canceled)
	f.printer.AssertNotCalled(t, "Print", mock.Anything, mock.Anything)
}
