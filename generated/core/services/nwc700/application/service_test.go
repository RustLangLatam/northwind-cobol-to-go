package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"milton_prism/core/services/nwc700/application"
	"milton_prism/core/services/nwc700/domain"
	"milton_prism/core/services/nwc700/mocks"
	"milton_prism/core/shared/domainerr"
)

func newService() (*application.Service, *mocks.MockCustomerRepository, *mocks.MockReportPrinter) {
	repo := &mocks.MockCustomerRepository{}
	pr := &mocks.MockReportPrinter{}
	return application.NewService(repo, pr, mocks.FixedClock{T: runDay}), repo, pr
}

func TestPrintCustomerList_OK(t *testing.T) {
	t.Parallel()
	svc, repo, pr := newService()
	// Raw values from the store: unpadded and over-long fields are MOVEd to the sort record.
	repo.On("ListByCountry", mock.Anything, domain.MaxCustomers+1).Return([]domain.Customer{
		{Number: "ALFKI", Name: "ALFREDS", Contact: "MARIA", City: "BERLIN", Country: "GERMANY", Phone: "030-0074321"},
		{Number: "DELTD", Name: strings.Repeat("N", 45), Contact: "X", City: "BONN", Country: "GERMANY", Phone: "1"},
	}, nil).Once()
	var got domain.Report
	pr.On("Print", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		got = args.Get(1).(domain.Report)
	}).Return("/spool/x.txt", nil).Once()

	res, err := svc.PrintCustomerList(context.Background())
	require.NoError(t, err)
	assert.Equal(t, application.Result{Customers: 2, Pages: 1, Output: "/spool/x.txt"}, res)
	assert.Equal(t, runDay, got.RunTime)
	require.Len(t, got.Lines, 7)
	assert.Equal(t, "COUNTRY: GERMANY        ", got.Lines[2].Text)
	assert.Equal(t, "ALFKI  "+pad("ALFREDS", 40)+" "+pad("BERLIN", 15)+" "+pad("MARIA", 30)+" "+pad("030-0074321", 24), got.Lines[3].Text)
	assert.Contains(t, got.Lines[4].Text, "DELTD  "+strings.Repeat("N", 40)+" BONN")
	repo.AssertExpectations(t)
	pr.AssertExpectations(t)
}

func TestPrintCustomerList_TooManyCustomers(t *testing.T) {
	t.Parallel()
	svc, repo, pr := newService()
	repo.On("ListByCountry", mock.Anything, domain.MaxCustomers+1).
		Return(make([]domain.Customer, domain.MaxCustomers+1), nil).Once()
	_, err := svc.PrintCustomerList(context.Background())
	require.Error(t, err)
	assert.True(t, domainerr.Is(err, domainerr.ResourceExhausted))
	_, msg := domainerr.KindOf(err)
	assert.Contains(t, msg, domain.ErrCodeTooManyCustomers)
	pr.AssertNotCalled(t, "Print", mock.Anything, mock.Anything)
}

func TestPrintCustomerList_MaxCustomersPrinted(t *testing.T) {
	t.Parallel()
	svc, repo, pr := newService()
	repo.On("ListByCountry", mock.Anything, domain.MaxCustomers+1).
		Return(manyIn("X", domain.MaxCustomers), nil).Once()
	pr.On("Print", mock.Anything, mock.Anything).Return("out", nil).Once()
	res, err := svc.PrintCustomerList(context.Background())
	require.NoError(t, err)
	assert.Equal(t, domain.MaxCustomers, res.Customers)
}

func TestPrintCustomerList_StoreUnavailable(t *testing.T) {
	t.Parallel()
	svc, repo, pr := newService()
	cause := errors.New("down")
	repo.On("ListByCountry", mock.Anything, mock.Anything).Return(nil, domain.StoreUnavailable(cause)).Once()
	_, err := svc.PrintCustomerList(context.Background())
	assert.True(t, domainerr.Is(err, domainerr.Unavailable))
	assert.ErrorIs(t, err, cause)
	pr.AssertNotCalled(t, "Print", mock.Anything, mock.Anything)
}

func TestPrintCustomerList_PrinterFails(t *testing.T) {
	t.Parallel()
	svc, repo, pr := newService()
	repo.On("ListByCountry", mock.Anything, mock.Anything).Return([]domain.Customer{}, nil).Once()
	pr.On("Print", mock.Anything, mock.Anything).Return("", domain.PrintFailed(errors.New("disk full"))).Once()
	_, err := svc.PrintCustomerList(context.Background())
	assert.True(t, domainerr.Is(err, domainerr.Unavailable))
}

func TestPrintCustomerList_CanceledBeforePrint(t *testing.T) {
	t.Parallel()
	svc, repo, pr := newService()
	ctx, cancel := context.WithCancel(context.Background())
	repo.On("ListByCountry", mock.Anything, mock.Anything).Run(func(mock.Arguments) { cancel() }).
		Return([]domain.Customer{}, nil).Once()
	_, err := svc.PrintCustomerList(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	pr.AssertNotCalled(t, "Print", mock.Anything, mock.Anything)
}
