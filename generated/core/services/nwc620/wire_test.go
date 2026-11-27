package nwc620_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"milton_prism/core/services/nwc620"
	"milton_prism/core/services/nwc620/infrastructure/printer"
	"milton_prism/core/services/nwc620/infrastructure/repositories"
	"milton_prism/core/shared/svcruntime"
	"milton_prism/pkg/db2access"
	"milton_prism/pkg/db2access/db2fake"
	"milton_prism/pkg/db2access/ibmi"
)

type direct struct{ s *db2access.Store }

func (d direct) Do(fn func(*db2access.Store) error) error { return fn(d.s) }

func TestBuild_RegistersServiceAndReadiness(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"GRPC_ALLOW_PLAINTEXT": "true", "GRPC_AUTH_MODE": "token",
		"AUTH_TOKEN_PUBLIC_KEY": strings.Repeat("ab", 32), "AUTH_TOKEN_AUDIENCE": "nwc620",
		"GRPC_PORT": "0", "METRICS_PORT": "0",
	}
	cfg, err := svcruntime.ConfigFromEnv("nwc620", func(k string) string { return env[k] })
	require.NoError(t, err)
	srv, err := svcruntime.NewServer(cfg)
	require.NoError(t, err)

	tables, err := repositories.NewTables()
	require.NoError(t, err)
	fake := db2fake.New()
	fake.Declare(tables.All()...)
	store, err := db2access.Open(context.Background(), ibmi.Driver{Connect: fake.ConnectFunc()},
		db2access.Config{Host: "ibmi.example", Library: "NWLIB", User: "U", Password: "pw-for-tests",
			CommitMode: db2access.CommitNone, CCSID: 37})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	clock := svcruntime.NewClock(time.UTC, nil)
	ping, err := nwc620.Build(srv, nwc620.Deps{Store: direct{store}, Tables: tables,
		Printer: printer.LogPrinter{}, Clock: clock})
	require.NoError(t, err)
	assert.NoError(t, ping(context.Background()))
	_, ok := srv.GRPC().GetServiceInfo()["milton_prism.services.nwc620.v1.Nwc620Service"]
	assert.True(t, ok)

	fake.FailQuery("CODTBL", db2fake.DBError{SQLSTATE: "08001", SQLCODE: -30080})
	assert.Error(t, ping(context.Background()))

	// The counters are registered once per server.
	_, err = nwc620.Build(srv, nwc620.Deps{Store: direct{store}, Tables: tables, Printer: printer.LogPrinter{}, Clock: clock})
	assert.Error(t, err)
}
