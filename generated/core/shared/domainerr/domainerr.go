// Package domainerr is the typed error a generated service's domain and application
// layers return. It imports only the standard library, so the domain never depends
// on gRPC or on generated protobuf code; the transport maps the kind to a status
// (svcruntime.StatusError) and sends the public message only, never the cause.
package domainerr

import "errors"

// Kind classifies an error for the transport.
type Kind uint8

const (
	// Internal is every error that carries no kind: the caller sees a constant message.
	Internal Kind = iota
	// NotFound: the requested record does not exist.
	NotFound
	// InvalidArgument: the request is outside what the program accepts.
	InvalidArgument
	// FailedPrecondition: the request is valid but the system state refuses it.
	FailedPrecondition
	// AlreadyExists: the record to create exists.
	AlreadyExists
	// Unavailable: a dependency (database, file system, peer) cannot be reached now.
	Unavailable
	// PermissionDenied: the caller is authenticated but may not do this.
	PermissionDenied
	// ResourceExhausted: a quota or limit of the program is reached.
	ResourceExhausted
	// Aborted: a concurrency conflict (the record changed); the caller may retry.
	Aborted
)

// Error is a classified error. Msg is the text the caller may see; Cause stays on the
// server side (logs, errors.Is/As).
type Error struct {
	Kind  Kind
	Msg   string
	Cause error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Msg
	}
	return e.Msg + ": " + e.Cause.Error()
}

func (e *Error) Unwrap() error { return e.Cause }

// New returns a classified error with no cause.
func New(kind Kind, msg string) error { return &Error{Kind: kind, Msg: msg} }

// Wrap returns a classified error that keeps cause in the chain.
func Wrap(kind Kind, msg string, cause error) error {
	return &Error{Kind: kind, Msg: msg, Cause: cause}
}

// KindOf returns the kind and public message of the first *Error in err's chain that
// carries a kind (anything but Internal), searched outermost first (joined errors in
// order), or Internal and "" when none does. A kindless *Error, such as a wrapper that
// only adds context, therefore never hides the kind of an error it wraps.
func KindOf(err error) (Kind, string) {
	if e := firstClassified(err); e != nil {
		return e.Kind, e.Msg
	}
	return Internal, ""
}

// Is reports whether err's chain holds an *Error of kind (see KindOf).
func Is(err error, kind Kind) bool {
	if k, _ := KindOf(err); k != Internal {
		return k == kind
	}
	var e *Error // no classified error: only an explicit Internal one matches Internal
	return kind == Internal && errors.As(err, &e)
}

func firstClassified(err error) *Error {
	switch u := err.(type) {
	case nil:
		return nil
	case *Error:
		if u.Kind != Internal {
			return u
		}
		return firstClassified(u.Cause)
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if c := firstClassified(e); c != nil {
				return c
			}
		}
		return nil
	case interface{ Unwrap() error }:
		return firstClassified(u.Unwrap())
	}
	return nil
}
