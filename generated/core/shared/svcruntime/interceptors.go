package svcruntime

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	applog "milton_prism/pkg/log"
)

// recoverUnary turns a handler panic into Internal and logs it with its stack. The
// panic VALUE can carry request data, so only its type goes to the Error line; the
// value is logged at Debug.
func recoverUnary(panics prometheus.Counter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				logPanic(panics, info.FullMethod, r)
				resp, err = nil, status.Error(codes.Internal, msgInternal)
			}
		}()
		return h(ctx, req)
	}
}

func recoverStream(panics prometheus.Counter) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logPanic(panics, info.FullMethod, r)
				err = status.Error(codes.Internal, msgInternal)
			}
		}()
		return h(srv, ss)
	}
}

func logPanic(panics prometheus.Counter, method string, r any) {
	panics.Inc()
	applog.Errorf("svcruntime: panic in %s: type=%T\n%s", method, r, debug.Stack())
	applog.Debugf("svcruntime: panic in %s: value=%q", method, fmt.Sprint(r))
}

// errKind is what a server log line may say about an error: its type, the type of the
// innermost cause and, for a classified one, the code. Driver and SQL error text can
// hold values and personal data, so the full text is logged at Debug only.
func errKind(err error) string {
	root := err
	for {
		next := errors.Unwrap(root)
		if next == nil {
			break
		}
		root = next
	}
	code, _ := Classify(err)
	return fmt.Sprintf("type=%T root=%T kind=%s", err, root, code)
}

// logUnary tags the call's log lines with the request and trace ids and logs one
// line per call: method, code and duration, never the request or response. Health
// checks log at Debug.
func logUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	applog.SetRequestContext(requestID(ctx), trace.SpanContextFromContext(ctx).TraceID().String())
	defer applog.ClearRequestContext()
	start := time.Now()
	resp, err := h(ctx, req)
	logCall("method", info.FullMethod, status.Code(err), time.Since(start).Milliseconds())
	return resp, err
}

func requestID(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("x-request-id"); len(v) > 0 && safeID(v[0]) {
			return v[0]
		}
	}
	return uuid.NewString()
}

// logStream is logUnary for streams: one line per call with method, code and duration.
func logStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
	ctx := ss.Context()
	applog.SetRequestContext(requestID(ctx), trace.SpanContextFromContext(ctx).TraceID().String())
	defer applog.ClearRequestContext()
	start := time.Now()
	err := h(srv, ss)
	logCall("stream", info.FullMethod, status.Code(err), time.Since(start).Milliseconds())
	return err
}

// logCall writes the one line of a finished call (health checks at Debug).
func logCall(kind, method string, code codes.Code, ms int64) {
	switch {
	case strings.HasPrefix(method, "/grpc.health.v1.Health/"):
		applog.Debugf("rpc %s=%s code=%s duration_ms=%d", kind, method, code, ms)
	case code == codes.Internal || code == codes.Unknown || code == codes.DataLoss:
		applog.Errorf("rpc %s=%s code=%s duration_ms=%d", kind, method, code, ms)
	default:
		applog.Infof("rpc %s=%s code=%s duration_ms=%d", kind, method, code, ms)
	}
}

// logCause records why a call failed: the type of the error for Internal and
// Unavailable (the code the operator must act on), the full text at Debug for every
// mapped error, including the Canceled and DeadlineExceeded causes the caller never sees.
func logCause(kind, method string, mapped codes.Code, err error) {
	if mapped == codes.Internal || mapped == codes.Unavailable {
		applog.Errorf("rpc %s=%s code=%s cause: %s", kind, method, mapped, errKind(err))
	}
	applog.Debugf("rpc %s=%s code=%s cause=%q", kind, method, mapped, err.Error())
}

// statusUnary maps every error a handler returns with StatusError, logging the
// cause of an Internal or Unavailable on the server side only.
func statusUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	resp, err := h(ctx, req)
	if err == nil {
		return resp, nil
	}
	mapped := StatusError(err)
	logCause("method", info.FullMethod, status.Code(mapped), err)
	return nil, mapped
}

// statusStream maps a stream handler's error like statusUnary. Streams get no
// default deadline: a stream may legitimately outlive GRPC_DEFAULT_TIMEOUT.
func statusStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
	err := h(srv, ss)
	if err == nil {
		return nil
	}
	mapped := StatusError(err)
	logCause("stream", info.FullMethod, status.Code(mapped), err)
	return mapped
}

// deadlineUnary gives a call without a deadline the default one.
func deadlineUnary(d time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
		return h(ctx, req)
	}
}

// safeID accepts a caller-supplied request id only when it cannot forge a log line.
func safeID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
