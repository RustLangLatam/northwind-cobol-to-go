// Package ports declares what the NWC700 application needs from the outside: the
// customer master, the printer and the business clock.
package ports

import (
	"context"
	"time"

	"milton_prism/core/services/nwc700/domain"
)

// CustomerRepository reads CUSMST.
type CustomerRepository interface {
	// ListByCountry returns at most limit customers, every record of the file (deleted
	// status included), ascending by country, city and company name in the host's
	// collating sequence, as the program's SORT returns them.
	ListByCountry(ctx context.Context, limit int) ([]domain.Customer, error)
}

// ReportPrinter writes a finished report to its output (the QSYSPRT spool file of the
// source) and returns where it went.
type ReportPrinter interface {
	Print(ctx context.Context, report domain.Report) (string, error)
}

// Clock is the business clock of the source system.
type Clock interface {
	Now() time.Time
}
