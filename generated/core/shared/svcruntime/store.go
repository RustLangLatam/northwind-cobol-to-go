package svcruntime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	applog "milton_prism/pkg/log"
)

// ErrNotReady is returned while a store is not connected (or after it was closed).
// StatusError maps it to Unavailable.
var ErrNotReady = errors.New("svcruntime: the store is not connected")

// DefaultEscalateAfter is how many consecutive failed connects are logged at Warning
// before each further one is logged at Error.
const DefaultEscalateAfter = 5

// StartOption adjusts Holder.Start.
type StartOption func(*startOptions)

type startOptions struct {
	permanent     func(error) bool
	onPermanent   func(error)
	escalateAfter int
}

// WithPermanentErrors tells Start which open errors retrying cannot fix (a rejected
// password, a bad DSN, a TLS or CCSID mismatch). The first one stops the retries: the
// holder stays not ready, Err returns the cause, and the WithOnPermanent function runs.
// Without a classifier every error is retried forever, which only warns.
func WithPermanentErrors(isPermanent func(error) bool) StartOption {
	return func(o *startOptions) { o.permanent = isPermanent }
}

// WithOnPermanent runs fn once when a permanent error stops the connect. Pass
// Server.Fail so the service stops instead of idling at not-ready.
func WithOnPermanent(fn func(error)) StartOption {
	return func(o *startOptions) { o.onPermanent = fn }
}

// WithEscalateAfter sets after how many consecutive failures a retry is logged at
// Error instead of Warning (default DefaultEscalateAfter).
func WithEscalateAfter(n int) StartOption {
	return func(o *startOptions) { o.escalateAfter = n }
}

// Holder owns one store connection (a *db2access.Store, a database handle, any
// io.Closer). The service starts before the store answers: Start connects in the
// background, retrying; Do fails with ErrNotReady until it succeeds. Close cancels
// a pending connect, waits for it, closes a store that connected late, and waits for
// every in-flight Do before closing the connected one.
type Holder[T io.Closer] struct {
	closed atomic.Bool // set first by Close: no new Do starts

	life   sync.Mutex // guards cancel and done (Start, Close)
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.RWMutex // guards store and connected; Do holds it for reading
	store     T
	connected bool

	perm atomic.Pointer[error] // the permanent open error, once classified
}

// Err returns the permanent error that stopped the connect, or nil.
func (h *Holder[T]) Err() error {
	if p := h.perm.Load(); p != nil {
		return *p
	}
	return nil
}

// Start connects in the background with open, retrying every retry until it
// succeeds, ctx ends or Close is called. A second call, or a call after Close, does
// nothing.
func (h *Holder[T]) Start(ctx context.Context, open func(context.Context) (T, error), retry time.Duration, opts ...StartOption) {
	so := startOptions{escalateAfter: DefaultEscalateAfter}
	for _, o := range opts {
		o(&so)
	}
	h.life.Lock()
	defer h.life.Unlock()
	if h.closed.Load() || h.done != nil {
		applog.Warning("svcruntime: Holder.Start called twice or after Close; ignored")
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	h.cancel, h.done = cancel, make(chan struct{})
	go h.connect(ctx, open, retry, h.done, so)
}

func (h *Holder[T]) connect(ctx context.Context, open func(context.Context) (T, error), retry time.Duration, done chan struct{}, so startOptions) {
	defer close(done)
	for attempt := 1; ; attempt++ {
		s, err := open(ctx)
		if err == nil {
			h.mu.Lock()
			if h.closed.Load() {
				h.mu.Unlock()
				if cerr := s.Close(); cerr != nil {
					applog.Warningf("svcruntime: closing a store that connected after shutdown: %s", errKind(cerr))
				}
				return
			}
			h.store, h.connected = s, true
			h.mu.Unlock()
			applog.Info("svcruntime: store connected")
			return
		}
		if ctx.Err() != nil {
			return
		}
		if so.permanent != nil && so.permanent(err) {
			h.perm.Store(&err)
			applog.Errorf("svcruntime: the store cannot be opened and retrying will not help: %s", errKind(err))
			applog.Debugf("svcruntime: the store cannot be opened: cause=%q", err.Error())
			if so.onPermanent != nil {
				so.onPermanent(err)
			}
			return
		}
		logf := applog.Warningf
		if attempt >= so.escalateAfter {
			logf = applog.Errorf
		}
		logf("svcruntime: store not reachable (attempt %d), retrying in %s: %s", attempt, retry, errKind(err))
		applog.Debugf("svcruntime: store not reachable: cause=%q", err.Error())
		t := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// Do runs fn with the connected store. The store cannot be closed while fn runs;
// fn must not call Do or Ready of the same holder.
func (h *Holder[T]) Do(fn func(T) error) error {
	if h.closed.Load() {
		return ErrNotReady
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if !h.connected {
		return ErrNotReady
	}
	return fn(h.store)
}

// Ready returns nil once the store is connected, ErrNotReady before and after Close.
// It does not wait for in-flight queries (they hold the lock for reading too).
func (h *Holder[T]) Ready(context.Context) error {
	if h.closed.Load() {
		return ErrNotReady
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if !h.connected {
		return ErrNotReady
	}
	return nil
}

// Close stops a pending connect and closes the store. It waits for the connect
// goroutine and for in-flight Do calls, bounded by ctx; a store that connects, or a
// query that finishes, after ctx ended is still closed afterwards.
//
// Known limit: a Do whose fn never returns keeps the helper goroutine that waits for
// the write lock alive for the life of the process, and the store is never closed. The
// process is shutting down by then; give every query a context deadline so fn returns.
func (h *Holder[T]) Close(ctx context.Context) error {
	h.closed.Store(true)
	h.life.Lock()
	cancel, done := h.cancel, h.done
	h.life.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	closed := make(chan error, 1)
	go func() {
		h.mu.Lock() // waits for every in-flight Do
		defer h.mu.Unlock()
		if !h.connected {
			closed <- nil
			return
		}
		h.connected = false
		closed <- h.store.Close()
	}()
	select {
	case err := <-closed:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
