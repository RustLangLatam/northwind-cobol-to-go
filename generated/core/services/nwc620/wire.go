// Package nwc620 is the composition point of the NWC620 invoice service.
package nwc620

import (
	"context"

	"milton_prism/core/services/nwc620/application"
	grpchandlers "milton_prism/core/services/nwc620/infrastructure/grpc_handlers"
	"milton_prism/core/services/nwc620/infrastructure/metrics"
	"milton_prism/core/services/nwc620/infrastructure/repositories"
	"milton_prism/core/services/nwc620/ports"
	"milton_prism/core/shared/svcruntime"
	nwc620v1 "milton_prism/pkg/pb/gen/milton_prism/services/nwc620/v1"
)

// Deps are the adapters the service runs on.
type Deps struct {
	Store   repositories.StoreRunner
	Tables  repositories.Tables
	Printer ports.InvoicePrinter
	Clock   ports.Clock
}

// Build wires repository, application and handler, registers the service on srv and
// returns the readiness check of the store (a cheap read).
func Build(srv *svcruntime.Server, deps Deps) (func(context.Context) error, error) {
	repo := repositories.NewDb2InvoiceRepository(deps.Store, deps.Tables)
	counters, err := metrics.New(srv.Registry())
	if err != nil {
		return nil, err
	}
	app, err := application.NewService(repo, deps.Printer, deps.Clock, counters)
	if err != nil {
		return nil, err
	}
	nwc620v1.RegisterNwc620ServiceServer(srv.GRPC(), grpchandlers.NewNwc620Handler(app))
	return repo.Ping, nil
}
