package application

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"milton_prism/core/services/nwc700/domain"
)

// Line-count thresholds of the NWC700 page logic.
const (
	pageOverflow     = 55 // IF WS-LINE-CNT > 55: new page before a detail or the total
	breakOverflow    = 50 // IF WS-LINE-CNT > 50: new page before a country heading
	linesAfterHeader = 3  // MOVE 3 TO WS-LINE-CNT
	initialLineCount = 99 // WS-LINE-CNT VALUE 99: the first record forces a heading
)

// fit is a MOVE to a PIC X(n) item: cut at n characters or padded with blanks.
func fit(s string, n int) string {
	if c := utf8.RuneCountInString(s); c < n {
		return s + strings.Repeat(" ", n-c)
	}
	r := []rune(s)
	return string(r[:n])
}

// editCount is a 5-digit count moved to PIC ZZ,ZZ9: leading zeros and a comma with
// only zeros to its left print as blanks.
func editCount(n int) string {
	n %= 100000
	if n >= 1000 {
		return fmt.Sprintf("%2d,%03d", n/1000, n%1000)
	}
	return fmt.Sprintf("%6d", n)
}

// headingDate is ACCEPT WS-TODAY FROM DATE (YYMMDD) rearranged to MMDDYY and moved to
// PIC 99/99/99.
func headingDate(t time.Time) string {
	return fmt.Sprintf("%02d/%02d/%02d", int(t.Month()), t.Day(), t.Year()%100)
}

// printer holds the WORKING-STORAGE of the output procedure (2000-PRINT).
type printer struct {
	date       string
	lines      []domain.PrintLine
	lineCnt    int
	page       int
	cntryCnt   int
	totalCnt   int
	prev       string
	first      bool
	prevIsHigh bool // WS-PREV-COUNTRY still holds HIGH-VALUES
}

func (p *printer) write(newPage bool, advance int, text string) {
	p.lines = append(p.lines, domain.PrintLine{NewPage: newPage, Advance: advance, Text: text})
}

// headings is 2700-HEADINGS.
func (p *printer) headings() {
	p.page++
	hd1 := p.date + strings.Repeat(" ", 12) +
		fit("NORTHWIND TRADERS - DALLAS", 30) +
		fit("CUSTOMER LIST BY COUNTRY", 30) +
		fit("PAGE", 5) + fmt.Sprintf("%3d", p.page%1000) // HD1-PAGE PIC ZZ9
	p.write(true, 0, hd1)
	hd2 := fit("CUST", 7) + fit("COMPANY", 41) + fit("CITY", 16) + fit("CONTACT", 31) + fit("PHONE", 10)
	p.write(false, 2, hd2)
	p.lineCnt = linesAfterHeader
}

func countryTotal(n int) string {
	return strings.Repeat(" ", 10) + fit("CUSTOMERS IN COUNTRY", 22) + editCount(n)
}

// countryBreak is 2500-COUNTRY-BREAK.
func (p *printer) countryBreak(country string) {
	if !p.first {
		p.write(false, 2, countryTotal(p.cntryCnt))
		p.lineCnt += 2
	}
	p.first = false
	p.cntryCnt = 0
	p.prev, p.prevIsHigh = country, false
	if p.lineCnt > breakOverflow {
		p.headings()
	}
	p.write(false, 2, "COUNTRY: "+country)
	p.lineCnt += 2
}

// detail is the body of 2000-RETURN for one sorted record.
func (p *printer) detail(c domain.Customer) {
	if p.prevIsHigh || c.Country != p.prev {
		p.countryBreak(c.Country)
	}
	if p.lineCnt > pageOverflow {
		p.headings()
	}
	dl1 := c.Number + "  " + c.Name + " " + c.City + " " + c.Contact + " " + c.Phone
	p.write(false, 1, dl1)
	p.lineCnt++
	p.cntryCnt++
	p.totalCnt++
}

// finish is 2000-END.
func (p *printer) finish() {
	if !p.first {
		p.write(false, 2, countryTotal(p.cntryCnt))
		// PORT-NOTE: [The last country total does not add to the line count, as in the source] 2000-END writes TL1 without ADD 2 TO WS-LINE-CNT; the port keeps that, so the overflow test before the grand total sees the same count.
	}
	if p.lineCnt > pageOverflow {
		p.headings()
	}
	p.write(false, 3, strings.Repeat(" ", 10)+fit("TOTAL CUSTOMERS", 22)+editCount(p.totalCnt))
}

// BuildCustomerList is the output procedure of NWC700 over records already sorted by
// country, city and name: a heading per page, a heading and a count per country, and
// the grand total.
func BuildCustomerList(runTime time.Time, sorted []domain.Customer) domain.Report {
	p := &printer{date: headingDate(runTime), lineCnt: initialLineCount, first: true, prevIsHigh: true}
	for _, c := range sorted {
		p.detail(c)
	}
	p.finish()
	return domain.Report{RunTime: runTime, Lines: p.lines, Customers: p.totalCnt, Pages: p.page}
}

// sortRecord is the input procedure's MOVEs from CUS-REC to SORT-REC.
func sortRecord(c domain.Customer) domain.Customer {
	return domain.Customer{
		Number:  fit(c.Number, domain.CustomerNumberWidth),
		Name:    fit(c.Name, domain.CompanyNameWidth),
		Contact: fit(c.Contact, domain.ContactWidth),
		City:    fit(c.City, domain.CityWidth),
		Country: fit(c.Country, domain.CountryWidth),
		Phone:   fit(c.Phone, domain.PhoneWidth),
	}
}
