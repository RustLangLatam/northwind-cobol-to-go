package svcruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"

	applog "milton_prism/pkg/log"
)

// Server is a configured gRPC server plus its probe and metrics listener.
type Server struct {
	cfg      Config
	grpc     *grpc.Server
	health   *health.Server
	registry *prometheus.Registry
	metrics  *grpcprom.ServerMetrics
	ready    *readiness
	mux      *http.ServeMux

	methodScopes map[string][]string // WithMethodScopes: checked against the registered methods at Run

	draining     atomic.Bool // set when shutdown starts: /readyz answers 503
	metricsToken []byte      // METRICS_BEARER_TOKEN_FILE; empty: the metrics routes are open
	fatal        chan error  // Fail: stop Run with this error
	exit         func(int)   // called on a second termination signal; os.Exit
	pathsMu      sync.Mutex
	paths        map[string]bool

	mu        sync.Mutex
	shutdowns []shutdownStep
}

type shutdownStep struct {
	name string
	fn   func(context.Context) error
}

// Errors Run returns (joined with the others) when shutdown did not complete cleanly.
var (
	// ErrGracefulStopTimeout: open calls were still running after SHUTDOWN_TIMEOUT and
	// were canceled.
	ErrGracefulStopTimeout = errors.New("svcruntime: graceful stop timed out; open calls were canceled")
	// ErrStopIncomplete: after the forced stop, handlers that ignore their context were
	// still running when the shutdown functions started.
	ErrStopIncomplete = errors.New("svcruntime: handlers were still running after the forced stop")
)

// stopWaitMax bounds how long Run waits for the forced Stop to finish.
const stopWaitMax = 5 * time.Second

// notifySignals and stopSignals are signal.Notify and signal.Stop, as variables so a
// test can assert that Run unregisters its channel (C45): the test binary keeps its own
// SIGTERM listener (TestMain), which would hide a missing Stop.
var (
	notifySignals = signal.Notify
	stopSignals   = signal.Stop
)

// Option adjusts NewServer.
type Option func(*options)

type options struct {
	unary        []grpc.UnaryServerInterceptor
	stream       []grpc.StreamServerInterceptor
	revoked      RevocationCheck
	methodScopes map[string][]string
	now          func() time.Time
}

// WithUnaryInterceptors appends interceptors after the platform chain's
// authentication and before the innermost recovery. The whole order, outermost
// first: recovery, one log line per call, metrics, status mapping, the default
// deadline, authentication and authorization, YOUR interceptors, recovery. A panic in
// the platform interceptors or in yours is caught by the outer recovery (logged with
// its stack, counted, answered Internal), never fatal to the process; your
// interceptors see the Caller (CallerFromContext).
func WithUnaryInterceptors(i ...grpc.UnaryServerInterceptor) Option {
	return func(o *options) { o.unary = append(o.unary, i...) }
}

// WithStreamInterceptors appends stream interceptors in the same place as
// WithUnaryInterceptors (there is no default deadline on streams).
func WithStreamInterceptors(i ...grpc.StreamServerInterceptor) Option {
	return func(o *options) { o.stream = append(o.stream, i...) }
}

// WithRevocationCheck adds a revocation check to token authentication (see
// RevocationCheck). Without one, revocation is by expiry only, capped by
// AUTH_TOKEN_MAX_LIFETIME (at most 30 days).
//
// A unary call is checked on every call. A stream is checked once, when it opens: a
// token revoked afterwards keeps its open stream until the stream ends, the token
// expires, or the connection reaches its maximum age (30m plus 30s grace), whichever
// comes first. Services that must cut a stream at revocation re-check inside the
// handler (CallerFromContext gives the caller to check).
func WithRevocationCheck(f RevocationCheck) Option {
	return func(o *options) { o.revoked = f }
}

// WithMethodScopes requires every listed scope of a token for the full method name
// ("/pkg.Service/Method"), on top of AUTH_TOKEN_REQUIRED_SCOPES. Token mode only.
func WithMethodScopes(m map[string][]string) Option {
	return func(o *options) {
		o.methodScopes = make(map[string][]string, len(m))
		for k, v := range m {
			o.methodScopes[k] = append([]string(nil), v...)
		}
	}
}

