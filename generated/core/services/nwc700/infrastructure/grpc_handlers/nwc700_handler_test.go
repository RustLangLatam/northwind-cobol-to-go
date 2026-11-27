package grpchandlers_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"milton_prism/core/services/nwc700/application"
	"milton_prism/core/services/nwc700/domain"
	grpchandlers "milton_prism/core/services/nwc700/infrastructure/grpc_handlers"
	"milton_prism/core/services/nwc700/mocks"
	"milton_prism/core/shared/svcruntime"
	nwc700v1 "milton_prism/pkg/pb/gen/milton_prism/services/nwc700/v1"
)

type harness struct {
	client  nwc700v1.Nwc700ServiceClient
	repo    *mocks.MockCustomerRepository
	printer *mocks.MockReportPrinter
	reg     *prometheus.Registry
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	repo := &mocks.MockCustomerRepository{}
	pr := &mocks.MockReportPrinter{}
	reg := prometheus.NewRegistry()
	metrics, err := grpchandlers.NewMetrics(reg)
	require.NoError(t, err)
	app := application.NewService(repo, pr, mocks.FixedClock{T: time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)})

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	nwc700v1.RegisterNwc700ServiceServer(srv, grpchandlers.NewHandler(app, metrics))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return &harness{client: nwc700v1.NewNwc700ServiceClient(conn), repo: repo, printer: pr, reg: reg}
}

func counter(t *testing.T, reg *prometheus.Registry, name, outcome string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			match := outcome == ""
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == outcome {
					match = true
				}
			}
			if match {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestNwc700_OK(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.repo.On("ListByCountry", mock.Anything, domain.MaxCustomers+1).Return([]domain.Customer{
		{Number: "ALFKI", Name: "ALFREDS", City: "BERLIN", Country: "GERMANY"},
		{Number: "ANATR", Name: "ANA", City: "MEXICO", Country: "MEXICO"},
	}, nil).Once()
	var printed domain.Report
	h.printer.On("Print", mock.Anything, mock.Anything).Run(func(a mock.Arguments) {
		printed = a.Get(1).(domain.Report)
	}).Return("spool", nil).Once()

	resp, err := h.client.Nwc700(context.Background(), &nwc700v1.Nwc700Request{})
	require.NoError(t, err)
	assert.Equal(t, grpchandlers.StatusCompleted, resp.GetStatus())
	assert.Equal(t, 2, printed.Customers)
	assert.Equal(t, 1.0, counter(t, h.reg, "nwc700_customer_list_runs_total", "ok"))
	assert.Equal(t, 2.0, counter(t, h.reg, "nwc700_customers_listed_total", ""))
}

func TestNwc700_ErrorStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		repoErr  error
		rows     []domain.Customer
		printErr error
		want     codes.Code
	}{
		{name: "store unavailable", repoErr: domain.StoreUnavailable(svcruntime.ErrNotReady), want: codes.Unavailable},
		{name: "store busy", repoErr: domain.StoreBusy(errors.New("lock")), want: codes.Aborted},
		{name: "read limit", repoErr: domain.ReadLimitTooLow(errors.New("limit")), want: codes.FailedPrecondition},
		{name: "result too large", repoErr: domain.ResultTooLarge(errors.New("big")), want: codes.ResourceExhausted},
		{name: "unclassified", repoErr: errors.New("boom"), want: codes.Internal},
		{name: "deadline", repoErr: context.DeadlineExceeded, want: codes.DeadlineExceeded},
		{name: "too many customers", rows: make([]domain.Customer, domain.MaxCustomers+1), want: codes.ResourceExhausted},
		{name: "printer fails", rows: []domain.Customer{}, printErr: domain.PrintFailed(errors.New("disk")), want: codes.Unavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.repo.On("ListByCountry", mock.Anything, mock.Anything).Return(tc.rows, tc.repoErr).Once()
			h.printer.On("Print", mock.Anything, mock.Anything).Return("", tc.printErr).Maybe()
			_, err := h.client.Nwc700(context.Background(), &nwc700v1.Nwc700Request{})
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tc.want, st.Code())
			assert.NotContains(t, st.Message(), "boom")
			assert.NotContains(t, st.Message(), "disk")
		})
	}
}
