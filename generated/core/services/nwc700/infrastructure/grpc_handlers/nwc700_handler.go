// Package grpchandlers exposes the NWC700 batch program over gRPC.
package grpchandlers

import (
	"context"
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"milton_prism/core/services/nwc700/application"
	"milton_prism/core/shared/svcruntime"
	nwc700v1 "milton_prism/pkg/pb/gen/milton_prism/services/nwc700/v1"
)

// StatusCompleted is the RETURN-CODE of a run that reached STOP RUN.
const StatusCompleted = "0"

var tracer = otel.Tracer("milton_prism/core/services/nwc700/grpc_handlers")

// Metrics counts what the runs printed.
type Metrics struct {
	runs      *prometheus.CounterVec
	customers prometheus.Counter
}

// NewMetrics registers the service's collectors on reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nwc700_customer_list_runs_total",
			Help: "Customer list runs by outcome.",
		}, []string{"outcome"}),
		customers: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "nwc700_customers_listed_total",
			Help: "Customers printed on the customer list.",
		}),
	}
	for _, c := range []prometheus.Collector{m.runs, m.customers} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("nwc700: register metrics: %w", err)
		}
	}
	return m, nil
}

// Handler serves Nwc700Service.
type Handler struct {
	nwc700v1.UnimplementedNwc700ServiceServer
	svc     *application.Service
	metrics *Metrics
}

// NewHandler builds the handler.
func NewHandler(svc *application.Service, metrics *Metrics) *Handler {
	return &Handler{svc: svc, metrics: metrics}
}

// Nwc700 runs the customer list (the request has no fields to validate).
func (h *Handler) Nwc700(ctx context.Context, _ *nwc700v1.Nwc700Request) (*nwc700v1.Nwc700Response, error) {
	ctx, span := tracer.Start(ctx, "nwc700.PrintCustomerList")
	defer span.End()
	res, err := h.svc.PrintCustomerList(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "customer list failed")
		h.metrics.runs.WithLabelValues(outcome(err)).Inc()
		return nil, svcruntime.StatusError(err)
	}
	span.SetAttributes(attribute.Int("nwc700.customers", res.Customers), attribute.Int("nwc700.pages", res.Pages))
	h.metrics.runs.WithLabelValues("ok").Inc()
	h.metrics.customers.Add(float64(res.Customers))
	return &nwc700v1.Nwc700Response{Status: StatusCompleted}, nil
}

func outcome(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "error"
}
