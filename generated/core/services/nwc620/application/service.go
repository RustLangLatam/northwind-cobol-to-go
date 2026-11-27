// Package application is the NWC620 use case: print one invoice.
package application

import (
	"context"
	"fmt"

	"github.com/RustLangLatam/cobolnum-go"
	"github.com/RustLangLatam/cobolnum-go/edit"

	"milton_prism/core/services/nwc620/domain"
	"milton_prism/core/services/nwc620/ports"
)

// Thresholds of WS-LINE-CNT that force the page headings.
var (
	detailOverflow = cobolnum.MustNum("55") // 0100-READ: IF WS-LINE-CNT > 55
	totalsOverflow = cobolnum.MustNum("50") // 0500-TOTALS: IF WS-LINE-CNT > 50
)

// Service prints invoices.
type Service struct {
	repo    ports.InvoiceRepository
	printer ports.InvoicePrinter
	clock   ports.Clock
	metrics ports.Metrics
	pics    pictures
}

// NewService builds the use case. The edited pictures are parsed once here.
func NewService(repo ports.InvoiceRepository, printer ports.InvoicePrinter, clock ports.Clock, metrics ports.Metrics) (*Service, error) {
	pics, err := newPictures()
	if err != nil {
		return nil, err
	}
	return &Service{repo: repo, printer: printer, clock: clock, metrics: metrics, pics: pics}, nil
}

// Result is what the caller gets back: the status and LK-ORD-NO, which NWC620 does
// not change.
type Result struct {
	Status  string
	OrderNo int64
}

// PrintInvoice is NWC620 called with LK-ORD-NO = orderNo.
//
// PORT-NOTE: [A missing order is reported as NotFound instead of ending silently] The
// source ends at 9000-END-PGM with no spooled file and a normal return; the port
// writes no output either and also tells the caller with NotFound.
//
// PORT-NOTE: [Errors reach the caller instead of being swallowed by the CL wrapper]
// NWC620C monitors CPF0000, CEE0000 and LNR0000 after the CALL, so a failure of
// NWC620 was invisible to its caller; this RPC is NWC620 itself and reports database
// and output failures with their gRPC code. Whether ILE COBOL raises an escape message
// for a non-INVALID-KEY file error with a FILE STATUS clause and no USE procedure is
// unverified.
func (s *Service) PrintInvoice(ctx context.Context, orderNo int64) (Result, error) {
	if orderNo < domain.MinOrderNo || orderNo > domain.MaxOrderNo {
		return Result{}, domain.ErrOrderNoOutOfRange()
	}
	// In range, so LK-ORD-NO S9(7) holds it without a size error.
	ord, _ := domain.PicOrderNo.Store(cobolnum.FromInt64(orderNo), cobolnum.Truncation)

	// ACCEPT WS-TODAY FROM DATE reads the source system's local date.
	now := s.clock.Now()
	today, err := todayMDY(now.Year()%100, int(now.Month()), now.Day())
	if err != nil {
		return Result{}, domain.ErrInternal(err)
	}

	hdr, found, err := s.repo.GetOrderHeader(ctx, ord)
	if err != nil {
		return Result{}, err
	}
	if !found { // READ ORDHDR INVALID KEY GO TO 9000-END-PGM: no record is written
		s.metrics.OrderNotFound()
		return Result{}, domain.ErrOrderNotFound()
	}

	cust, found, err := s.repo.GetCustomer(ctx, hdr.Customer)
	if err != nil {
		return Result{}, err
	}
	if !found { // MOVE SPACES TO CUSMST-REC, then the not-on-file text into CM-NAME
		cust = domain.Customer{Name: domain.CustomerNotOnFile}
	}

	shipVia, found, err := s.repo.GetCodeDescription(ctx, domain.CodeTypeShipper, alnum(hdr.ShipVia, domain.CodeLength))
	if err != nil {
		return Result{}, err
	}
	if !found { // MOVE SPACES TO CT-DESC
		shipVia = ""
	}

	// PORT-NOTE: [The order lines are read in one statement instead of a READ NEXT loop]
	// START ORDDTL KEY NOT LESS THAN (order, 0) and READ NEXT until the order changes
	// select the lines with ODLIN# >= 0 in key order, which is the WHERE and ORDER BY of
	// the read. The files are opened for input without commitment control, so the
	// source also saw lines committed by other jobs while it read; a line another job
	// writes during the one statement may or may not be in it (unverified either way).
	lines, err := s.repo.ListOrderLines(ctx, ord)
	if err != nil {
		return Result{}, err
	}

	r := &run{svc: s, hdr: hdr, cust: cust, shipVia: shipVia, today: today,
		lineCnt: mustStore(domain.PicLineCnt, "99"), page: domain.PicPage.Zero(), items: domain.PicItems.Zero(),
		products: map[string]string{}}
	if err := r.print(ctx, lines); err != nil {
		return Result{}, err
	}

	// A WRITE is always reached once the order exists (the totals), so the spooled file
	// is never empty here.
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("nwc620: before printing: %w", err)
	}
	if err := s.printer.Print(ctx, domain.SpoolFile{Name: domain.SpoolFileName, FormType: domain.FormType,
		OrderNo: hdr.OrderNo, Records: r.records}); err != nil {
		return Result{}, domain.ErrOutputUnavailable(err)
	}
	s.metrics.InvoicePrinted()
	return Result{Status: domain.StatusOK, OrderNo: orderNo}, nil
}

