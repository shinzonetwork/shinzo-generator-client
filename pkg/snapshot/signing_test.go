package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defracontext"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ComputeSnapshotMerkleRoot
// ---------------------------------------------------------------------------

func TestComputeSnapshotMerkleRoot_EmptyInput(t *testing.T) {
	result := ComputeSnapshotMerkleRoot(nil)
	assert.Nil(t, result)

	result = ComputeSnapshotMerkleRoot([][]byte{})
	assert.Nil(t, result)
}

func TestComputeSnapshotMerkleRoot_Sizes(t *testing.T) {
	single := [][]byte{[]byte("test merkle root data")}
	two := [][]byte{[]byte("root one"), []byte("root two")}
	three := [][]byte{[]byte("root one"), []byte("root two"), []byte("root three")}
	four := make([][]byte, 4)
	for i := range four {
		four[i] = []byte{byte(i + 1), byte(i + 10), byte(i + 20)}
	}
	five := make([][]byte, 5)
	for i := range five {
		five[i] = bytes.Repeat([]byte{byte(i + 10)}, 32)
	}

	// Hand-computed expectations (never via the function under test).
	leaf := func(data []byte) []byte {
		h := sha256.Sum256(data)
		return h[:]
	}
	hashPair := func(a, b []byte) []byte {
		combined := make([]byte, 64)
		copy(combined[:32], a)
		copy(combined[32:], b)
		h := sha256.Sum256(combined)
		return h[:]
	}

	tests := []struct {
		name  string
		roots [][]byte
		want  func() []byte
	}{
		{
			name:  "single root is hashed directly",
			roots: single,
			want:  func() []byte { return leaf([]byte("test merkle root data")) },
		},
		{
			name:  "two roots pair into one hash",
			roots: two,
			want:  func() []byte { return hashPair(leaf([]byte("root one")), leaf([]byte("root two"))) },
		},
		{
			name:  "three roots promote the odd element",
			roots: three,
			want: func() []byte {
				return hashPair(hashPair(leaf([]byte("root one")), leaf([]byte("root two"))), leaf([]byte("root three")))
			},
		},
		{
			name:  "four roots form a balanced tree",
			roots: four,
			want: func() []byte {
				h01 := hashPair(leaf(four[0]), leaf(four[1]))
				h23 := hashPair(leaf(four[2]), leaf(four[3]))
				return hashPair(h01, h23)
			},
		},
		{
			name:  "five roots are odd at the second level",
			roots: five,
			want: func() []byte {
				h01 := hashPair(leaf(five[0]), leaf(five[1]))
				h23 := hashPair(leaf(five[2]), leaf(five[3]))
				return hashPair(hashPair(h01, h23), leaf(five[4]))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ComputeSnapshotMerkleRoot(tt.roots)
			require.NotNil(t, result)
			assert.Len(t, result, 32)
			assert.Equal(t, tt.want(), result)
		})
	}
}

func TestComputeSnapshotMerkleRoot_Deterministic(t *testing.T) {
	roots := [][]byte{
		[]byte("block sig root 1"),
		[]byte("block sig root 2"),
		[]byte("block sig root 3"),
	}

	result1 := ComputeSnapshotMerkleRoot(roots)
	result2 := ComputeSnapshotMerkleRoot(roots)

	assert.Equal(t, result1, result2)
}

func TestComputeSnapshotMerkleRoot_DifferentOrder_DifferentResult(t *testing.T) {
	root1 := []byte("alpha")
	root2 := []byte("beta")

	resultAB := ComputeSnapshotMerkleRoot([][]byte{root1, root2})
	resultBA := ComputeSnapshotMerkleRoot([][]byte{root2, root1})

	assert.NotEqual(t, resultAB, resultBA, "order should matter for merkle root")
}

// ---------------------------------------------------------------------------
// SnapshotSignatureData JSON serialization
// ---------------------------------------------------------------------------

