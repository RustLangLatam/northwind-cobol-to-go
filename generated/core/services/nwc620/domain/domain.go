// Package domain holds the records NWC620 reads and the invoice it prints. It never
// imports generated protobuf code: the gRPC handler maps the wire messages.
//
// Every numeric item is a COBOL fixed-point decimal (cobolnum.Dec) with the picture
// of its DDS field, never an approximate binary number.
package domain

import "github.com/RustLangLatam/cobolnum-go"

// Pictures of the items NWC620 reads (DDS of the physical files) and of its working
// storage. The DDS packed and zoned fields are signed (SQL DECIMAL/NUMERIC).
var (
	PicOrderNo   = cobolnum.Pic{Digits: 7, Signed: true}            // LK-ORD-NO S9(7) COMP-3, OHORD#/ODORD# 7P 0
	PicLineNo    = cobolnum.Pic{Digits: 3, Signed: true}            // ODLIN# 3P 0
	PicProductNo = cobolnum.Pic{Digits: 5, Signed: true}            // ODPRD#/PMPRD# 5P 0
	PicQty       = cobolnum.Pic{Digits: 5, Signed: true}            // ODQTY 5P 0
	PicPrice     = cobolnum.Pic{Digits: 9, Scale: 2, Signed: true}  // ODPRIC 9P 2
	PicDisc      = cobolnum.Pic{Digits: 3, Scale: 2, Signed: true}  // ODDISC 3P 2
	PicExt       = cobolnum.Pic{Digits: 11, Scale: 2, Signed: true} // ODEXT 11P 2
	PicDate7     = cobolnum.Pic{Digits: 7, Signed: true}            // OHODAT 7P 0 (CYYMMDD)
	PicFreight   = cobolnum.Pic{Digits: 7, Scale: 2, Signed: true}  // OHFRT 7P 2
	PicTotal     = cobolnum.Pic{Digits: 11, Scale: 2, Signed: true} // OHTOTL 11P 2

	PicItems   = cobolnum.Pic{Digits: 11, Scale: 2, Signed: true} // WS-ITEMS S9(9)V99
	PicDue     = cobolnum.Pic{Digits: 11, Scale: 2, Signed: true} // WS-DUE S9(9)V99
	PicDiscPct = cobolnum.Pic{Digits: 3}                          // WS-DISC-PCT 999
	PicLineCnt = cobolnum.Pic{Digits: 2}                          // WS-LINE-CNT 99
	PicPage    = cobolnum.Pic{Digits: 3}                          // WS-PAGE 999
	PicCYMD    = cobolnum.Pic{Digits: 7}                          // WS-CYMD 9(7)
	PicCYY     = cobolnum.Pic{Digits: 3}                          // WS-CYY 999
	PicCC      = cobolnum.Pic{Digits: 1}                          // WS-CC 9
	PicYY      = cobolnum.Pic{Digits: 2}                          // WS-YY 99
	PicMMDD    = cobolnum.Pic{Digits: 4}                          // WS-MMDD 9(4)
	PicMDY6    = cobolnum.Pic{Digits: 6}                          // WS-MDY6 9(6)
	PicTwo     = cobolnum.Pic{Digits: 2}                          // WS-T-YY, WS-T-MM, WS-T-DD 99
)

// MinOrderNo and MaxOrderNo bound an order number the program can find: OHORD# is a
// positive key of 7 digits.
const (
	MinOrderNo = 1
	MaxOrderNo = 9999999
)

// OrderHeader is the part of an ORDHDR record the invoice uses. Text fields keep
// the column's fixed length (CHAR blank padding).
type OrderHeader struct {
	OrderNo     cobolnum.Dec // OHORD#
	Customer    string       // OHCUST
	OrderDate   cobolnum.Dec // OHODAT, CYYMMDD
	ShipVia     string       // OHSHPV
	Freight     cobolnum.Dec // OHFRT
	ShipName    string       // OHSNAM
	ShipAddress string       // OHSADR
	ShipCity    string       // OHSCTY
	ShipCountry string       // OHSCNT
	Total       cobolnum.Dec // OHTOTL
}

// Customer is the part of a CUSMST record the invoice uses.
type Customer struct {
	Name    string // CMNAME
	Address string // CMADDR
	City    string // CMCITY
	Country string // CMCNTY
}

// OrderLine is one ORDDTL record.
type OrderLine struct {
	LineNo    cobolnum.Dec // ODLIN#
	ProductNo cobolnum.Dec // ODPRD#
	Qty       cobolnum.Dec // ODQTY
	Price     cobolnum.Dec // ODPRIC
	Disc      cobolnum.Dec // ODDISC, a fraction (.xx)
	Extended  cobolnum.Dec // ODEXT, as stored by NWC610
}

// Code table types and the program's fixed texts.
const (
	CodeTypeShipper   = "SHP"
	CodeLength        = 10 // CTCODE CHAR(10): MOVE OH-SHPV TO CT-CODE pads with blanks
	CustomerNotOnFile = "*** CUSTOMER NOT ON FILE ***"
	ProductNotOnFile  = "*NOT ON FILE"
	SpoolFileName     = "NWINVOICE" // OVRPRTF SPLFNAME of NWC620C
	FormType          = "INVOICE"   // OVRPRTF FORMTYPE of NWC620C
	PrintRecordLength = 132         // PRT-REC PIC X(132), PAGESIZE(66 132)
	PageLength        = 66
	StatusOK          = "0" // RETURN-CODE after GOBACK
)

// Advance is the AFTER ADVANCING phrase of one WRITE.
type Advance struct {
	Page  bool // AFTER ADVANCING PAGE
	Lines int  // AFTER ADVANCING n LINE(S), when Page is false
}

// PrintRecord is one WRITE PRT-REC: the advance and the 132-byte record, trailing
// blanks included.
type PrintRecord struct {
	Advance Advance
	Line    string
}

// SpoolFile is the printer output of one call (QSYSPRT overridden by NWC620C).
type SpoolFile struct {
	Name     string
	FormType string
	OrderNo  cobolnum.Dec
	Records  []PrintRecord
}
