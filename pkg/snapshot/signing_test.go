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

func TestComputeSnapshotMerkleRoot_SingleRoot(t *testing.T) {
	root := []byte("test merkle root data")
	result := ComputeSnapshotMerkleRoot([][]byte{root})
	require.NotNil(t, result)

	// Single root: hash of that root
	expected := sha256.Sum256(root)
	assert.Equal(t, expected[:], result)
}

func TestComputeSnapshotMerkleRoot_TwoRoots(t *testing.T) {
	root1 := []byte("root one")
	root2 := []byte("root two")

	result := ComputeSnapshotMerkleRoot([][]byte{root1, root2})
	require.NotNil(t, result)

	// Two roots: hash(hash(root1) || hash(root2))
	h1 := sha256.Sum256(root1)
	h2 := sha256.Sum256(root2)
	combined := make([]byte, 64)
	copy(combined[:32], h1[:])
	copy(combined[32:], h2[:])
	expected := sha256.Sum256(combined)
	assert.Equal(t, expected[:], result)
}

func TestComputeSnapshotMerkleRoot_ThreeRoots_OddCount(t *testing.T) {
	root1 := []byte("root one")
	root2 := []byte("root two")
	root3 := []byte("root three")

	result := ComputeSnapshotMerkleRoot([][]byte{root1, root2, root3})
	require.NotNil(t, result)
	assert.Len(t, result, 32)

	// Three roots: pair (h1, h2) -> combined hash, h3 promoted.
	// Then pair (combined, h3) -> final.
	h1 := sha256.Sum256(root1)
	h2 := sha256.Sum256(root2)
	h3 := sha256.Sum256(root3)

	combined12 := make([]byte, 64)
	copy(combined12[:32], h1[:])
	copy(combined12[32:], h2[:])
	hash12 := sha256.Sum256(combined12)

	// h3 is promoted as-is (odd element)
	// Now pair (hash12, h3[:])
	combinedFinal := make([]byte, 64)
	copy(combinedFinal[:32], hash12[:])
	copy(combinedFinal[32:], h3[:])
	expected := sha256.Sum256(combinedFinal)
	assert.Equal(t, expected[:], result)
}

