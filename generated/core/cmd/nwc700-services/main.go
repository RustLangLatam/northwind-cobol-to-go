// Command nwc700-services serves the NWC700 customer list by country.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"milton_prism/core/services/nwc700"
	"milton_prism/core/services/nwc700/infrastructure/output"
	"milton_prism/core/services/nwc700/infrastructure/repositories"
	"milton_prism/core/shared/svcruntime"
	"milton_prism/pkg/db2access"
	"milton_prism/pkg/db2access/ibmi"
	_ "milton_prism/pkg/db2access/ibmi/i400conn" // links the IBM i driver
	applog "milton_prism/pkg/log"
)

const (
	serviceName = "nwc700"
	storeRetry  = 5 * time.Second
	// customerReadRows is the one read of the list: MaxCustomers plus the row that
	// detects an overflow.
	customerReadRows = 100000
)

func main() {
	if err := run(); err != nil {
		applog.Errorf("nwc700: %v", err)
		os.Exit(1)
	}
}

func run() error {
	applog.InitLogger(serviceName)
	applog.ConfigureFromEnv()

	cfg, err := svcruntime.ConfigFromEnv(serviceName, os.Getenv)
	if err != nil {
		return err
	}
	clock, err := svcruntime.ClockFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	// DB2_COMMIT_MODE is required and has no default: the source's commitment control
	// was not measured, so the operator chooses it. The service writes no table.
	dbCfg, err := db2access.ConfigFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if dbCfg.MaxLimit != 0 && dbCfg.MaxLimit < customerReadRows {
		return fmt.Errorf("nwc700: %s must be at least %d, the rows the customer list reads", db2access.EnvMaxLimit, customerReadRows)
	}
	printer, err := output.PrinterFromEnv(os.Getenv)
	if err != nil {
		return err
	}

	srv, err := svcruntime.NewServer(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var store svcruntime.Holder[*db2access.Store]
	store.Start(ctx, func(ctx context.Context) (*db2access.Store, error) {
		return db2access.Open(ctx, ibmi.Driver{}, dbCfg)
	}, storeRetry,
		svcruntime.WithPermanentErrors(repositories.IsPermanentOpenError),
		svcruntime.WithOnPermanent(srv.Fail))
	srv.OnShutdownNamed("db2", store.Close)

	if err := nwc700.Build(srv, &store, dbCfg.Library, printer, clock); err != nil {
		return err
	}
	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