// withClock replaces the clock the authentication uses (tests).
func withClock(now func() time.Time) Option { return func(o *options) { o.now = now } }

// NewServer builds the gRPC server from cfg: TLS (or the explicit plaintext opt-in,
// logged as a warning), authentication on every RPC except health checks, a default
// deadline, error mapping (StatusError), stream and keepalive limits, panic recovery
// with stack, one log line per call, Prometheus metrics, OpenTelemetry tracing and
// the gRPC health service. Register the service on GRPC() and call Run.
func NewServer(cfg Config, opts ...Option) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.now == nil {
		o.now = time.Now
	}
	if len(o.methodScopes) > 0 && cfg.AuthMode != AuthToken {
		return nil, fmt.Errorf("svcruntime: WithMethodScopes needs %s=token", EnvAuthMode)
	}
	auth, err := newAuthenticator(cfg, o)
	if err != nil {
		return nil, err
	}
	var metricsToken []byte
	if cfg.MetricsTokenFile != "" {
		if metricsToken, err = readMetricsToken(cfg.MetricsTokenFile); err != nil {
			return nil, err
		}
	} else if !loopbackAddr(cfg.MetricsAddr) { // Validate allowed it: the operator opted in
		applog.Warningf("svcruntime: %s=true: the metrics listener %s is reachable beyond this host and unauthenticated: /metrics exposes method names, error rates and process internals; keep it behind a NetworkPolicy or set %s", EnvMetricsAllowUnauthenticated, cfg.MetricsAddr, EnvMetricsTokenFile)
	}
	if cfg.MetricsTokenFile != "" && !loopbackAddr(cfg.MetricsAddr) {
		applog.Warningf("svcruntime: the metrics bearer token travels in cleartext: the metrics listener %s speaks plain HTTP, so terminate TLS in front of it (mesh, ingress) or keep it on the pod network", cfg.MetricsAddr)
	}
	reg := prometheus.NewRegistry()
	srvMetrics := grpcprom.NewServerMetrics(grpcprom.WithServerHandlingTimeHistogram())
	panics := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "grpc_server_panics_recovered_total",
		Help: "Handler panics recovered by the runtime.",
	})
	reg.MustRegister(srvMetrics, panics, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Recovery is both outermost and innermost. The inner one logs a handler panic with
	// the request id and maps it like any Internal error; the outer one catches a panic
	// anywhere else (authentication, a validator, logging, a service interceptor), so a
	// malformed request cannot kill the process before it is authenticated.
	unary := append([]grpc.UnaryServerInterceptor{
		recoverUnary(panics),
		logUnary,
		srvMetrics.UnaryServerInterceptor(),
		statusUnary,
		deadlineUnary(cfg.DefaultTimeout),
		authUnary(auth, o, cfg),
	}, o.unary...)
	unary = append(unary, recoverUnary(panics))
	stream := append([]grpc.StreamServerInterceptor{
		recoverStream(panics),
		logStream,
		srvMetrics.StreamServerInterceptor(),
		statusStream,
		authStream(auth, o, cfg),
	}, o.stream...)
	stream = append(stream, recoverStream(panics))

	sopts := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(unary...),
		grpc.ChainStreamInterceptor(stream...),
		grpc.MaxConcurrentStreams(cfg.MaxConcurrentStreams),
		grpc.MaxRecvMsgSize(cfg.MaxRecvMsgBytes),
		grpc.WaitForHandlers(true), // the forced Stop waits for handlers; Run bounds that wait
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 30 * time.Second}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     5 * time.Minute,
			MaxConnectionAge:      30 * time.Minute,
			MaxConnectionAgeGrace: 30 * time.Second,
			Time:                  2 * time.Minute,
			Timeout:               20 * time.Second,
		}),
	}
	if cfg.TLSCertFile != "" {
		tc, err := serverTLS(cfg)
		if err != nil {
			return nil, err
		}
		sopts = append(sopts, grpc.Creds(credentials.NewTLS(tc)))
	} else {
		applog.Warningf("svcruntime: %s=true: the gRPC server runs WITHOUT TLS; never do this outside local development", EnvAllowPlaintext)
	}
	// Tracing last: it installs global state, so nothing may fail after it.
	shutdownTracing, err := setupTracing(cfg)
	if err != nil {
		return nil, err
	}

	gs := grpc.NewServer(sopts...)
	hs := health.NewServer()
	healthgrpc.RegisterHealthServer(gs, hs)
	hs.SetServingStatus("", healthgrpc.HealthCheckResponse_NOT_SERVING)

	s := &Server{
		cfg:      cfg,
		grpc:     gs,
		health:   hs,
		registry: reg,
		metrics:  srvMetrics,
		ready:    newReadiness(cfg.ReadyCacheTTL),
		mux:      http.NewServeMux(),

		methodScopes: o.methodScopes,
		metricsToken: metricsToken,
		fatal:        make(chan error, 1),
		exit:         os.Exit,
		paths:        map[string]bool{"/metrics": true, "/health": true, "/readyz": true},
	}
	s.OnShutdownNamed("tracing", shutdownTracing)
	s.routeProbes()
	return s, nil
}

