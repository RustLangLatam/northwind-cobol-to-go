// Command nwc620-services serves Nwc620Service: the invoice print of NWC620.
package main

import (
	"context"
	"errors"
	"os"
	"time"

	"milton_prism/core/services/nwc620"
	"milton_prism/core/services/nwc620/infrastructure/printer"
	"milton_prism/core/services/nwc620/infrastructure/repositories"
	"milton_prism/core/shared/svcruntime"
	"milton_prism/pkg/db2access"
	"milton_prism/pkg/db2access/ibmi"
	_ "milton_prism/pkg/db2access/ibmi/i400conn" // links the IBM i driver
	applog "milton_prism/pkg/log"
)

const storeRetry = 5 * time.Second

func main() {
	applog.InitLogger("nwc620")
	applog.ConfigureFromEnv()
	if err := run(); err != nil {
		applog.Fatal("nwc620: startup failed: " + err.Error())
	}
}

func run() error {
	ctx := context.Background()
	cfg, err := svcruntime.ConfigFromEnv("nwc620", os.Getenv)
	if err != nil {
		return err
	}
	clock, err := svcruntime.ClockFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	// DB2_COMMIT_MODE is required and has no default (ParseCommitMode inside the
	// loader refuses an empty one); the password stays a db2access.Secret.
	dbCfg, err := db2access.ConfigFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	tables, err := repositories.NewTables()
	if err != nil {
		return err
	}
	out, err := printer.FromEnv(os.Getenv, clock)
	if err != nil {
		return err
	}

	srv, err := svcruntime.NewServer(cfg)
	if err != nil {
		return err
	}
	var holder svcruntime.Holder[*db2access.Store]
	// NWC620 opens every file for input: no table is written, so none needs journaling.
	holder.Start(ctx, func(ctx context.Context) (*db2access.Store, error) {
		return db2access.Open(ctx, ibmi.Driver{}, dbCfg)
	}, storeRetry, svcruntime.WithPermanentErrors(isPermanent), svcruntime.WithOnPermanent(srv.Fail))
	srv.OnShutdownNamed("db2", holder.Close)

	ping, err := nwc620.Build(srv, nwc620.Deps{Store: &holder, Tables: tables, Printer: out, Clock: clock})
	if err != nil {
		return err
	}
	srv.SetReadiness(func(ctx context.Context) error {
		if err := holder.Ready(ctx); err != nil {
			return err
		}
		return ping(ctx)
	})
	return srv.Run(ctx)
}

// isPermanent reports the open failures retrying cannot fix: a rejected sign-on, an
// unauthorized profile, a CCSID mismatch or a configuration the driver refuses.
func isPermanent(err error) bool {
	var dbErr *db2access.Error
	if !errors.As(err, &dbErr) {
		// Not a database answer: the configuration (TLS roots, limits) was refused.
		return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	switch {
	case dbErr.SQLState == "28000", dbErr.SQLCode == -30082:
		return true
	case dbErr.SQLState == "42501", dbErr.SQLCode == -551, dbErr.SQLCode == -552:
		return true
	case dbErr.SQLState == db2access.SQLStateCCSIDMismatch:
		return true
	}
	return false
}
