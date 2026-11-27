// Package tenant provides the multi-tenancy primitives for Milton Prism
// (ESC-6 / #40): a tenant id that is derived from the authenticated session,
// propagated on context.Context, and applied to data-access filters and
// ownership checks.
//
// # Backward compatibility (the load-bearing invariant)
//
// The zero value, DefaultTenantID (0), is the "default tenant". Every existing
// row, session and request maps to it because uint64 fields default to 0. The
// scoping helpers are written so that the default tenant NEVER adds a
// tenant predicate to a query and NEVER rejects an access check against another
// default-tenant resource. As a result, wiring these helpers into the existing
// single-tenant flow is a byte-for-byte no-op until a real (non-zero) tenant is
// assigned to a session. This is what lets the framework be threaded through the
// data layer without a risky backfill and without any chance of locking existing
// users out.
package tenant

import "context"

// ID is a tenant identifier. It is deliberately a distinct type from a user id
// so the two can never be silently swapped at a call site.
type ID uint64

// DefaultTenantID is the tenant every pre-multi-tenancy session, row and request
// belongs to. It is the zero value on purpose: any uint64 field that is not
// explicitly populated is already the default tenant.
const DefaultTenantID ID = 0

// IsDefault reports whether id is the default (single-tenant back-compat) tenant.
func (id ID) IsDefault() bool { return id == DefaultTenantID }

// Uint64 returns the raw identifier, for persistence / metric labels.
func (id ID) Uint64() uint64 { return uint64(id) }

// ctxKey is an unexported context key type so tenant values can never collide
// with another package's context values.
type ctxKey struct{}

// WithTenant returns a copy of ctx carrying the given tenant id. Storing the
// default tenant is allowed and harmless (FromContext would return it anyway).
func WithTenant(ctx context.Context, id ID) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the tenant id carried by ctx, or DefaultTenantID when none
// is present. It never returns an error: an absent tenant is the default tenant,
// which preserves the pre-multi-tenancy behavior.
func FromContext(ctx context.Context) ID {
	if ctx == nil {
		return DefaultTenantID
	}
	if v, ok := ctx.Value(ctxKey{}).(ID); ok {
		return v
	}
	return DefaultTenantID
}

// sessionView is the minimal read surface tenant.Derive needs from a session.
// Depending on this narrow interface (rather than importing the session package)
// keeps tenant a leaf package with no risk of an import cycle.
type sessionView interface {
	Tenant() ID
}

// Derive resolves the tenant for an authenticated principal. A nil session, or a
// session that carries no tenant, resolves to the default tenant so legacy /
// system sessions keep working unchanged.
func Derive(s sessionView) ID {
	if s == nil {
		return DefaultTenantID
	}
	return s.Tenant()
}
