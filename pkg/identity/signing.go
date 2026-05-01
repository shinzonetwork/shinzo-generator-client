package identity

import (
	"context"

	acpIdentity "github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/immutable"
)

type identityContextKey struct{}

// WithContext is...
func WithContext(ctx context.Context, identity immutable.Option[acpIdentity.FullIdentity]) context.Context {
	if identity.HasValue() {
		return context.WithValue(ctx, identityContextKey{}, identity.Value())
	}
	return context.WithValue(ctx, identityContextKey{}, nil)
}

// FromContext is...
func FromContext(ctx context.Context) immutable.Option[acpIdentity.FullIdentity] {
	ident, ok := ctx.Value(identityContextKey{}).(acpIdentity.FullIdentity)
	if ok {
		return immutable.Some(ident)
	}
	return immutable.None[acpIdentity.FullIdentity]()
}
