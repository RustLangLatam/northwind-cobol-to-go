package nwc700_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"milton_prism/core/services/nwc700"
	"milton_prism/core/services/nwc700/infrastructure/output"
	"milton_prism/core/shared/svcruntime"
	"milton_prism/pkg/db2access"
)

func TestBuild_RegistersServiceAndMetrics(t *testing.T) {
	env := map[string]string{
		svcruntime.EnvAllowPlaintext:     "true",
		svcruntime.EnvGRPCPort:           "0",
		svcruntime.EnvMetricsPort:        "0",
		svcruntime.EnvAuthMode:           "token",
		svcruntime.EnvAuthTokenPublicKey: strings.Repeat("ab", 32),
		svcruntime.EnvAuthTokenAudience:  "nwc700",
	}
	cfg, err := svcruntime.ConfigFromEnv("nwc700", func(k string) string { return env[k] })
	require.NoError(t, err)
	srv, err := svcruntime.NewServer(cfg)
	require.NoError(t, err)
	holder := &svcruntime.Holder[*db2access.Store]{}
	clock := svcruntime.NewClock(time.UTC, nil)
	require.NoError(t, nwc700.Build(srv, holder, "NWLIB", output.LogPrinter{}, clock))
	_, ok := srv.GRPC().GetServiceInfo()["milton_prism.services.nwc700.v1.Nwc700Service"]
	assert.True(t, ok)
	// The collectors are registered once: a second Build on the same registry fails.
	assert.Error(t, nwc700.Build(srv, holder, "NWLIB", output.LogPrinter{}, clock))
	assert.Error(t, nwc700.Build(srv, holder, "BAD LIB", output.LogPrinter{}, clock))
}
