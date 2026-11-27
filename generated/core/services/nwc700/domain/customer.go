// Package domain holds the NWC700 customer-list model: the sort record the program
// builds from CUSMST and the print lines of the report.
package domain

// Field widths of the NWC700 sort record (SORT-REC), in characters.
const (
	CustomerNumberWidth = 5  // SR-NO      PIC X(5)
	CompanyNameWidth    = 40 // SR-NAME    PIC X(40)
	ContactWidth        = 30 // SR-CONTACT PIC X(30)
	CityWidth           = 15 // SR-CITY    PIC X(15)
	CountryWidth        = 15 // SR-COUNTRY PIC X(15)
	PhoneWidth          = 24 // SR-PHONE   PIC X(24)
)

// MaxCustomers is the largest count the report can print: WS-CNTRY-CNT and
// WS-TOTAL-CNT are PIC 9(5).
const MaxCustomers = 99999

// Customer is one SORT-REC: the CUSMST fields the list prints. Every field holds the
// fixed-width value of its PIC X item (blank padded, cut at the width).
type Customer struct {
	Number  string
	Name    string
	Contact string
	City    string
	Country string
	Phone   string
}
