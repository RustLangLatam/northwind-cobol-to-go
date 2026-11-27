// Package mocks holds testify mocks of the NWC620 ports.
package mocks

import (
	"context"
	"time"

	"github.com/RustLangLatam/cobolnum-go"
	"github.com/stretchr/testify/mock"

	"milton_prism/core/services/nwc620/domain"
	"milton_prism/core/services/nwc620/ports"
)

var (
	_ ports.InvoiceRepository = (*MockInvoiceRepository)(nil)
	_ ports.InvoicePrinter    = (*MockInvoicePrinter)(nil)
	_ ports.Metrics           = (*MockMetrics)(nil)
	_ ports.Clock             = FixedClock{}
)

// MockInvoiceRepository mocks ports.InvoiceRepository.
type MockInvoiceRepository struct{ mock.Mock }

func (m *MockInvoiceRepository) GetOrderHeader(ctx context.Context, orderNo cobolnum.Dec) (domain.OrderHeader, bool, error) {
	args := m.Called(ctx, orderNo)
	h, _ := args.Get(0).(domain.OrderHeader)
	return h, args.Bool(1), args.Error(2)
}

func (m *MockInvoiceRepository) GetCustomer(ctx context.Context, customer string) (domain.Customer, bool, error) {
	args := m.Called(ctx, customer)
	c, _ := args.Get(0).(domain.Customer)
	return c, args.Bool(1), args.Error(2)
}

func (m *MockInvoiceRepository) GetCodeDescription(ctx context.Context, codeType, code string) (string, bool, error) {
	args := m.Called(ctx, codeType, code)
	return args.String(0), args.Bool(1), args.Error(2)
}

func (m *MockInvoiceRepository) ListOrderLines(ctx context.Context, orderNo cobolnum.Dec) ([]domain.OrderLine, error) {
	args := m.Called(ctx, orderNo)
	l, _ := args.Get(0).([]domain.OrderLine)
	return l, args.Error(1)
}

func (m *MockInvoiceRepository) GetProductName(ctx context.Context, productNo cobolnum.Dec) (string, bool, error) {
	args := m.Called(ctx, productNo)
	return args.String(0), args.Bool(1), args.Error(2)
}

// MockInvoicePrinter mocks ports.InvoicePrinter.
type MockInvoicePrinter struct{ mock.Mock }

func (m *MockInvoicePrinter) Print(ctx context.Context, f domain.SpoolFile) error {
	return m.Called(ctx, f).Error(0)
}

// MockMetrics mocks ports.Metrics.
type MockMetrics struct{ mock.Mock }

func (m *MockMetrics) InvoicePrinted() { m.Called() }
func (m *MockMetrics) OrderNotFound()  { m.Called() }

// FixedClock is a business clock stopped at T.
type FixedClock struct{ T time.Time }

func (c FixedClock) Now() time.Time { return c.T }
