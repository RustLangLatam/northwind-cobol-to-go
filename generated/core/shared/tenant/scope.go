package tenant

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
)

// FieldName is the persisted BSON field that tags a document with its tenant.
// Documents written before multi-tenancy have no such field; those documents
// belong to the default tenant.
const FieldName = "tenant_id"

// ScopeFilter narrows a Mongo query filter to a single tenant.
//
// For the default tenant it returns the filter UNCHANGED. This is the safety
// invariant: default-tenant callers see exactly the documents they saw before
// multi-tenancy, including every legacy document that has no tenant_id field.
// Adding a `tenant_id == 0` predicate would instead HIDE all legacy documents
// (they have no field at all) and lock users out — so we deliberately do not.
//
// For a real (non-default) tenant it adds a `tenant_id == <id>` predicate,
// isolating that tenant's documents. The input map is mutated and returned for
// call-site convenience; pass a fresh map if you need the original untouched.
func ScopeFilter(filter bson.M, id ID) bson.M {
	if filter == nil {
		filter = bson.M{}
	}
	if id.IsDefault() {
		return filter
	}
	filter[FieldName] = id.Uint64()
	return filter
}

// Tag stamps a document about to be written with its tenant. For the default
// tenant it is a no-op (the absent field IS the default tenant), keeping legacy
// writes byte-identical. The map is mutated and returned.
func Tag(doc bson.M, id ID) bson.M {
	if doc == nil {
		doc = bson.M{}
	}
	if id.IsDefault() {
		return doc
	}
	doc[FieldName] = id.Uint64()
	return doc
}

// ScopeCtx is the one-liner every repository query uses: it narrows filter to the
// tenant carried by ctx. It composes FromContext + ScopeFilter, so for the default
// tenant (an absent/zero tenant on ctx — the pre-multi-tenancy reality) it returns
// the filter UNCHANGED, and a real tenant on ctx adds the `tenant_id == id`
// predicate. This is the safe seam: a repo that has NOT been threaded (ctx without a
// tenant) fails open to the default tenant and keeps every legacy row visible.
func ScopeCtx(ctx context.Context, filter bson.M) bson.M {
	return ScopeFilter(filter, FromContext(ctx))
}

// StampCtx returns the tenant to persist on a document being written under ctx, as a
// raw uint64 suitable for an `omitempty` BSON field. For the default tenant it is 0,
// which an omitempty field drops entirely — so a default-tenant write stays
// byte-identical to the pre-multi-tenancy document (no tenant_id key at all). A real
// tenant returns its non-zero id, which is then persisted and matched by ScopeCtx.
func StampCtx(ctx context.Context) uint64 {
	return FromContext(ctx).Uint64()
}

// CanAccess reports whether a caller in tenant `caller` may touch a resource that
// belongs to tenant `resource`. Isolation is strict equality: a tenant can only
// reach its own resources. The default tenant can reach default-tenant resources
// (the whole single-tenant world), preserving current behavior.
func CanAccess(resource, caller ID) bool {
	return resource == caller
}
