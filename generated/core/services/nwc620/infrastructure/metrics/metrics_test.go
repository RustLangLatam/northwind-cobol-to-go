package metrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"milton_prism/core/services/nwc620/infrastructure/metrics"
)

func TestNew_RegistersOnce(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	c, err := metrics.New(reg)
	require.NoError(t, err)
	c.InvoicePrinted()
	c.OrderNotFound()
	_, err = metrics.New(reg)
	assert.Error(t, err, "a second registration of the same counters is refused")
}
