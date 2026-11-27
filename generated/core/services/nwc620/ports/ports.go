// Package ports declares what the NWC620 application needs from the outside: the
// records of the existing Db2 files, the printer output, the business clock and the
// service counters.
package ports

import (
	"context"
	"time"

	"github.com/RustLangLatam/cobolnum-go"

	"milton_prism/core/services/nwc620/domain"
)

// InvoiceRepository reads the files NWC620 opens for input. A record that does not
// exist is reported by found=false (the COBOL INVALID KEY), never as an error.
type InvoiceRepository interface {
	// GetOrderHeader is READ ORDHDR by OHORD#.
	GetOrderHeader(ctx context.Context, orderNo cobolnum.Dec) (h domain.OrderHeader, found bool, err error)
	// GetCustomer is READ CUSMST by CMCUST.
	GetCustomer(ctx context.Context, customer string) (c domain.Customer, found bool, err error)
	// GetCodeDescription is READ CODTBL by CTTYPE, CTCODE and returns CTDESC.
	GetCodeDescription(ctx context.Context, codeType, code string) (desc string, found bool, err error)
	// ListOrderLines is START ORDDTL KEY NOT LESS THAN (orderNo, 0) followed by READ
	// NEXT while ODORD# is orderNo: the lines of the order with ODLIN# >= 0, in
	// ODLIN# order.
	ListOrderLines(ctx context.Context, orderNo cobolnum.Dec) ([]domain.OrderLine, error)
	// GetProductName is READ PRDMST by PMPRD# and returns PMNAME.
	GetProductName(ctx context.Context, productNo cobolnum.Dec) (name string, found bool, err error)
}

// InvoicePrinter receives the spooled file of one call. It is called only when the
// program wrote at least one record.
type InvoicePrinter interface {
	Print(ctx context.Context, f domain.SpoolFile) error
}

// Clock is the business clock of the source system (ACCEPT FROM DATE).
type Clock interface {
	Now() time.Time
}

// Metrics counts business outcomes.
type Metrics interface {
	InvoicePrinted()
	OrderNotFound()
}
