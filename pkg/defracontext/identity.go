package defracontext

import (
	"context"

	"github.com/sourcenetwork/defradb/acp/identity"
)

// identityContextKey is the context key for node identity values.
type identityContextKey struct{}

// WithIdentity returns a new context with the given identity attached.
func WithIdentity(ctx context.Context, id identity.Identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, id)
}

// IdentityFrom returns the identity stored in the context.
func IdentityFrom(ctx context.Context) (identity.Identity, bool) {
	id, ok := ctx.Value(identityContextKey{}).(identity.Identity)
	return id, ok
}
