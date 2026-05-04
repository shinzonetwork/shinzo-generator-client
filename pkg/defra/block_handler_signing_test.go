package defra

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	gocid "github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/node"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/testutils"
)

// makeTestCID returns a deterministic CID derived from seed. Lets tests build
// known collectors without depending on real document writes.
func makeTestCID(t *testing.T, seed string) gocid.Cid {
	t.Helper()
	hash, err := multihash.Sum([]byte(seed), multihash.SHA2_256, -1)
	require.NoError(t, err)
	return gocid.NewCidV1(gocid.Raw, hash)
}

// NewBlockHandler must select signBlockWithIdentity when given a non-nil
// FullIdentity. The signing-from-struct path doesn't depend on identity being
// in the context, which is the only way batch signing works for callers that
// can't write to defradb's internal identity key.
func TestNewBlockHandler_NonNilIdent_RoutesThroughSignBlockWithIdentity(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	require.NotNil(t, td.Identity, "test setup must provide a real FullIdentity")

	h, err := NewBlockHandler(td.Node, 1000, nil, td.Identity)
	require.NoError(t, err)
	require.NotNil(t, h.signBlockFn)

	expectedFn := h.signBlockWithIdentity
	assert.Equal(t,
		reflect.ValueOf(expectedFn).Pointer(),
		reflect.ValueOf(h.signBlockFn).Pointer(),
		"with non-nil ident, signBlockFn must be h.signBlockWithIdentity",
	)
	assert.NotEqual(t,
		reflect.ValueOf(node.SignBlock).Pointer(),
		reflect.ValueOf(h.signBlockFn).Pointer(),
		"with non-nil ident, signBlockFn must not be node.SignBlock",
	)
}

// NewBlockHandler must fall back to node.SignBlock when ident is nil. This
// path produces no signatures (node.SignBlock requires identity in defradb's
// internal context key, which external code can't populate), but several
// tests pass nil ident and depend on this branch existing.
func TestNewBlockHandler_NilIdent_RoutesThroughNodeSignBlock(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	h, err := NewBlockHandler(td.Node, 1000, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, h.signBlockFn)

	assert.Equal(t,
		reflect.ValueOf(node.SignBlock).Pointer(),
		reflect.ValueOf(h.signBlockFn).Pointer(),
		"with nil ident, signBlockFn must be node.SignBlock",
	)
}

// signBlockWithIdentity must produce a cryptographically correct signature:
// the merkle root must hash the supplied CIDs, the signature must verify
// against the configured identity's public key, and the header fields
// (signatureType, signatureIdentity) must match the configured identity.
func TestSignBlockWithIdentity_ProducesValidSignature(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	require.NotNil(t, td.Identity, "test setup must provide a real FullIdentity")

	h, err := NewBlockHandler(td.Node, 1000, nil, td.Identity)
	require.NoError(t, err)

	collector := node.NewBlockCIDCollector()
	cids := []gocid.Cid{
		makeTestCID(t, "doc-A"),
		makeTestCID(t, "doc-B"),
		makeTestCID(t, "doc-C"),
	}
	for _, c := range cids {
		collector.Add(c)
	}

	blockSig, err := h.signBlockWithIdentity(context.Background(), collector)
	require.NoError(t, err)
	require.NotNil(t, blockSig, "signing must produce a non-nil BlockSignature")

	expectedRoot := node.ComputeMerkleRoot(cids)
	require.Equal(t, len(expectedRoot), len(blockSig.MerkleRoot), "merkle root length mismatch")
	assert.True(t, bytes.Equal(expectedRoot, blockSig.MerkleRoot),
		"merkle root in signature must match ComputeMerkleRoot over the input CIDs")

	assert.Equal(t, len(cids), blockSig.CIDCount)

	switch td.Identity.PrivateKey().Type() {
	case crypto.KeyTypeSecp256k1:
		assert.Equal(t, "ES256K", blockSig.Header.Type,
			"secp256k1 keys must produce ES256K signatures")
	case crypto.KeyTypeEd25519:
		assert.Equal(t, "EdDSA", blockSig.Header.Type,
			"ed25519 keys must produce EdDSA signatures")
	default:
		t.Fatalf("test identity uses unexpected key type: %v", td.Identity.PrivateKey().Type())
	}

	expectedIdent := td.Identity.PublicKey().String()
	assert.Equal(t, expectedIdent, string(blockSig.Header.Identity),
		"signature must carry the handler's identity in the header")

	valid, err := node.VerifyBlockSignatureCIDs(blockSig, cids)
	require.NoError(t, err)
	assert.True(t, valid, "signature must verify against the input CIDs")
}

// signBlockWithIdentity must short-circuit and return (nil, nil) when the
// collector has no CIDs. Callers rely on this to skip signature creation
// for blocks that produced no signed content.
func TestSignBlockWithIdentity_EmptyCollector_ReturnsNil(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	h, err := NewBlockHandler(td.Node, 1000, nil, td.Identity)
	require.NoError(t, err)

	collector := node.NewBlockCIDCollector()
	blockSig, err := h.signBlockWithIdentity(context.Background(), collector)
	require.NoError(t, err)
	assert.Nil(t, blockSig, "empty collector must produce a nil BlockSignature")
}

// signBlockWithIdentity must short-circuit when h.nodeIdentity is nil rather
// than panicking on a nil PrivateKey() dereference. Guards the direct-call
// path; callers normally avoid this via the routing logic in NewBlockHandler.
func TestSignBlockWithIdentity_NilNodeIdentity_ReturnsNil(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	h, err := NewBlockHandler(td.Node, 1000, nil, nil)
	require.NoError(t, err)
	require.Nil(t, h.nodeIdentity, "test precondition: h.nodeIdentity must be nil")

	collector := node.NewBlockCIDCollector()
	collector.Add(makeTestCID(t, "doc-A"))
	blockSig, err := h.signBlockWithIdentity(context.Background(), collector)
	require.NoError(t, err)
	assert.Nil(t, blockSig, "nil h.nodeIdentity must produce a nil BlockSignature")
}

// Distinct CID sets must produce distinct merkle roots and distinct signature
// values. Catches a class of bug where signing accidentally hashes a constant
// or stale state instead of the current collector's contents.
func TestSignBlockWithIdentity_DifferentCIDsProduceDifferentRoots(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	h, err := NewBlockHandler(td.Node, 1000, nil, td.Identity)
	require.NoError(t, err)

	collector1 := node.NewBlockCIDCollector()
	collector1.Add(makeTestCID(t, "alpha"))
	collector1.Add(makeTestCID(t, "beta"))

	collector2 := node.NewBlockCIDCollector()
	collector2.Add(makeTestCID(t, "gamma"))
	collector2.Add(makeTestCID(t, "delta"))

	sig1, err := h.signBlockWithIdentity(context.Background(), collector1)
	require.NoError(t, err)
	require.NotNil(t, sig1)

	sig2, err := h.signBlockWithIdentity(context.Background(), collector2)
	require.NoError(t, err)
	require.NotNil(t, sig2)

	assert.False(t, bytes.Equal(sig1.MerkleRoot, sig2.MerkleRoot),
		"distinct CID sets must produce distinct merkle roots")
	assert.False(t, bytes.Equal(sig1.Value, sig2.Value),
		"distinct merkle roots must produce distinct signature values")
}