func mustStore(p cobolnum.Pic, v string) cobolnum.Dec {
	d, _ := p.Store(cobolnum.MustNum(v), cobolnum.Truncation)
	return d
}

// run is the working storage of one call.
type run struct {
	svc      *Service
	hdr      domain.OrderHeader
	cust     domain.Customer
	shipVia  string
	today    cobolnum.Dec // WS-MDY6 of the run date (HD2-DATE)
	lineCnt  cobolnum.Dec // WS-LINE-CNT
	page     cobolnum.Dec // WS-PAGE
	items    cobolnum.Dec // WS-ITEMS
	products map[string]string
	records  []domain.PrintRecord
}

func (r *run) write(adv domain.Advance, line string) {
	r.records = append(r.records, domain.PrintRecord{Advance: adv, Line: record(line)})
}

func advance(n int) domain.Advance { return domain.Advance{Lines: n} }

// print is 0100-READ through 0500-TOTALS.
func (r *run) print(ctx context.Context, lines []domain.OrderLine) error {
	p := r.svc.pics
	for _, ln := range lines {
		if r.lineCnt.Num().Cmp(detailOverflow) > 0 {
			if err := r.headings(); err != nil {
				return err
			}
		}
		name, err := r.productName(ctx, ln.ProductNo)
		if err != nil {
			return err
		}
		pct, err := discountPercent(ln.Disc)
		if err != nil {
			return domain.ErrInternal(err)
		}
		// EXTENDED PRICE AS STORED ON THE LINE (NWC610 WORKS IT OUT).
		if r.items, err = addTo(r.items, ln.Extended); err != nil {
			return domain.ErrInternal(err)
		}
		dl1, err := detailLine(p, ln, name, pct)
		if err != nil {
			return domain.ErrInternal(err)
		}
		r.write(advance(1), dl1)
		if r.lineCnt, err = addOne(r.lineCnt); err != nil {
			return domain.ErrInternal(err)
		}
	}

	// 0500-TOTALS.
	if r.lineCnt.Num().Cmp(totalsOverflow) > 0 {
		if err := r.headings(); err != nil {
			return err
		}
	}
	due, err := totalDue(r.hdr.Total, r.hdr.Freight)
	if err != nil {
		return domain.ErrInternal(err)
	}
	for _, t := range []struct {
		adv  int
		text string
		amt  cobolnum.Dec
	}{{2, tl1Items, r.items}, {1, tl1Freight, r.hdr.Freight}, {2, tl1Due, due}} {
		amt, err := move(p.amount, t.amt)
		if err != nil {
			return domain.ErrInternal(err)
		}
		r.write(advance(t.adv), blanks(64)+alnum(t.text, 15)+amt)
	}
	return nil
}

