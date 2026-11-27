package svcruntime

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	applog "milton_prism/pkg/log"
)

const healthInterval = 5 * time.Second // how often the gRPC health status follows readiness

// readyTimeout bounds one dependency check (a variable so tests can shorten it).
var readyTimeout = 3 * time.Second

func (s *Server) routeProbes() {
	s.mux.Handle("/metrics", s.protect(promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})))
	s.mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	s.mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.draining.Load() || s.ready.check() != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
}

// protect requires the METRICS_BEARER_TOKEN_FILE token on h when one is configured.
// /health and /readyz stay open: they say only up or down, and kubelet probes carry
// no credentials. Without a token file anyone who can reach the listener reads
// /metrics (method names, error rates, Go and process internals) and every
// HandleHTTP route, so a non-loopback METRICS_HOST should always set one.
func (s *Server) protect(h http.Handler) http.Handler {
	if len(s.metricsToken) == 0 {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), s.metricsToken) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// minMetricsTokenBytes is the shortest bearer token accepted for the metrics listener.
const minMetricsTokenBytes = 16

// readMetricsToken loads the bearer token from path (trailing newline trimmed).
func readMetricsToken(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("svcruntime: read %s: %w", EnvMetricsTokenFile, err)
	}
	b = bytes.TrimRight(b, "\r\n")
	if len(b) < minMetricsTokenBytes {
		return nil, fmt.Errorf("svcruntime: %s must hold a token of at least %d bytes", EnvMetricsTokenFile, minMetricsTokenBytes)
	}
	return b, nil
}

// readiness runs the dependency check at most once per ttl, whoever asks (/readyz
// hits and the health watcher share the result), and never more than one check at a
// time: while a check that ignores its context is still running, callers get
// errReadyBusy instead of a new goroutine.
type readiness struct {
	ttl time.Duration

	mu      sync.Mutex
	fn      func(context.Context) error
	at      time.Time
	last    error
	valid   bool
	known   bool        // a result was reported at least once (for transition logging)
	ok      bool        // the last reported result
	running atomic.Bool // a check goroutine is still alive
}

func newReadiness(ttl time.Duration) *readiness { return &readiness{ttl: ttl} }

func (r *readiness) set(fn func(context.Context) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fn, r.valid = fn, false
}

func (r *readiness) check() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fn == nil {
		return nil
	}
	if r.valid && time.Since(r.at) < r.ttl {
		return r.last
	}
	r.last, r.at, r.valid = r.run(r.fn), time.Now(), true
	r.report(r.last)
	return r.last
}

// report logs readiness transitions only: a steady state is not a log line per probe.
func (r *readiness) report(err error) {
	ok := err == nil
	if r.known && r.ok == ok {
		return
	}
	r.known, r.ok = true, ok
	if ok {
		applog.Info("svcruntime: readiness: ready")
		return
	}
	applog.Warningf("svcruntime: readiness: NOT ready: %s", errKind(err))
	applog.Debugf("svcruntime: readiness: NOT ready: cause=%q", err.Error())
}

// errReadyTimeout is the result of a dependency check that outlived readyTimeout.
var errReadyTimeout = errors.New("svcruntime: the readiness check timed out")

// errReadyBusy is the result while an earlier check that timed out is still running.
var errReadyBusy = errors.New("svcruntime: the previous readiness check has not returned")

// run is boundedCheck with one check in flight: r.mu is held by the caller.
func (r *readiness) run(fn func(context.Context) error) error {
	if !r.running.CompareAndSwap(false, true) {
		return errReadyBusy
	}
	return boundedCheck(fn, func() { r.running.Store(false) })
}

// boundedCheck runs fn for at most readyTimeout, even when fn ignores its context
// (its goroutine is then left to finish on its own; done runs when it does).
func boundedCheck(fn func(context.Context) error, done func()) error {
	ctx, cancel := context.WithTimeout(context.Background(), readyTimeout)
	defer cancel()
	res := make(chan error, 1)
	go func() {
		defer done()
		defer func() {
			if r := recover(); r != nil {
				res <- fmt.Errorf("svcruntime: the readiness check panicked: type=%T", r)
			}
		}()
		res <- fn(ctx)
	}()
	select {
	case err := <-res:
		return err
	case <-ctx.Done():
		return errReadyTimeout
	}
}