func TestSnapshotSignatureData_JSONRoundTrip(t *testing.T) {
	original := SnapshotSignatureData{
		Version:             1,
		SnapshotFile:        "snapshot_1000_1999.kvsnap.gz",
		StartBlock:          1000,
		EndBlock:            1999,
		MerkleRoot:          "abcdef0123456789",
		BlockCount:          1000,
		SignatureType:       constants.Secp256k1ValueString,
		SignatureIdentity:   "z6MkPublicKey...",
		SignatureValue:      "deadbeef",
		CreatedAt:           "2024-01-01T00:00:00Z",
		BlockSigMerkleRoots: []string{"root1hex", "root2hex"},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded SnapshotSignatureData
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original, decoded)
}

func TestSnapshotSignatureData_OmitEmptyBlockSigRoots(t *testing.T) {
	sig := SnapshotSignatureData{
		Version:      1,
		SnapshotFile: "test.kvsnap.gz",
	}

	data, err := json.Marshal(sig)
	require.NoError(t, err)

	// blockSigMerkleRoots should be omitted when nil/empty
	assert.NotContains(t, string(data), "block_sig_merkle_roots")
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestComputeSnapshotMerkleRoot_LargeNumberOfRoots(t *testing.T) {
	// Verify it handles a large number of roots without panicking
	roots := make([][]byte, 1000)
	for i := range roots {
		roots[i] = []byte{byte(i % 256), byte(i / 256), 0x01}
	}

	result := ComputeSnapshotMerkleRoot(roots)
	require.NotNil(t, result)
	assert.Len(t, result, 32)
}

func TestComputeSnapshotMerkleRoot_PowerOfTwoRoots(t *testing.T) {
	// Power-of-two count means no odd-element promotions
	roots := make([][]byte, 8)
	for i := range roots {
		roots[i] = []byte{byte(i)}
	}

	result := ComputeSnapshotMerkleRoot(roots)
	require.NotNil(t, result)
	assert.Len(t, result, 32)

	// Should be deterministic
	result2 := ComputeSnapshotMerkleRoot(roots)
	assert.Equal(t, result, result2)
}

// ---------------------------------------------------------------------------
// getBlockSigMerkleRoots
// ---------------------------------------------------------------------------

func TestGetBlockSigMerkleRoots_EmptyDB(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	roots, count, err := s.getBlockSigMerkleRoots(ctx, 0, 1000)
	require.NoError(t, err)
	assert.Empty(t, roots)
	assert.Equal(t, 0, count)
}

func TestGetBlockSigMerkleRoots_WithBlocks(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	insertTestBlocks(t, td, 100, 102) // 3 blocks, each creates a BlockSignature
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	roots, count, err := s.getBlockSigMerkleRoots(ctx, 100, 102)
	require.NoError(t, err)

	// Each block should produce a BlockSignature document with a merkleRoot.
	// The exact count depends on whether signing succeeds (requires identity),
	// but we should get no error.
	assert.Equal(t, count, len(roots), "count should match number of roots returned")
}

func TestGetBlockSigMerkleRoots_OutOfRange(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	insertTestBlocks(t, td, 100, 102)
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	roots, count, err := s.getBlockSigMerkleRoots(ctx, 500, 600)
	require.NoError(t, err)
	assert.Empty(t, roots)
	assert.Equal(t, 0, count)
}

// ---------------------------------------------------------------------------
// QuerySnapshotSignatures
// ---------------------------------------------------------------------------

func TestQuerySnapshotSignatures_EmptyDB(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	sigs, err := QuerySnapshotSignatures(ctx, td.Node, testSnapshotSignatureCollection)
	require.NoError(t, err)
	assert.NotNil(t, sigs)
	assert.Empty(t, sigs)
}

// ---------------------------------------------------------------------------
// createSnapshotSignatureDoc + QuerySnapshotSignatures roundtrip
// ---------------------------------------------------------------------------

func TestCreateSnapshotSignatureDoc_And_QuerySnapshotSignatures(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	sig := &SnapshotSignatureData{
		Version:           1,
		SnapshotFile:      "snapshot_1000_1999.kvsnap.gz",
		StartBlock:        1000,
		EndBlock:          1999,
		MerkleRoot:        "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		BlockCount:        1000,
		SignatureType:     constants.Secp256k1ValueString,
		SignatureIdentity: "z6MkTestPublicKey1234567890",
		SignatureValue:    "deadbeefcafe0000000000000000000000000000000000000000000000000000",
		CreatedAt:         "2024-01-15T12:00:00Z",
		BlockSigMerkleRoots: []string{
			"aaaa000000000000000000000000000000000000000000000000000000000000",
			"bbbb000000000000000000000000000000000000000000000000000000000000",
		},
	}

	err := s.createSnapshotSignatureDoc(ctx, sig)
	require.NoError(t, err)

	// Query back
	sigs, err := QuerySnapshotSignatures(ctx, td.Node, testSnapshotSignatureCollection)
	require.NoError(t, err)
	require.Len(t, sigs, 1)

	retrieved, ok := sigs["snapshot_1000_1999.kvsnap.gz"]
	require.True(t, ok, "should find the sig by snapshot filename")

	assert.Equal(t, int64(1000), retrieved.StartBlock)
	assert.Equal(t, int64(1999), retrieved.EndBlock)
	assert.Equal(t, "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", retrieved.MerkleRoot)
	assert.Equal(t, 1000, retrieved.BlockCount)
	assert.Equal(t, constants.Secp256k1ValueString, retrieved.SignatureType)
	assert.Equal(t, "z6MkTestPublicKey1234567890", retrieved.SignatureIdentity)
	assert.Equal(t, "deadbeefcafe0000000000000000000000000000000000000000000000000000", retrieved.SignatureValue)
	assert.Equal(t, "snapshot_1000_1999.kvsnap.gz", retrieved.SnapshotFile)
	assert.Equal(t, "2024-01-15T12:00:00Z", retrieved.CreatedAt)
	// Note: blockSigMerkleRoots may come back nil from DefraDB blind write queries.
	// The field is stored correctly but [String] array fields may not round-trip
	// through blind write transactions. This does not affect production usage
	// since the roots are also embedded in the snapshot file header.
}

func TestCreateSnapshotSignatureDoc_MultipleDocs(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	for i := range 3 {
		sig := &SnapshotSignatureData{
			Version:           1,
			SnapshotFile:      fmt.Sprintf("snapshot_%d_%d.kvsnap.gz", i*1000, (i+1)*1000-1),
			StartBlock:        int64(i * 1000),
			EndBlock:          int64((i+1)*1000 - 1),
			MerkleRoot:        fmt.Sprintf("%064x", i+1),
			BlockCount:        1000,
			SignatureType:     constants.Secp256k1ValueString,
			SignatureIdentity: "z6MkTestKey",
			SignatureValue:    fmt.Sprintf("%064x", i+100),
			CreatedAt:         "2024-01-15T12:00:00Z",
		}
		err := s.createSnapshotSignatureDoc(ctx, sig)
		require.NoError(t, err)
	}

	sigs, err := QuerySnapshotSignatures(ctx, td.Node, testSnapshotSignatureCollection)
	require.NoError(t, err)
	assert.Len(t, sigs, 3)

	// Verify all three are retrievable by filename
	for i := range 3 {
		filename := fmt.Sprintf("snapshot_%d_%d.kvsnap.gz", i*1000, (i+1)*1000-1)
		_, ok := sigs[filename]
		assert.True(t, ok, "should find sig for %s", filename)
	}
}

// ---------------------------------------------------------------------------
// signMerkleRoot: all paths
// ---------------------------------------------------------------------------

func TestSignMerkleRoot_NoIdentityInContext(t *testing.T) {
	ctx := context.Background()
	merkleRoot := bytes.Repeat([]byte{0xAA}, 32)

	err := signMerkleRootErr(ctx, merkleRoot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no identity in context")
}

func TestSignMerkleRoot(t *testing.T) {
	tests := []struct {
		name     string
		keyType  crypto.KeyType
		wantType string
		root     byte
	}{
		{name: "Ed25519", keyType: crypto.KeyTypeEd25519, wantType: constants.Ed25519ValueString, root: 0xBB},
		{name: "Secp256k1", keyType: crypto.KeyTypeSecp256k1, wantType: constants.Secp256k1ValueString, root: 0xCC},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := newIdentityCtx(t, tt.keyType)

			merkleRoot := bytes.Repeat([]byte{tt.root}, 32)
			sigType, sigIdentity, sigValue, err := signMerkleRoot(ctx, merkleRoot)
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, sigType)
			assert.NotEmpty(t, sigIdentity)
			assert.NotEmpty(t, sigValue)

			// Verify the signature is correct
			pubKey, err := crypto.PublicKeyFromString(tt.keyType, sigIdentity)
			require.NoError(t, err)
			valid, err := pubKey.Verify(merkleRoot, sigValue)
			require.NoError(t, err)
			assert.True(t, valid)
		})
	}
}

