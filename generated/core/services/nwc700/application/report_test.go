package application_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"milton_prism/core/services/nwc700/application"
	"milton_prism/core/services/nwc700/domain"
)

var runDay = time.Date(2026, 11, 25, 9, 30, 0, 0, time.UTC)

func pad(s string, n int) string { return s + strings.Repeat(" ", n-len(s)) }

func cust(no, name, city, country string) domain.Customer {
	return domain.Customer{
		Number: pad(no, 5), Name: pad(name, 40), Contact: pad("CONTACT "+no, 30),
		City: pad(city, 15), Country: pad(country, 15), Phone: pad("555-"+no, 24),
	}
}

func manyIn(country string, n int) []domain.Customer {
	out := make([]domain.Customer, n)
	for i := range out {
		out[i] = cust(fmt.Sprintf("%05d", i), fmt.Sprintf("COMPANY %05d", i), "CITY", country)
	}
	return out
}

const hd1Page1 = "11/25/26            NORTHWIND TRADERS - DALLAS    CUSTOMER LIST BY COUNTRY      PAGE   1"
const hd2 = "CUST   COMPANY                                  CITY            CONTACT                        PHONE     "

func TestBuildCustomerList_EmptyFile(t *testing.T) {
	t.Parallel()
	r := application.BuildCustomerList(runDay, nil)
	require.Len(t, r.Lines, 3)
	assert.Equal(t, domain.PrintLine{NewPage: true, Text: hd1Page1}, r.Lines[0])
	assert.Equal(t, domain.PrintLine{Advance: 2, Text: hd2}, r.Lines[1])
	assert.Equal(t, domain.PrintLine{Advance: 3, Text: "          TOTAL CUSTOMERS            0"}, r.Lines[2])
	assert.Equal(t, 0, r.Customers)
	assert.Equal(t, 1, r.Pages)
}

func TestBuildCustomerList_CountryBreaks(t *testing.T) {
	t.Parallel()
	in := []domain.Customer{
		cust("ALFKI", "ALFREDS FUTTERKISTE", "BERLIN", "GERMANY"),
		cust("BLAUS", "BLAUER SEE", "MANNHEIM", "GERMANY"),
		cust("GREAL", "GREAT LAKES", "EUGENE", "USA"),
	}
	r := application.BuildCustomerList(runDay, in)
	texts := make([]string, len(r.Lines))
	for i, l := range r.Lines {
		texts[i] = strings.TrimRight(l.Text, " ")
	}
	assert.Equal(t, []string{
		hd1Page1,
		strings.TrimRight(hd2, " "),
		"COUNTRY: GERMANY",
		"ALFKI  ALFREDS FUTTERKISTE                      BERLIN          CONTACT ALFKI                  555-ALFKI",
		"BLAUS  BLAUER SEE                               MANNHEIM        CONTACT BLAUS                  555-BLAUS",
		"          CUSTOMERS IN COUNTRY       2",
		"COUNTRY: USA",
		"GREAL  GREAT LAKES                              EUGENE          CONTACT GREAL                  555-GREAL",
		"          CUSTOMERS IN COUNTRY       1",
		"          TOTAL CUSTOMERS            3",
	}, texts)
	assert.Equal(t, []int{0, 2, 2, 1, 1, 2, 2, 1, 2, 3}, advances(r))
	assert.Equal(t, 3, r.Customers)
	assert.Equal(t, 1, r.Pages)
}

func advances(r domain.Report) []int {
	out := make([]int, len(r.Lines))
	for i, l := range r.Lines {
		out[i] = l.Advance
	}
	return out
}

func pageStarts(r domain.Report) []int {
	var out []int
	for i, l := range r.Lines {
		if l.NewPage {
			out = append(out, i)
		}
	}
	return out
}

func TestBuildCustomerList_PageOverflowWithinCountry(t *testing.T) {
	t.Parallel()
	// Heading (3) + country heading (2) + 51 details = 56 > 55: the 52nd detail opens page 2.
	r := application.BuildCustomerList(runDay, manyIn("MEXICO", 60))
	require.Equal(t, []int{0, 54}, pageStarts(r))
	assert.Contains(t, r.Lines[54].Text, "PAGE   2")
	assert.Equal(t, hd2, r.Lines[55].Text)
	assert.True(t, strings.HasPrefix(r.Lines[56].Text, "00051"), "the detail continues without a new country heading")
	assert.Equal(t, 2, r.Pages)
	assert.Equal(t, 60, r.Customers)
}

func TestBuildCustomerList_CountryHeadingNearPageEnd(t *testing.T) {
	t.Parallel()
	// After 46 details the count is 51; the country total adds 2 (53 > 50), so the
	// next country heading goes on a new page.
	in := append(manyIn("ARGENTINA", 46), cust("ZZZZZ", "LAST", "BERN", "SWITZERLAND"))
	r := application.BuildCustomerList(runDay, in)
	starts := pageStarts(r)
	require.Len(t, starts, 2)
	assert.Equal(t, "          CUSTOMERS IN COUNTRY      46", r.Lines[starts[1]-1].Text)
	assert.Equal(t, "COUNTRY: SWITZERLAND    ", r.Lines[starts[1]+2].Text)
}

func TestBuildCustomerList_GrandTotalOverflow(t *testing.T) {
	t.Parallel()
	// 51 details leave the count at 56: the final country total does not add, the
	// grand total goes on page 2.
	r := application.BuildCustomerList(runDay, manyIn("BRAZIL", 51))
	starts := pageStarts(r)
	require.Len(t, starts, 2)
	last := r.Lines[len(r.Lines)-1]
	assert.Equal(t, "          TOTAL CUSTOMERS           51", last.Text)
	assert.Equal(t, len(r.Lines)-3, starts[1])
	assert.Equal(t, "          CUSTOMERS IN COUNTRY      51", r.Lines[starts[1]-1].Text)
}

func TestBuildCustomerList_EditedCountsWithComma(t *testing.T) {
	t.Parallel()
	r := application.BuildCustomerList(runDay, manyIn("CANADA", 1234))
	last := r.Lines[len(r.Lines)-1]
	assert.Equal(t, "          TOTAL CUSTOMERS        1,234", last.Text)
	assert.Equal(t, 1234, r.Customers)
	assert.Equal(t, 24, r.Pages) // 51 lines on page 1 with the heading, 53 on each next
}

func TestBuildCustomerList_HeadingDate(t *testing.T) {
	t.Parallel()
	r := application.BuildCustomerList(time.Date(2001, 2, 3, 0, 0, 0, 0, time.UTC), nil)
	assert.True(t, strings.HasPrefix(r.Lines[0].Text, "02/03/01"))
}