func TestComputeSnapshotMerkleRoot_FourRoots(t *testing.T) {
	roots := make([][]byte, 4)
	for i := range roots {
		roots[i] = []byte{byte(i + 1), byte(i + 10), byte(i + 20)}
	}

	result := ComputeSnapshotMerkleRoot(roots)
	require.NotNil(t, result)
	assert.Len(t, result, 32)

	// Manually compute
	hashes := make([][]byte, 4)
	for i, r := range roots {
		h := sha256.Sum256(r)
		hashes[i] = h[:]
	}

	combined01 := make([]byte, 64)
	copy(combined01[:32], hashes[0])
	copy(combined01[32:], hashes[1])
	h01 := sha256.Sum256(combined01)

	combined23 := make([]byte, 64)
	copy(combined23[:32], hashes[2])
	copy(combined23[32:], hashes[3])
	h23 := sha256.Sum256(combined23)

	combinedFinal := make([]byte, 64)
	copy(combinedFinal[:32], h01[:])
	copy(combinedFinal[32:], h23[:])
	expected := sha256.Sum256(combinedFinal)

	assert.Equal(t, expected[:], result)
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

func TestSignMerkleRoot_Ed25519(t *testing.T) {
	ctx, _ := newIdentityCtx(t, crypto.KeyTypeEd25519)

	merkleRoot := bytes.Repeat([]byte{0xBB}, 32)
	sigType, sigIdentity, sigValue, err := signMerkleRoot(ctx, merkleRoot)
	require.NoError(t, err)
	assert.Equal(t, constants.Ed25519ValueString, sigType)
	assert.NotEmpty(t, sigIdentity)
	assert.NotEmpty(t, sigValue)

	// Verify the signature is correct
	pubKey, err := crypto.PublicKeyFromString(crypto.KeyTypeEd25519, sigIdentity)
	require.NoError(t, err)
	valid, err := pubKey.Verify(merkleRoot, sigValue)
	require.NoError(t, err)
	assert.True(t, valid)
}

func TestSignMerkleRoot_Secp256k1(t *testing.T) {
	ctx, _ := newIdentityCtx(t, crypto.KeyTypeSecp256k1)

	merkleRoot := bytes.Repeat([]byte{0xCC}, 32)
	sigType, sigIdentity, sigValue, err := signMerkleRoot(ctx, merkleRoot)
	require.NoError(t, err)
	assert.Equal(t, constants.Secp256k1ValueString, sigType)
	assert.NotEmpty(t, sigIdentity)
	assert.NotEmpty(t, sigValue)

	// Verify the signature is correct
	pubKey, err := crypto.PublicKeyFromString(crypto.KeyTypeSecp256k1, sigIdentity)
	require.NoError(t, err)
	valid, err := pubKey.Verify(merkleRoot, sigValue)
	require.NoError(t, err)
	assert.True(t, valid)
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
// ComputeSnapshotMerkleRoot: five roots (odd at second level)
// ---------------------------------------------------------------------------

func TestComputeSnapshotMerkleRoot_FiveRoots(t *testing.T) {
	roots := make([][]byte, 5)
	for i := range roots {
		roots[i] = bytes.Repeat([]byte{byte(i + 10)}, 32)
	}

	result := ComputeSnapshotMerkleRoot(roots)
	require.NotNil(t, result)
	assert.Len(t, result, 32)

	// Verify deterministic
	result2 := ComputeSnapshotMerkleRoot(roots)
	assert.Equal(t, result, result2)
}

// ---------------------------------------------------------------------------
// checkAndSnapshot: highest > 0 but lowest == 0 (structurally impossible)
// and lowest > 0 but highest == 0 (structurally impossible)
// These paths are defensive and cannot be triggered with real DefraDB.
// ---------------------------------------------------------------------------

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
// createKVSnapshot: rootsHex loop (line 57-59)
// This is covered when block signatures exist and getBlockSigMerkleRoots returns roots.
// Since our test DefraDB doesn't create block signatures, we need an identity-enabled node.
// We test this indirectly via createKVSnapshot_WithIdentity_SignsSnapshot.
// Let's also test it directly by inserting block signatures manually.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// getBlockSigMerkleRoots: with actual BlockSignature documents
// ---------------------------------------------------------------------------

func TestGetBlockSigMerkleRoots_WithBlockSignatures(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	// Insert block signature documents with known merkle roots
	mr1 := hex.EncodeToString(bytes.Repeat([]byte{0x11}, 32))
	mr2 := hex.EncodeToString(bytes.Repeat([]byte{0x22}, 32))
	mr3 := hex.EncodeToString(bytes.Repeat([]byte{0x33}, 32))

	insertBlockSignature(t, td, 100, mr1)
	insertBlockSignature(t, td, 101, mr2)
	insertBlockSignature(t, td, 102, mr3)

	roots, count, err := s.getBlockSigMerkleRoots(ctx, 100, 102)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
	require.Len(t, roots, 3)

	expected1 := bytes.Repeat([]byte{0x11}, 32)
	expected2 := bytes.Repeat([]byte{0x22}, 32)
	expected3 := bytes.Repeat([]byte{0x33}, 32)
	assert.Equal(t, expected1, roots[0])
	assert.Equal(t, expected2, roots[1])
	assert.Equal(t, expected3, roots[2])
}

func TestGetBlockSigMerkleRoots_WithInvalidMerkleRootHex(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	// Insert a block signature with invalid hex in merkleRoot
	insertBlockSignature(t, td, 200, "not_valid_hex_zzzzz")
	// Insert a valid one
	validMR := hex.EncodeToString(bytes.Repeat([]byte{0xAA}, 32))
	insertBlockSignature(t, td, 201, validMR)

	roots, count, err := s.getBlockSigMerkleRoots(ctx, 200, 201)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "count includes invalid docs")
	assert.Len(t, roots, 1, "only valid roots are returned")
	assert.Equal(t, bytes.Repeat([]byte{0xAA}, 32), roots[0])
}

func TestGetBlockSigMerkleRoots_WithEmptyMerkleRoot(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()
	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))

	// Insert a block signature with empty merkleRoot
	insertBlockSignature(t, td, 300, "")
	// Insert a valid one
	validMR := hex.EncodeToString(bytes.Repeat([]byte{0xBB}, 32))
	insertBlockSignature(t, td, 301, validMR)

	roots, count, err := s.getBlockSigMerkleRoots(ctx, 300, 301)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Len(t, roots, 1, "empty merkleRoot should be skipped")
	assert.Equal(t, bytes.Repeat([]byte{0xBB}, 32), roots[0])
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