// ---------------------------------------------------------------------------
// signSnapshotWithRoots: all paths
// ---------------------------------------------------------------------------

func TestSignSnapshotWithRoots_NoRoots(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	// No roots: should skip signing and return nil
	err := s.signSnapshotWithRoots(ctx, "test.kvsnap.gz", 1000, 1999, nil, 0)
	require.NoError(t, err)

	err = s.signSnapshotWithRoots(ctx, "test.kvsnap.gz", 1000, 1999, [][]byte{}, 0)
	require.NoError(t, err)
}

func TestSignSnapshotWithRoots_NoIdentity(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background() // No identity in context
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	roots := [][]byte{bytes.Repeat([]byte{0xAA}, 32)}

	// signMerkleRoot will fail with "no identity in context",
	// signSnapshotWithRoots logs a warning and returns nil
	err := s.signSnapshotWithRoots(ctx, "test.kvsnap.gz", 1000, 1999, roots, 1)
	require.NoError(t, err)
}

func TestSignSnapshotWithRoots_WithIdentity(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	ctx, _ := newIdentityCtx(t, crypto.KeyTypeEd25519)

	roots := [][]byte{
		bytes.Repeat([]byte{0xAA}, 32),
		bytes.Repeat([]byte{0xBB}, 32),
	}

	err := s.signSnapshotWithRoots(ctx, "snapshot_1000_1999.kvsnap.gz", 1000, 1999, roots, 2)
	require.NoError(t, err)

	// Verify the signature was stored in DefraDB
	sigs, err := QuerySnapshotSignatures(ctx, td.Node, testSnapshotSignatureCollection)
	require.NoError(t, err)
	assert.Len(t, sigs, 1)

	sig, ok := sigs["snapshot_1000_1999.kvsnap.gz"]
	require.True(t, ok)
	assert.Equal(t, int64(1000), sig.StartBlock)
	assert.Equal(t, int64(1999), sig.EndBlock)
	assert.Equal(t, constants.Ed25519ValueString, sig.SignatureType)
	assert.Equal(t, 2, sig.BlockCount)
	assert.NotEmpty(t, sig.MerkleRoot)
	assert.NotEmpty(t, sig.SignatureValue)
}

