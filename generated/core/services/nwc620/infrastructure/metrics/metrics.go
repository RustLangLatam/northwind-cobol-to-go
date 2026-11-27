// Package metrics counts the invoices NWC620 prints. Integer counters only; the
// runtime already exports request count and latency per RPC.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"milton_prism/core/services/nwc620/ports"
)

var _ ports.Metrics = (*Counters)(nil)

// Counters holds the service's business counters.
type Counters struct {
	outcomes *prometheus.CounterVec
}

// New registers the counters on reg.
func New(reg prometheus.Registerer) (*Counters, error) {
	c := &Counters{outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nwc620_invoices_total",
		Help: "Invoice print requests by outcome (printed, order_not_found).",
	}, []string{"outcome"})}
	if err := reg.Register(c.outcomes); err != nil {
		return nil, err
	}
	return c, nil
}

// InvoicePrinted counts a printed invoice.
func (c *Counters) InvoicePrinted() { c.outcomes.WithLabelValues("printed").Inc() }

// OrderNotFound counts a request for an order that is not on file.
func (c *Counters) OrderNotFound() { c.outcomes.WithLabelValues("order_not_found").Inc() }
