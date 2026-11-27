// Package grpchandlers exposes the NWC620 use case over gRPC. It maps the wire
// messages to domain values and back and leaves every rule to the application.
package grpchandlers

import (
	"context"

	"go.opentelemetry.io/otel"

	"milton_prism/core/services/nwc620/application"
	"milton_prism/core/shared/svcruntime"
	nwc620v1 "milton_prism/pkg/pb/gen/milton_prism/services/nwc620/v1"
)

var tracer = otel.Tracer("milton_prism/core/services/nwc620/grpc_handlers")

// Nwc620Handler serves Nwc620Service. The source performs no request
// authentication; the platform runtime authenticates every call.
type Nwc620Handler struct {
	nwc620v1.UnimplementedNwc620ServiceServer
	svc *application.Service
}

// NewNwc620Handler returns the handler over the application service.
func NewNwc620Handler(svc *application.Service) *Nwc620Handler {
	return &Nwc620Handler{svc: svc}
}

// Nwc620 prints the invoice of req.LkOrdNo.
func (h *Nwc620Handler) Nwc620(ctx context.Context, req *nwc620v1.Nwc620Request) (*nwc620v1.Nwc620Response, error) {
	ctx, span := tracer.Start(ctx, "Nwc620Service/Nwc620")
	defer span.End()
	res, err := h.svc.PrintInvoice(ctx, int64(req.GetLkOrdNo()))
	if err != nil {
		span.RecordError(err)
		return nil, svcruntime.StatusError(err)
	}
	// The order number is 1..9999999 here (validated), so it fits the wire's sint32.
	return &nwc620v1.Nwc620Response{Status: res.Status, LkOrdNo: int32(res.OrderNo)}, nil
}
