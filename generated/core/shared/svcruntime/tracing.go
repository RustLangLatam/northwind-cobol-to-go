package svcruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	applog "milton_prism/pkg/log"
)

// maxOTLPResponseBytes caps what the exporter reads back from a collector.
const maxOTLPResponseBytes = 64 << 10

// setupTracing installs the global tracer provider, the W3C trace-context propagator
// (no Baggage: an unauthenticated caller must not inject baggage into the service's
// spans and logs) and an error handler that writes exporter failures to the platform
// log, once per distinct message per minute. An empty endpoint installs no exporter
// and returns a no-op shutdown: the service still starts, and the log says so.
func setupTracing(cfg Config) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetErrorHandler(newDedupErrorHandler(time.Minute, time.Now))
	if cfg.OTelTracesIgnored {
		applog.Warningf("svcruntime: %s is set but not supported: set %s (the base URL; /v1/traces is appended)", EnvOTelTracesEndpoint, EnvOTelEndpoint)
	}
	if cfg.OTelEndpoint == "" {
		applog.Infof("svcruntime: tracing disabled: %s is not set", EnvOTelEndpoint)
		return func(context.Context) error { return nil }, nil
	}
	endpoint, err := otlpTracesURL(cfg.OTelEndpoint, cfg.OTelInsecure)
	if err != nil {
		return nil, err
	}
	exp, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpointURL(endpoint),
		otlptracehttp.WithHTTPClient(otlpHTTPClient()),
		otlptracehttp.WithTimeout(10*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("svcruntime: OTLP exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", cfg.OTelServiceName))),
	)
	otel.SetTracerProvider(tp)
	host := "?"
	if u, err := url.Parse(endpoint); err == nil {
		host = u.Host
	}
	applog.Infof("svcruntime: tracing enabled: exporting to %s as service %q", host, cfg.OTelServiceName)
	return tp.Shutdown, nil
}

// otlpTracesURL validates OTEL_EXPORTER_OTLP_ENDPOINT and returns the traces URL
// (base path + /v1/traces). https is required, except for a loopback collector or
// with OTEL_EXPORTER_OTLP_INSECURE=true. Credentials in the URL are refused.
func otlpTracesURL(raw string, insecure bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("svcruntime: %s must be an absolute http(s) URL", EnvOTelEndpoint)
	}
	if u.User != nil {
		return "", fmt.Errorf("svcruntime: %s must not carry credentials", EnvOTelEndpoint)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !insecure && !isLoopback(u.Hostname()) {
			return "", fmt.Errorf("svcruntime: %s must use https for a non-loopback collector (or set %s=true)", EnvOTelEndpoint, EnvOTelInsecure)
		}
	default:
		return "", fmt.Errorf("svcruntime: %s must use https or http", EnvOTelEndpoint)
	}
	if blockedOTLPHost(u.Hostname()) {
		return "", fmt.Errorf("svcruntime: %s must not point at a link-local or metadata address", EnvOTelEndpoint)
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimSuffix(u.Path, "/") + "/v1/traces"
	return u.String(), nil
}

// blockedOTLPHost is a collector host that can only be an SSRF target: a link-local
// address (169.254.0.0/16, fe80::/10, which holds the cloud metadata services), the
// unspecified address, or a well-known metadata name.
func blockedOTLPHost(host string) bool {
	switch strings.ToLower(strings.TrimSuffix(host, ".")) {
	case "metadata.google.internal", "metadata", "instance-data":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && blockedOTLPIP(ip)
}

func blockedOTLPIP(ip net.IP) bool {
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() ||
		ip.Equal(net.ParseIP("fd00:ec2::254")) // AWS IPv6 metadata
}

var errBlockedAddr = errors.New("svcruntime: the OTLP collector resolves to a link-local or metadata address")

// refuseBlockedDial runs after name resolution, so a hostname that points at a
// metadata address is refused too.
func refuseBlockedDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip != nil && blockedOTLPIP(ip) {
		return errBlockedAddr
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// otlpHTTPClient never follows a redirect and caps every response body.
func otlpHTTPClient() *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil // spans and OTLP headers never go through an environment proxy
	base.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: refuseBlockedDial}).DialContext
	return &http.Client{
		Timeout:       10 * time.Second,
		Transport:     cappedTransport{base: base},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect },
	}
}

var errRedirect = errors.New("svcruntime: the OTLP collector answered with a redirect, which is not followed")

type cappedTransport struct{ base http.RoundTripper }

func (t cappedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = http.MaxBytesReader(nil, resp.Body, maxOTLPResponseBytes)
	return resp, nil
}

// dedupErrorHandler writes OpenTelemetry errors (a collector that is down fails every
// batch) to the platform log, and repeats of the same message within window are
// counted, not printed.
type dedupErrorHandler struct {
	window time.Duration
	now    func() time.Time

	mu         sync.Mutex
	lastMsg    string
	lastAt     time.Time
	suppressed int
}

func newDedupErrorHandler(window time.Duration, now func() time.Time) *dedupErrorHandler {
	return &dedupErrorHandler{window: window, now: now}
}

// Handle implements otel.ErrorHandler. The message is logged by type at Warning
// (exporter errors carry the collector URL and response text); the text is Debug.
func (h *dedupErrorHandler) Handle(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	h.mu.Lock()
	t := h.now()
	if msg == h.lastMsg && t.Sub(h.lastAt) < h.window {
		h.suppressed++
		h.mu.Unlock()
		return
	}
	n := h.suppressed
	h.lastMsg, h.lastAt, h.suppressed = msg, t, 0
	h.mu.Unlock()
	if n > 0 {
		applog.Warningf("svcruntime: telemetry: the previous error repeated %d more time(s)", n)
	}
	applog.Warningf("svcruntime: telemetry export error: %s", errKind(err))
	applog.Debugf("svcruntime: telemetry export error: cause=%q", msg)
}
