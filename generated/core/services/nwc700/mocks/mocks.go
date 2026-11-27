// Package mocks holds testify mocks of the NWC700 ports.
package mocks

import (
	"context"
	"time"

	"github.com/stretchr/testify/mock"

	"milton_prism/core/services/nwc700/domain"
	"milton_prism/core/services/nwc700/ports"
)

var (
	_ ports.CustomerRepository = (*MockCustomerRepository)(nil)
	_ ports.ReportPrinter      = (*MockReportPrinter)(nil)
	_ ports.Clock              = FixedClock{}
)

// MockCustomerRepository mocks ports.CustomerRepository.
type MockCustomerRepository struct{ mock.Mock }

// ListByCountry records the call.
func (m *MockCustomerRepository) ListByCountry(ctx context.Context, limit int) ([]domain.Customer, error) {
	args := m.Called(ctx, limit)
	v, _ := args.Get(0).([]domain.Customer)
	return v, args.Error(1)
}

// MockReportPrinter mocks ports.ReportPrinter.
type MockReportPrinter struct{ mock.Mock }

// Print records the call.
func (m *MockReportPrinter) Print(ctx context.Context, report domain.Report) (string, error) {
	args := m.Called(ctx, report)
	return args.String(0), args.Error(1)
}

// FixedClock always answers the same time.
type FixedClock struct{ T time.Time }

// Now returns the fixed time.
func (c FixedClock) Now() time.Time { return c.T }