// GRPC is the server to register the service on.
func (s *Server) GRPC() *grpc.Server { return s.grpc }

// Registry is the Prometheus registry served on /metrics; register the service's
// own counters on it.
func (s *Server) Registry() *prometheus.Registry { return s.registry }

// SetReadiness sets the dependency check behind /readyz and the gRPC health status
// (for example the store holder's Ready plus a ping). Without one the service is
// ready as soon as it serves.
func (s *Server) SetReadiness(check func(context.Context) error) { s.ready.set(check) }

// HandleHTTP serves h at path on the probe and metrics listener, next to /metrics,
// /health and /readyz, behind the METRICS_BEARER_TOKEN_FILE token when one is set.
// Call it before Run. It returns an error, and registers nothing, for an empty or
// relative path, a nil handler, a path with a space, method or wildcard, and a path
// already taken (including the three built-in ones).
func (s *Server) HandleHTTP(path string, h http.Handler) error {
	switch {
	case h == nil:
		return fmt.Errorf("svcruntime: HandleHTTP %q: nil handler", path)
	case !strings.HasPrefix(path, "/") || path == "/" || strings.ContainsAny(path, " {}\t\n"):
		return fmt.Errorf("svcruntime: HandleHTTP %q: must be an absolute path other than \"/\", without spaces, methods or wildcards", path)
	}
	s.pathsMu.Lock()
	defer s.pathsMu.Unlock()
	if s.paths[path] {
		return fmt.Errorf("svcruntime: HandleHTTP %q: path already registered", path)
	}
	s.paths[path] = true
	s.mux.Handle(path, s.protect(h))
	return nil
}

// OnShutdown is OnShutdownNamed with a generated name ("hook-N", N from 1).
func (s *Server) OnShutdown(fn func(context.Context) error) {
	s.mu.Lock()
	n := len(s.shutdowns)
	s.mu.Unlock()
	s.OnShutdownNamed("hook-"+strconv.Itoa(n), fn)
}

// OnShutdownNamed registers fn to run after the gRPC server and the probe listener
// have stopped (close a store holder, flush a writer). Functions run in reverse order
// of registration; tracing, registered first by NewServer, is flushed last. name
// appears in the log and in the error Run returns when fn fails.
func (s *Server) OnShutdownNamed(name string, fn func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdowns = append(s.shutdowns, shutdownStep{name: name, fn: fn})
}

// Fail stops Run: it shuts down in order and returns err (joined with any shutdown
// error). Use it as the Holder's WithOnPermanent function. Only the first call counts.
func (s *Server) Fail(err error) {
	select {
	case s.fatal <- err:
	default:
	}
}