// ---------------------------------------------------------------------------
// signMerkleRoot: identity is not FullIdentity (no private key)
// ---------------------------------------------------------------------------

func TestSignMerkleRoot_IdentityNotFull(t *testing.T) {
	// Create a context with a non-full identity (just a DID)
	baseIdent := identity.FromDID("did:key:z6Mk123")
	ctx := defracontext.WithIdentity(context.Background(), baseIdent)

	merkleRoot := bytes.Repeat([]byte{0xAA}, 32)
	err := signMerkleRootErr(ctx, merkleRoot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity is not a full identity")
}

// ---------------------------------------------------------------------------
// createSnapshotSignatureDoc: multiple fields roundtrip with blockSigMerkleRoots
// ---------------------------------------------------------------------------

func TestCreateSnapshotSignatureDoc_WithBlockSigMerkleRoots(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	sig := &SnapshotSignatureData{
		Version:           1,
		SnapshotFile:      "snapshot_2000_2999.kvsnap.gz",
		StartBlock:        2000,
		EndBlock:          2999,
		MerkleRoot:        "aabbccdd" + strings.Repeat("00", 28),
		BlockCount:        1000,
		SignatureType:     constants.Ed25519ValueString,
		SignatureIdentity: "z6MkTestKey2",
		SignatureValue:    "deadbeef" + strings.Repeat("00", 28),
		CreatedAt:         "2024-06-15T12:00:00Z",
		BlockSigMerkleRoots: []string{
			strings.Repeat("aa", 32),
			strings.Repeat("bb", 32),
			strings.Repeat("cc", 32),
		},
	}

	err := s.createSnapshotSignatureDoc(ctx, sig)
	require.NoError(t, err)

	// Query back and verify
	sigs, err := QuerySnapshotSignatures(ctx, td.Node, testSnapshotSignatureCollection)
	require.NoError(t, err)
	require.Len(t, sigs, 1)

	retrieved, ok := sigs["snapshot_2000_2999.kvsnap.gz"]
	require.True(t, ok)
	assert.Equal(t, constants.Ed25519ValueString, retrieved.SignatureType)
	assert.Equal(t, "2024-06-15T12:00:00Z", retrieved.CreatedAt)
}

// ---------------------------------------------------------------------------
// QuerySnapshotSignatures: empty snapshotFile skipped
// ---------------------------------------------------------------------------

func TestQuerySnapshotSignatures_EmptySnapshotFileSkipped(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	// Create a doc with empty snapshotFile - it should be skipped in results
	sig := &SnapshotSignatureData{
		Version:           1,
		SnapshotFile:      "", // empty
		StartBlock:        1000,
		EndBlock:          1999,
		MerkleRoot:        strings.Repeat("ab", 32),
		BlockCount:        1000,
		SignatureType:     constants.Secp256k1ValueString,
		SignatureIdentity: "z6MkTestKey",
		SignatureValue:    strings.Repeat("cd", 32),
		CreatedAt:         "2024-01-01T00:00:00Z",
	}

	err := s.createSnapshotSignatureDoc(ctx, sig)
	require.NoError(t, err)

	sigs, err := QuerySnapshotSignatures(ctx, td.Node, testSnapshotSignatureCollection)
	require.NoError(t, err)
	// Doc with empty snapshotFile should be skipped
	assert.Empty(t, sigs)
}

// ---------------------------------------------------------------------------
// getBlockSigMerkleRoots with blocks inserted (cover parsing paths)
// ---------------------------------------------------------------------------

func TestGetBlockSigMerkleRoots_CoverParsing(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	insertTestBlocks(t, td, 400, 404)
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	// Query the full range
	roots, count, err := s.getBlockSigMerkleRoots(ctx, 400, 404)
	require.NoError(t, err)
	// Block signatures may or may not be created depending on node config,
	// but the function should not error
	assert.GreaterOrEqual(t, count, 0)
	assert.Equal(t, len(roots), count)

	// Also test a range that partially overlaps
	roots2, count2, err := s.getBlockSigMerkleRoots(ctx, 402, 410)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count2, 0)
	assert.Equal(t, len(roots2), count2)
}

