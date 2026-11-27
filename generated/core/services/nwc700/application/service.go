// Package application is the NWC700 batch program: read every customer, sort by
// country, city and name, and print the list with a break on country.
package application

import (
	"context"
	"fmt"

	"milton_prism/core/services/nwc700/domain"
	"milton_prism/core/services/nwc700/ports"
)

// Result is what one run printed.
type Result struct {
	Customers int
	Pages     int
	Output    string // where the printer put the report
}

// Service runs the customer list.
type Service struct {
	customers ports.CustomerRepository
	printer   ports.ReportPrinter
	clock     ports.Clock
}

// NewService builds the use case over its ports.
func NewService(customers ports.CustomerRepository, printer ports.ReportPrinter, clock ports.Clock) *Service {
	return &Service{customers: customers, printer: printer, clock: clock}
}

// PrintCustomerList is NWC700: 0000-MAIN with its SORT input (1000-SELECT) and output
// (2000-PRINT) procedures.
func (s *Service) PrintCustomerList(ctx context.Context) (Result, error) {
	// ACCEPT WS-TODAY FROM DATE comes first, before the file is opened.
	runTime := s.clock.Now()

	// PORT-NOTE: [The SORT runs in the database: ORDER BY country, city, name] The input procedure releases every CUSMST record (status D included, no filter) and the SORT orders them ascending by SR-COUNTRY, SR-CITY, SR-NAME; the repository asks Db2 for that order, which uses the host's collating sequence like the COBOL SORT on IBM i.
	// One row above MaxCustomers is read so a file the counters cannot hold is detected instead of printed with a wrong total.
	rows, err := s.customers.ListByCountry(ctx, domain.MaxCustomers+1)
	if err != nil {
		return Result{}, fmt.Errorf("nwc700: read CUSMST: %w", err)
	}
	if len(rows) > domain.MaxCustomers {
		// PORT-NOTE: [A file over 99999 customers is refused instead of printing a wrapped total] WS-CNTRY-CNT and WS-TOTAL-CNT are PIC 9(5); the source would truncate the count silently, the port reports ResourceExhausted.
		return Result{}, domain.TooManyCustomers()
	}
	sorted := make([]domain.Customer, len(rows))
	for i, c := range rows {
		sorted[i] = sortRecord(c)
	}
	report := BuildCustomerList(runTime, sorted)
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("nwc700: before printing: %w", err)
	}
	out, err := s.printer.Print(ctx, report)
	if err != nil {
		return Result{}, fmt.Errorf("nwc700: print the customer list: %w", err)
	}
	return Result{Customers: report.Customers, Pages: report.Pages, Output: out}, nil
}
