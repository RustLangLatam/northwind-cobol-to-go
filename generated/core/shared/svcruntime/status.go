package svcruntime

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"milton_prism/core/shared/domainerr"
)

// Messages the caller sees for errors whose text must stay on the server.
const (
	msgInternal    = "internal error"
	msgUnavailable = "a dependency of the service is unavailable; retry later"
	msgCanceled    = "the request was canceled"
	msgDeadline    = "the request deadline was exceeded"
)

// StatusError maps err to a gRPC status error:
//   - a status error returned as is (not wrapped) keeps its code; Internal, Unknown,
//     DataLoss and Unavailable get the constant message;
//   - domainerr NotFound, InvalidArgument, FailedPrecondition, AlreadyExists,
//     PermissionDenied, ResourceExhausted, Aborted -> the same code with the error's
//     public message (the domain's kind wins over a context error it wraps);
//   - context.Canceled -> Canceled, context.DeadlineExceeded -> DeadlineExceeded;
//   - domainerr Unavailable, ErrNotReady, driver.ErrBadConn, a net.Error, or a wrapped
//     status Unavailable from a peer -> Unavailable;
//   - anything else -> Internal.
//
// Unavailable, Canceled, DeadlineExceeded and Internal carry a constant message: the
// cause never reaches the caller. Wrap causes with %w so this chain is visible.
func StatusError(err error) error {
	if err == nil {
		return nil
	}
	if se, ok := err.(interface{ GRPCStatus() *status.Status }); ok {
		// A status the handler built: its code stands, but a code that means "the
		// server failed" gets the constant message, so a cause put there never leaks.
		switch c := se.GRPCStatus().Code(); c {
		case codes.Internal, codes.Unknown, codes.DataLoss:
			return status.Error(codes.Internal, msgInternal)
		case codes.Unavailable:
			return status.Error(codes.Unavailable, msgUnavailable)
		}
		return err
	}
	return status.Error(Classify(err))
}

// Classify returns the code and caller-visible message StatusError uses for err.
func Classify(err error) (codes.Code, string) {
	// The domain's own classification is the most specific signal, so it goes first.
	switch kind, msg := domainerr.KindOf(err); kind {
	case domainerr.NotFound:
		return codes.NotFound, msg
	case domainerr.InvalidArgument:
		return codes.InvalidArgument, msg
	case domainerr.FailedPrecondition:
		return codes.FailedPrecondition, msg
	case domainerr.AlreadyExists:
		return codes.AlreadyExists, msg
	case domainerr.PermissionDenied:
		return codes.PermissionDenied, msg
	case domainerr.ResourceExhausted:
		return codes.ResourceExhausted, msg
	case domainerr.Aborted:
		return codes.Aborted, msg
	case domainerr.Unavailable:
		return codes.Unavailable, msgUnavailable
	}
	switch {
	case errors.Is(err, context.Canceled):
		return codes.Canceled, msgCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return codes.DeadlineExceeded, msgDeadline
	}
	var netErr net.Error
	var peer interface{ GRPCStatus() *status.Status }
	switch {
	case errors.Is(err, ErrNotReady), errors.Is(err, driver.ErrBadConn), errors.As(err, &netErr):
		return codes.Unavailable, msgUnavailable
	case errors.As(err, &peer):
		switch c := peer.GRPCStatus().Code(); c {
		case codes.Unavailable:
			return codes.Unavailable, msgUnavailable
		case codes.DeadlineExceeded:
			return codes.DeadlineExceeded, msgDeadline
		case codes.Canceled:
			return codes.Canceled, msgCanceled
		}
	}
	return codes.Internal, msgInternal
}