// notify cancels the returned context on the first termination signal and exits the
// process on the second, so a stuck shutdown can always be forced from the outside.
func (s *Server) notify(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	sigs := make(chan os.Signal, 2)
	notifySignals(sigs, terminationSignals...)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-done:
			return
		case <-parent.Done():
			return
		}
		select {
		case <-sigs:
			applog.Error("svcruntime: second termination signal during shutdown: exiting now")
			s.exit(1)
		case <-done:
		}
	}()
	return ctx, func() {
		stopSignals(sigs)
		close(done)
		cancel()
	}
}

// Run serves until ctx ends, SIGINT or SIGTERM arrives, a listener fails or Fail is
// called. Then it shuts down in order: health NOT_SERVING, gRPC GracefulStop bounded
// by SHUTDOWN_TIMEOUT (then a forced Stop that is awaited, bounded), the probe and
// metrics listener, and the OnShutdown functions. A second termination signal exits
// the process at once. Run returns nil after a clean shutdown, and otherwise the
// listener error, the Fail error, ErrGracefulStopTimeout, ErrStopIncomplete and the
// failure of every shutdown step, joined.
func (s *Server) Run(ctx context.Context) error {
	ctx, stop := s.notify(ctx)
	defer stop()

	if err := s.checkMethodScopes(); err != nil {
		return errors.Join(err, s.runShutdowns())
	}
	gl, err := net.Listen("tcp", s.cfg.GRPCAddr)
	if err != nil {
		return errors.Join(fmt.Errorf("svcruntime: listen %s: %w", EnvGRPCPort, err), s.runShutdowns())
	}
	pl, err := net.Listen("tcp", s.cfg.MetricsAddr)
	if err != nil {
		_ = gl.Close()
		return errors.Join(fmt.Errorf("svcruntime: listen %s: %w", EnvMetricsPort, err), s.runShutdowns())
	}
	for _, l := range []net.Listener{gl, pl} {
		if key, ok := ownPort(l.Addr()); ok {
			defer ownedPorts.Delete(key)
		}
	}
	probes := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.metrics.InitializeMetrics(s.grpc) // the service is registered by now: zero series per method
	errs := make(chan error, 2)
	go func() { errs <- s.grpc.Serve(gl) }()
	go func() {
		if err := probes.Serve(pl); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	applog.Infof("svcruntime: %s serving gRPC on %s, probes and metrics on %s", s.cfg.ServiceName, gl.Addr(), pl.Addr())
	proc, recheck, check := selfProc, listenerRecheck, listenerCheck // read here: an abandoned watcher (C88) must not race a test's write
	if err := check(proc); err != nil {
		s.Fail(err)
	}

	watchCtx, stopWatch := context.WithCancel(ctx)
	watched, listened := make(chan struct{}), make(chan struct{})
	go func() { defer close(watched); s.watchHealth(watchCtx) }()
	go func() { defer close(listened); s.watchListeners(watchCtx, proc, recheck, check) }()

	var runErr error
	select {
	case <-ctx.Done():
		applog.Warning("svcruntime: shutting down")
	case runErr = <-s.fatal:
		applog.Errorf("svcruntime: shutting down after a fatal error: %s", errKind(runErr))
	case runErr = <-errs:
		applog.Errorf("svcruntime: a listener failed, shutting down: %s", errKind(runErr))
	}
	stopWatch()
	select { // the watcher waits at most one bounded readiness check
	case <-watched:
	case <-time.After(readyTimeout + time.Second):
	}
	// The listener watcher reads /proc and may call Fail: wait for it, so no tick runs
	// against a stopped server or after ownedPorts is cleared (C76).
	select {
	case <-listened:
	case <-time.After(listenerStopTimeout): // a /proc read that hangs must not hang Run (C88)
		applog.Warning("svcruntime: the listener check did not return in time; shutting down without it")
	}
	return errors.Join(runErr, s.shutdown(probes))
}

// checkMethodScopes fails when a WithMethodScopes key is not a method registered on the
// gRPC server (C56): a mistyped name enforces nothing and looks like protection. The
// health Check is exempt from authentication, so a scope on it could never apply.
func (s *Server) checkMethodScopes() error {
	if len(s.methodScopes) == 0 {
		return nil
	}
	registered := map[string]bool{}
	for svc, info := range s.grpc.GetServiceInfo() {
		for _, m := range info.Methods {
			registered["/"+svc+"/"+m.Name] = true
		}
	}
	var bad []string
	for k := range s.methodScopes {
		if k == healthCheck || !registered[k] {
			bad = append(bad, strconv.Quote(k))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("svcruntime: WithMethodScopes names method(s) that are not registered on the server, or are exempt from authentication: %s; register the service before Run and spell each key as /package.Service/Method", strings.Join(bad, ", "))
}

// shutdown stops everything in order and returns what did not go cleanly.
func (s *Server) shutdown(probes *http.Server) error {
	var errs []error
	s.draining.Store(true)
	s.health.Shutdown() // NOT_SERVING before anything stops
	stopped := make(chan struct{})
	go func() { s.grpc.GracefulStop(); close(stopped) }()
	timer := time.NewTimer(s.cfg.ShutdownTimeout)
	select {
	case <-stopped:
		timer.Stop()
	case <-timer.C:
		// Stop cancels every open call. It runs in its own goroutine because it can
		// block behind the pending GracefulStop and behind a handler that ignores its
		// context; Run waits for it, but only for a bounded time.
		applog.Warning("svcruntime: graceful stop timed out; closing open calls")
		errs = append(errs, ErrGracefulStopTimeout)
		forced := make(chan struct{})
		go func() { s.grpc.Stop(); close(forced) }()
		wait := min(s.cfg.ShutdownTimeout, stopWaitMax)
		select {
		case <-forced:
		case <-time.After(wait):
			applog.Errorf("svcruntime: handlers still running %s after the forced stop; closing the stores anyway", wait)
			errs = append(errs, ErrStopIncomplete)
		}
	}
	pctx, pcancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer pcancel()
	if err := probes.Shutdown(pctx); err != nil {
		applog.Warningf("svcruntime: probe listener shutdown: %s", errKind(err))
		errs = append(errs, fmt.Errorf("svcruntime: probe listener shutdown: %w", err))
	}
	return errors.Join(append(errs, s.runShutdowns())...)
}

// runShutdowns runs the OnShutdown functions, newest first, with their own
// SHUTDOWN_TIMEOUT, and returns the failures, each named.
func (s *Server) runShutdowns() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
	s.mu.Lock()
	steps := append([]shutdownStep(nil), s.shutdowns...)
	s.mu.Unlock()
	var errs []error
	for i := len(steps) - 1; i >= 0; i-- {
		if err := steps[i].fn(ctx); err != nil {
			applog.Warningf("svcruntime: shutdown step %q failed: %s", steps[i].name, errKind(err))
			applog.Debugf("svcruntime: shutdown step %q failed: cause=%q", steps[i].name, err.Error())
			errs = append(errs, fmt.Errorf("svcruntime: shutdown step %q: %w", steps[i].name, err))
		}
	}
	return errors.Join(errs...)
}

// watchListeners looks again every listenerRecheck for a listener the runtime does not
// own, and stops the service when it finds one (C51).
func (s *Server) watchListeners(ctx context.Context, proc string, every time.Duration, check func(string) error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := check(proc); err != nil {
				s.Fail(err)
				return
			}
		}
	}
}

// watchHealth keeps the gRPC health status in step with readiness until ctx ends, and
// logs the transitions.
func (s *Server) watchHealth(ctx context.Context) {
	t := time.NewTicker(healthInterval)
	defer t.Stop()
	last := healthgrpc.HealthCheckResponse_UNKNOWN
	for {
		st := healthgrpc.HealthCheckResponse_SERVING
		if s.ready.check() != nil {
			st = healthgrpc.HealthCheckResponse_NOT_SERVING
		}
		s.health.SetServingStatus("", st)
		if st != last {
			applog.Infof("svcruntime: gRPC health status is %s", st)
			last = st
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
