// Package nwc700 wires the NWC700 customer-list service.
package nwc700

import (
	"fmt"

	"milton_prism/core/services/nwc700/application"
	"milton_prism/core/services/nwc700/infrastructure/grpc_handlers"
	"milton_prism/core/services/nwc700/infrastructure/repositories"
	"milton_prism/core/services/nwc700/ports"
	"milton_prism/core/shared/svcruntime"
	nwc700v1 "milton_prism/pkg/pb/gen/milton_prism/services/nwc700/v1"
)

// Build constructs the service over the store holder and registers it on srv, with
// the readiness check (CUSMST answers a one-row read) and the service metrics.
func Build(srv *svcruntime.Server, store repositories.StoreRunner, library string, printer ports.ReportPrinter, clock ports.Clock) error {
	table, err := repositories.CustomerTable(library)
	if err != nil {
		return err
	}
	repo := repositories.NewDb2CustomerRepository(store, table)
	app := application.NewService(repo, printer, clock)
	metrics, err := grpchandlers.NewMetrics(srv.Registry())
	if err != nil {
		return fmt.Errorf("nwc700: %w", err)
	}
	nwc700v1.RegisterNwc700ServiceServer(srv.GRPC(), grpchandlers.NewHandler(app, metrics))
	srv.SetReadiness(repo.Ping)
	return nil
}