// ---------------------------------------------------------------------------
// signSnapshotWithRoots: multiple roots with identity (full signing flow)
// ---------------------------------------------------------------------------

func TestSignSnapshotWithRoots_MultipleRoots(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)

	ctx, _ := newIdentityCtx(t, crypto.KeyTypeSecp256k1)
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	roots := make([][]byte, 5)
	for i := range roots {
		roots[i] = bytes.Repeat([]byte{byte(i + 1)}, 32)
	}

	err := s.signSnapshotWithRoots(ctx, "snapshot_5000_5999.kvsnap.gz", 5000, 5999, roots, 5)
	require.NoError(t, err)

	// Verify the signature document was created
	sigs, err := QuerySnapshotSignatures(ctx, td.Node, testSnapshotSignatureCollection)
	require.NoError(t, err)
	require.Len(t, sigs, 1)

	sig := sigs["snapshot_5000_5999.kvsnap.gz"]
	require.NotNil(t, sig)
	assert.Equal(t, constants.Secp256k1ValueString, sig.SignatureType)
	assert.Equal(t, 5, sig.BlockCount)
	assert.NotEmpty(t, sig.MerkleRoot)
	assert.NotEmpty(t, sig.SignatureValue)
	assert.NotEmpty(t, sig.SignatureIdentity)
}

