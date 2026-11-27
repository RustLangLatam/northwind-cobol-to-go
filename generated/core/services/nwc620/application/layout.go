package application

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/RustLangLatam/cobolnum-go"
	"github.com/RustLangLatam/cobolnum-go/edit"

	"milton_prism/core/services/nwc620/domain"
)

// Fixed texts of the print lines (FILLER VALUE clauses of HD1..HD7).
const (
	hd1Company = "NORTHWIND TRADERS - DALLAS"
	hd1Invoice = "INVOICE"
	hd1Page    = "  PAGE "
	hd2Address = "2101 COMMERCE ST.  DALLAS TX 75201"
	hd2Date    = "DATE "
	hd3OrderNo = "ORDER NO."
	hd3Ordered = "ORDERED"
	hd3ShipVia = "SHIP VIA"
	hd4SoldTo  = "SOLD TO"
	hd4ShipTo  = "SHIP TO"
	hd7Left    = "LN  PROD#  DESCRIPTION                          "
	hd7Right   = "    QTY     UNIT PRICE  DISC      EXTENDED"
	tl1Items   = "ITEMS"
	tl1Freight = "FREIGHT"
	tl1Due     = "TOTAL DUE"
)

// pictures are the edited PICTUREs of the print lines.
type pictures struct {
	zz9    edit.Picture // HD1-PAGE, DL1-LIN, DL1-DISC
	mdy    edit.Picture // HD2-DATE, HD3-ODAT 99/99/99
	z6n9   edit.Picture // HD3-ORD Z(6)9
	zzzz9  edit.Picture // DL1-PRD
	qty    edit.Picture // DL1-QTY ZZ,ZZ9
	price  edit.Picture // DL1-PRIC Z,ZZZ,ZZ9.99
	amount edit.Picture // DL1-EXT, TL1-AMT ZZZ,ZZZ,ZZ9.99-
}

func newPictures() (pictures, error) {
	var p pictures
	for _, x := range []struct {
		dst *edit.Picture
		pic string
	}{
		{&p.zz9, "ZZ9"}, {&p.mdy, "99/99/99"}, {&p.z6n9, "Z(6)9"}, {&p.zzzz9, "ZZZZ9"},
		{&p.qty, "ZZ,ZZ9"}, {&p.price, "Z,ZZZ,ZZ9.99"}, {&p.amount, "ZZZ,ZZZ,ZZ9.99-"},
	} {
		pic, err := edit.Parse(x.pic, edit.Options{})
		if err != nil {
			return pictures{}, fmt.Errorf("nwc620: edited picture %s: %w", x.pic, err)
		}
		*x.dst = pic
	}
	return p, nil
}

// alnum is MOVE of an alphanumeric item to PIC X(n): left-justified, padded with
// blanks or truncated on the right.
//
// PORT-NOTE: [Text fields are sized in characters, not EBCDIC bytes] On IBM i each
// character of these SBCS CHAR columns is one byte, so n characters are n bytes; the
// port counts characters so a non-ASCII letter read from Db2 is never split.
func alnum(s string, n int) string {
	if c := utf8.RuneCountInString(s); c < n {
		return s + strings.Repeat(" ", n-c)
	}
	i, r := 0, 0
	for i = range s {
		if r == n {
			return s[:i]
		}
		r++
	}
	return s
}

func blanks(n int) string { return strings.Repeat(" ", n) }

// move is MOVE of a numeric item to a numeric-edited receiver.
func move(p edit.Picture, d cobolnum.Dec) (string, error) {
	s, err := p.MoveDec(d)
	if err != nil {
		return "", fmt.Errorf("nwc620: edit move: %w", err)
	}
	return s, nil
}

// record pads a print line to PRT-REC PIC X(132) (WRITE PRT-REC FROM a group).
func record(line string) string { return alnum(line, domain.PrintRecordLength) }
