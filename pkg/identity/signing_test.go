package identity

import (
	"context"
	"testing"

	acpIdentity "github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/immutable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func generateIdentity(t *testing.T) acpIdentity.FullIdentity {
	t.Helper()
	ident, err := acpIdentity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	return ident
}

func TestWithContext_SetsIdentity(t *testing.T) {
	ident := generateIdentity(t)
	ctx := WithContext(context.Background(), immutable.Some[acpIdentity.FullIdentity](ident))

	result := FromContext(ctx)
	assert.True(t, result.HasValue())
	assert.Equal(t, ident.DID(), result.Value().DID())
}

func TestWithContext_NoneIdentity_SetsNil(t *testing.T) {
	ctx := WithContext(context.Background(), immutable.None[acpIdentity.FullIdentity]())

	result := FromContext(ctx)
	assert.False(t, result.HasValue())
}

func TestWithContext_OverwritesPreviousIdentity(t *testing.T) {
	ident1 := generateIdentity(t)
	ident2 := generateIdentity(t)

	ctx := WithContext(context.Background(), immutable.Some[acpIdentity.FullIdentity](ident1))
	ctx = WithContext(ctx, immutable.Some[acpIdentity.FullIdentity](ident2))

	result := FromContext(ctx)
	assert.True(t, result.HasValue())
	assert.Equal(t, ident2.DID(), result.Value().DID())
}

func TestFromContext_EmptyContext_ReturnsNone(t *testing.T) {
	result := FromContext(context.Background())
	assert.False(t, result.HasValue())
}

func TestFromContext_WrongTypeInContext_ReturnsNone(t *testing.T) {
	ctx := context.WithValue(context.Background(), identityContextKey{}, "not-an-identity")
	result := FromContext(ctx)
	assert.False(t, result.HasValue())
}