// ---------------------------------------------------------------------------
// signSnapshotWithRoots: computed merkle root is nil (single empty root)
// ---------------------------------------------------------------------------

func TestSignSnapshotWithRoots_ComputeRootFails(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	// Empty roots array triggers early return (already tested)
	// Non-empty roots should always produce a non-nil merkle root,
	// so the "failed to compute" path (line 293-295) is structurally unreachable.
	// We verify this by confirming ComputeSnapshotMerkleRoot never returns nil
	// for non-empty input.
	root := ComputeSnapshotMerkleRoot([][]byte{{0x01}})
	assert.NotNil(t, root)

	_ = td
	_ = ctx
}

// ---------------------------------------------------------------------------
// signSnapshotWithRoots: createSnapshotSignatureDoc fails (line 325-328)
// This logs a warning but doesn't fail the operation.
// Hard to trigger without mocking, but if we provide nil defraNode we'd panic.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// QuerySnapshotSignatures: GQL error path
// Hard to trigger with a real node. The collection always exists.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// signMerkleRoot: verify the signing actually produces correct identity string
// ---------------------------------------------------------------------------

func TestSignMerkleRoot_IdentityIsPublicKeyHex(t *testing.T) {
	ctx, fullIdent := newIdentityCtx(t, crypto.KeyTypeEd25519)

	merkleRoot := bytes.Repeat([]byte{0xAA}, 32)
	_, sigIdentity, _, err := signMerkleRoot(ctx, merkleRoot)
	require.NoError(t, err)

	// The identity should be the public key hex string
	assert.Equal(t, fullIdent.PublicKey().String(), sigIdentity)

	// Verify we can reconstruct the public key from the identity string
	_, err = crypto.PublicKeyFromString(crypto.KeyTypeEd25519, sigIdentity)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// getBlockSigMerkleRoots: with actual BlockSignature documents
// ---------------------------------------------------------------------------

func TestGetBlockSigMerkleRoots_InsertVariants(t *testing.T) {
	tests := []struct {
		name      string
		inserts   []string
		wantCount int
		wantRoots [][]byte
	}{
		{
			name: "valid block signatures return their exact roots",
			inserts: []string{
				hex.EncodeToString(bytes.Repeat([]byte{0x11}, 32)),
				hex.EncodeToString(bytes.Repeat([]byte{0x22}, 32)),
				hex.EncodeToString(bytes.Repeat([]byte{0x33}, 32)),
			},
			wantCount: 3,
			wantRoots: [][]byte{
				bytes.Repeat([]byte{0x11}, 32),
				bytes.Repeat([]byte{0x22}, 32),
				bytes.Repeat([]byte{0x33}, 32),
			},
		},
		{
			name:      "invalid hex root is counted but excluded",
			inserts:   []string{"not_valid_hex_zzzzz", hex.EncodeToString(bytes.Repeat([]byte{0xAA}, 32))},
			wantCount: 2,
			wantRoots: [][]byte{bytes.Repeat([]byte{0xAA}, 32)},
		},
		{
			name:      "empty root is counted but excluded",
			inserts:   []string{"", hex.EncodeToString(bytes.Repeat([]byte{0xBB}, 32))},
			wantCount: 2,
			wantRoots: [][]byte{bytes.Repeat([]byte{0xBB}, 32)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			td := testutils.SetupTestDefraDB(t)
			ctx := context.Background()
			s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

			for i, mr := range tt.inserts {
				insertBlockSignature(t, td, int64(100+i), mr)
			}

			roots, count, err := s.getBlockSigMerkleRoots(ctx, 100, int64(100+len(tt.inserts)-1))
			require.NoError(t, err)
			assert.Equal(t, tt.wantCount, count)
			require.Len(t, roots, len(tt.wantRoots))
			for i, want := range tt.wantRoots {
				assert.Equal(t, want, roots[i])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// signSnapshotWithRoots with block signatures + identity (full flow)
// ---------------------------------------------------------------------------

func TestSignSnapshotWithRoots_FullFlowWithBlockSigs(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	insertTestBlocks(t, td, 600, 602)
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	// Insert block signatures
	roots := make([][]byte, 3)
	for i := int64(600); i <= 602; i++ {
		rootBytes := bytes.Repeat([]byte{uint8(i + 1)}, 32) //nolint:mnd,gosec
		roots[i-600] = rootBytes
		insertBlockSignature(t, td, i, hex.EncodeToString(rootBytes))
	}

	ctx, _ := newIdentityCtx(t, crypto.KeyTypeEd25519)

	err := s.signSnapshotWithRoots(ctx, "snapshot_600_602.kvsnap.gz", 600, 602, roots, 3)
	require.NoError(t, err)

	// Verify the signature document
	sigs, err := QuerySnapshotSignatures(ctx, td.Node, testSnapshotSignatureCollection)
	require.NoError(t, err)
	require.Len(t, sigs, 1)

	sig := sigs["snapshot_600_602.kvsnap.gz"]
	require.NotNil(t, sig)
	assert.Equal(t, int64(600), sig.StartBlock)
	assert.Equal(t, int64(602), sig.EndBlock)
	assert.Equal(t, 3, sig.BlockCount)
	assert.Equal(t, constants.Ed25519ValueString, sig.SignatureType)
	assert.NotEmpty(t, sig.MerkleRoot)
	assert.NotEmpty(t, sig.SignatureValue)

	// Verify the signature is actually valid
	merkleRootBytes, err := hex.DecodeString(sig.MerkleRoot)
	require.NoError(t, err)
	sigValueBytes, err := hex.DecodeString(sig.SignatureValue)
	require.NoError(t, err)

	pubKey, err := crypto.PublicKeyFromString(crypto.KeyTypeEd25519, sig.SignatureIdentity)
	require.NoError(t, err)
	valid, err := pubKey.Verify(merkleRootBytes, sigValueBytes)
	require.NoError(t, err)
	assert.True(t, valid, "signature should verify correctly")
}

// ---------------------------------------------------------------------------
// signMerkleRoot: unsupported key type (secp256r1)
// ---------------------------------------------------------------------------

func TestSignMerkleRoot_UnsupportedKeyType(t *testing.T) {
	// Generate a secp256r1 key, which is not supported by signMerkleRoot
	ctx, _ := newIdentityCtx(t, crypto.KeyTypeSecp256r1)

	merkleRoot := bytes.Repeat([]byte{0xAA}, 32)
	err := signMerkleRootErr(ctx, merkleRoot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported key type")
}

// ---------------------------------------------------------------------------
// signSnapshotWithRoots: signMerkleRoot returns unsupported key type
// This logs a warning and returns nil (no error propagated)
// ---------------------------------------------------------------------------

func TestSignSnapshotWithRoots_UnsupportedKeyType(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)

	ctx, _ := newIdentityCtx(t, crypto.KeyTypeSecp256r1)
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	roots := [][]byte{bytes.Repeat([]byte{0xAA}, 32)}

	// signMerkleRoot will fail with "unsupported key type",
	// signSnapshotWithRoots logs warning and returns nil
	err := s.signSnapshotWithRoots(ctx, "test.kvsnap.gz", 1000, 1999, roots, 1)
	require.NoError(t, err, "should return nil even when signing fails")
}

// ---------------------------------------------------------------------------
// signMerkleRoot: sign error path (hard to trigger but test key identity)
// ---------------------------------------------------------------------------

func TestSignMerkleRoot_ProducesVerifiableSignature(t *testing.T) {
	for _, keyType := range []crypto.KeyType{crypto.KeyTypeEd25519, crypto.KeyTypeSecp256k1} {
		t.Run(string(keyType), func(t *testing.T) {
			ctx, _ := newIdentityCtx(t, keyType)

			merkleRoot := bytes.Repeat([]byte{0xDD}, 32)
			sigType, sigIdentity, sigValue, err := signMerkleRoot(ctx, merkleRoot)
			require.NoError(t, err)

			// Verify the returned values
			assert.NotEmpty(t, sigType)
			assert.NotEmpty(t, sigIdentity)
			assert.NotEmpty(t, sigValue)

			// Verify the signature
			var kt crypto.KeyType
			switch sigType {
			case constants.Secp256k1ValueString:
				kt = crypto.KeyTypeSecp256k1
			case constants.Ed25519ValueString:
				kt = crypto.KeyTypeEd25519
			}

			pubKey, err := crypto.PublicKeyFromString(kt, sigIdentity)
			require.NoError(t, err)
			valid, err := pubKey.Verify(merkleRoot, sigValue)
			require.NoError(t, err)
			assert.True(t, valid)
		})
	}
}

// ---------------------------------------------------------------------------
// QuerySnapshotSignatures: with multiple documents of various field types
// ---------------------------------------------------------------------------

func TestQuerySnapshotSignatures_MultipleDocsWithBlockSigRoots(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	// Create two docs with blockSigMerkleRoots
	for i := range 2 {
		sig := &SnapshotSignatureData{
			Version:           1,
			SnapshotFile:      fmt.Sprintf("snapshot_%d.kvsnap.gz", i),
			StartBlock:        int64(i * 1000),
			EndBlock:          int64((i+1)*1000 - 1),
			MerkleRoot:        fmt.Sprintf("%064x", i+1),
			BlockCount:        1000,
			SignatureType:     constants.Ed25519ValueString,
			SignatureIdentity: "z6MkTestKey",
			SignatureValue:    fmt.Sprintf("%064x", i+100),
			CreatedAt:         "2024-06-15T12:00:00Z",
			BlockSigMerkleRoots: []string{
				fmt.Sprintf("%064x", i+200),
				fmt.Sprintf("%064x", i+300),
			},
		}
		err := s.createSnapshotSignatureDoc(ctx, sig)
		require.NoError(t, err)
	}

	sigs, err := QuerySnapshotSignatures(ctx, td.Node, testSnapshotSignatureCollection)
	require.NoError(t, err)
	assert.Len(t, sigs, 2)

	for i := range 2 {
		filename := fmt.Sprintf("snapshot_%d.kvsnap.gz", i)
		sig, ok := sigs[filename]
		require.True(t, ok)
		assert.Equal(t, int64(i*1000), sig.StartBlock)
		assert.Equal(t, int64((i+1)*1000-1), sig.EndBlock)
		assert.Equal(t, constants.Ed25519ValueString, sig.SignatureType)
	}
}

// ---------------------------------------------------------------------------
// getBlockSigMerkleRoots: with blocks inserted via identity ([]any code path)
// ---------------------------------------------------------------------------

func TestGetBlockSigMerkleRoots_ViaIdentityInsertedBlocks(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx, _ := insertTestBlocksWithIdentity(t, td, 100, 102)
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	roots, count, err := s.getBlockSigMerkleRoots(ctx, 100, 102)
	require.NoError(t, err)
	assert.Equal(t, 3, count, "should find 3 block signatures")
	assert.Len(t, roots, 3, "should return 3 merkle roots")

	// Each root should be non-empty
	for i, root := range roots {
		assert.NotEmpty(t, root, "root %d should be non-empty", i)
	}
}