// headings is 1000-HEADINGS: the whole header is repeated on each page.
func (r *run) headings() error {
	p := r.svc.pics
	var err error
	if r.page, err = addOne(r.page); err != nil {
		return domain.ErrInternal(err)
	}
	page, err := move(p.zz9, r.page)
	if err != nil {
		return domain.ErrInternal(err)
	}
	today, err := move(p.mdy, r.today)
	if err != nil {
		return domain.ErrInternal(err)
	}
	ord, err := move(p.z6n9, r.hdr.OrderNo)
	if err != nil {
		return domain.ErrInternal(err)
	}
	odat, err := cymdToMDY(r.hdr.OrderDate)
	if err != nil {
		return domain.ErrInternal(err)
	}
	odatText, err := move(p.mdy, odat)
	if err != nil {
		return domain.ErrInternal(err)
	}
	r.write(domain.Advance{Page: true}, alnum(hd1Company, 40)+blanks(60)+alnum(hd1Invoice, 8)+alnum(hd1Page, 9)+page)
	r.write(advance(1), alnum(hd2Address, 40)+blanks(60)+alnum(hd2Date, 6)+today)
	r.write(advance(2), alnum(hd3OrderNo, 10)+ord+blanks(5)+alnum(hd3Ordered, 10)+odatText+blanks(5)+
		alnum(hd3ShipVia, 10)+alnum(r.shipVia, 30))
	r.write(advance(2), alnum(hd4SoldTo, 10)+alnum(r.hdr.Customer, 6)+alnum(r.cust.Name, 40)+blanks(4)+
		alnum(hd4ShipTo, 10)+alnum(r.hdr.ShipName, 40))
	r.write(advance(1), blanks(16)+alnum(r.cust.Address, 40)+blanks(14)+alnum(r.hdr.ShipAddress, 40))
	r.write(advance(1), blanks(16)+alnum(r.cust.City, 15)+" "+alnum(r.cust.Country, 24)+blanks(14)+
		alnum(r.hdr.ShipCity, 15)+" "+alnum(r.hdr.ShipCountry, 15))
	r.write(advance(2), alnum(hd7Left, 50)+alnum(hd7Right, 50))
	r.lineCnt = mustStore(domain.PicLineCnt, "11")
	return nil
}

// productName is READ PRDMST, memoized per call: a product on several lines is read
// once. A product not on file prints "*NOT ON FILE".
func (r *run) productName(ctx context.Context, productNo cobolnum.Dec) (string, error) {
	key := productNo.Plain()
	if name, ok := r.products[key]; ok {
		return name, nil
	}
	name, found, err := r.svc.repo.GetProductName(ctx, productNo)
	if err != nil {
		return "", err
	}
	if !found {
		name = domain.ProductNotOnFile
	}
	r.products[key] = name
	return name, nil
}

// detailLine is DL1.
func detailLine(p pictures, ln domain.OrderLine, name string, pct cobolnum.Dec) (string, error) {
	fields := make([]string, 0, 6)
	for _, f := range []struct {
		pic edit.Picture
		val cobolnum.Dec
	}{{p.zz9, ln.LineNo}, {p.zzzz9, ln.ProductNo}, {p.qty, ln.Qty}, {p.price, ln.Price}, {p.zz9, pct}, {p.amount, ln.Extended}} {
		s, err := move(f.pic, f.val)
		if err != nil {
			return "", err
		}
		fields = append(fields, s)
	}
	return fields[0] + " " + fields[1] + "  " + alnum(name, 40) + " " + fields[2] + "  " + fields[3] + "  " +
		fields[4] + "%" + " " + fields[5], nil
}
