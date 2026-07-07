package defra

import (
	"bytes"
	"crypto/sha256"
	"testing"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeMerkleCID creates a deterministic CIDv1 from arbitrary bytes.
func makeMerkleCID(t *testing.T, data []byte) cid.Cid {
	t.Helper()
	h, err := mh.Sum(data, mh.SHA2_256, -1)
	require.NoError(t, err)
	return cid.NewCidV1(cid.DagCBOR, h)
}

// computeExpectedRoot replicates the merkle tree computation for test assertions.
func computeExpectedRoot(sortedCids []cid.Cid) []byte {
	if len(sortedCids) == 0 {
		return nil
	}
	hashes := make([][]byte, len(sortedCids))
	for i, c := range sortedCids {
		h := sha256.Sum256(c.Bytes())
		hashes[i] = h[:]
	}
	combined := make([]byte, 64) //nolint:mnd
	for len(hashes) > 1 {
		newLen := (len(hashes) + 1) / 2 //nolint:mnd
		newHashes := make([][]byte, 0, newLen)
		for i := 0; i < len(hashes); i += 2 { //nolint:mnd
			if i+1 < len(hashes) {
				copy(combined[:32], hashes[i])   //nolint:mnd
				copy(combined[32:], hashes[i+1]) //nolint:mnd
				h := sha256.Sum256(combined)
				newHashes = append(newHashes, h[:])
			} else {
				newHashes = append(newHashes, hashes[i])
			}
		}
		hashes = newHashes
	}
	return hashes[0]
}

// ===========================================================================
// SortedCIDsByBytes
// ===========================================================================

func TestSortedCIDsByBytes_EmptySlice(t *testing.T) {
	t.Parallel()
	result := SortedCIDsByBytes(nil)
	assert.Empty(t, result)

	result = SortedCIDsByBytes([]cid.Cid{})
	assert.Empty(t, result)
}

func TestSortedCIDsByBytes_SingleCID(t *testing.T) {
	t.Parallel()
	c := makeMerkleCID(t, []byte("single"))
	result := SortedCIDsByBytes([]cid.Cid{c})
	require.Len(t, result, 1)
	assert.Equal(t, c, result[0])
}

func TestSortedCIDsByBytes_DoesNotModifyInput(t *testing.T) {
	t.Parallel()
	a := makeMerkleCID(t, []byte("aaa"))
	b := makeMerkleCID(t, []byte("bbb"))
	c := makeMerkleCID(t, []byte("ccc"))
	input := []cid.Cid{c, a, b}
	original := make([]cid.Cid, len(input))
	copy(original, input)

	SortedCIDsByBytes(input)

	assert.Equal(t, original, input, "input slice must not be modified")
}

func TestSortedCIDsByBytes_ResultSortedByBytes(t *testing.T) {
	t.Parallel()
	cids := make([]cid.Cid, 6)
	for i := range cids {
		cids[i] = makeMerkleCID(t, []byte{byte(i * 17), byte(i * 31)})
	}

	sorted := SortedCIDsByBytes(cids)
	require.Len(t, sorted, len(cids))

	for i := 1; i < len(sorted); i++ {
		assert.LessOrEqual(t,
			bytes.Compare(sorted[i-1].Bytes(), sorted[i].Bytes()), 0,
			"element %d should be <= element %d in byte order", i-1, i,
		)
	}
}

func TestSortedCIDsByBytes_AllUnique(t *testing.T) {
	t.Parallel()
	cids := []cid.Cid{
		makeMerkleCID(t, []byte("alpha")),
		makeMerkleCID(t, []byte("beta")),
		makeMerkleCID(t, []byte("gamma")),
		makeMerkleCID(t, []byte("delta")),
	}
	sorted := SortedCIDsByBytes(cids)
	seen := make(map[string]struct{})
	for _, c := range sorted {
		seen[c.String()] = struct{}{}
	}
	assert.Len(t, seen, len(cids), "all CIDs should be present after sort")
}

// ===========================================================================
// SortedCIDStrings
// ===========================================================================

func TestSortedCIDStrings_EmptySlice(t *testing.T) {
	t.Parallel()
	result := SortedCIDStrings(nil)
	assert.Empty(t, result)
}

func TestSortedCIDStrings_ReturnsStrings(t *testing.T) {
	t.Parallel()
	a := makeMerkleCID(t, []byte("x"))
	b := makeMerkleCID(t, []byte("y"))
	result := SortedCIDStrings([]cid.Cid{b, a})
	require.Len(t, result, 2)
	for _, s := range result {
		assert.NotEmpty(t, s)
	}
}

func TestSortedCIDStrings_OrderMatchesByteSort(t *testing.T) {
	t.Parallel()
	cids := make([]cid.Cid, 5)
	for i := range cids {
		cids[i] = makeMerkleCID(t, []byte{byte(i * 53), byte(i * 97)})
	}

	sortedCIDs := SortedCIDsByBytes(cids)
	sortedStrs := SortedCIDStrings(cids)

	require.Len(t, sortedStrs, len(sortedCIDs))
	for i, c := range sortedCIDs {
		assert.Equal(t, c.String(), sortedStrs[i])
	}
}

// ===========================================================================
// GenerateMerkleProof
// ===========================================================================

func TestGenerateMerkleProof_EmptySlice(t *testing.T) {
	t.Parallel()
	proof := GenerateMerkleProof(nil, 0)
	assert.Nil(t, proof)

	proof = GenerateMerkleProof([]cid.Cid{}, 0)
	assert.Nil(t, proof)
}

func TestGenerateMerkleProof_IndexNegative(t *testing.T) {
	t.Parallel()
	c := makeMerkleCID(t, []byte("a"))
	proof := GenerateMerkleProof([]cid.Cid{c}, -1)
	assert.Nil(t, proof)
}

func TestGenerateMerkleProof_IndexOutOfRange(t *testing.T) {
	t.Parallel()
	c := makeMerkleCID(t, []byte("a"))
	proof := GenerateMerkleProof([]cid.Cid{c}, 1)
	assert.Nil(t, proof)
}

func TestGenerateMerkleProof_SingleCID(t *testing.T) {
	t.Parallel()
	c := makeMerkleCID(t, []byte("solo"))
	proof := GenerateMerkleProof([]cid.Cid{c}, 0)
	require.NotNil(t, proof)
	// Single-element tree has no siblings.
	assert.Empty(t, proof.Siblings)
	assert.Empty(t, proof.Path)
}

func TestGenerateMerkleProof_TwoCIDs_Index0(t *testing.T) {
	t.Parallel()
	a := makeMerkleCID(t, []byte("left"))
	b := makeMerkleCID(t, []byte("right"))
	sorted := SortedCIDsByBytes([]cid.Cid{a, b})

	proof := GenerateMerkleProof(sorted, 0)
	require.NotNil(t, proof)
	assert.Len(t, proof.Siblings, 1)
	assert.Len(t, proof.Path, 1)
	// index 0 is left child → sibling is on right → path[0] == false
	assert.False(t, proof.Path[0])
}

func TestGenerateMerkleProof_TwoCIDs_Index1(t *testing.T) {
	t.Parallel()
	a := makeMerkleCID(t, []byte("left"))
	b := makeMerkleCID(t, []byte("right"))
	sorted := SortedCIDsByBytes([]cid.Cid{a, b})

	proof := GenerateMerkleProof(sorted, 1)
	require.NotNil(t, proof)
	assert.Len(t, proof.Siblings, 1)
	assert.Len(t, proof.Path, 1)
	// index 1 is right child → sibling is on left → path[0] == true
	assert.True(t, proof.Path[0])
}

func TestGenerateMerkleProof_SiblingsAndPathSameLength(t *testing.T) {
	t.Parallel()
	cids := make([]cid.Cid, 8)
	for i := range cids {
		cids[i] = makeMerkleCID(t, []byte{byte(i)})
	}
	sorted := SortedCIDsByBytes(cids)

	for idx := range sorted {
		proof := GenerateMerkleProof(sorted, idx)
		require.NotNil(t, proof, "proof for index %d should not be nil", idx)
		assert.Len(t, proof.Siblings, len(proof.Path),
			"siblings and path should be the same length for index %d", idx)
	}
}

// ===========================================================================
// VerifyMerkleProof
// ===========================================================================

func TestVerifyMerkleProof_NilProof(t *testing.T) {
	t.Parallel()
	c := makeMerkleCID(t, []byte("leaf"))
	result := VerifyMerkleProof(c, nil, make([]byte, 32))
	assert.False(t, result)
}

func TestVerifyMerkleProof_EmptyRoot(t *testing.T) {
	t.Parallel()
	c := makeMerkleCID(t, []byte("leaf"))
	proof := &MerkleProof{}
	result := VerifyMerkleProof(c, proof, nil)
	assert.False(t, result)

	result = VerifyMerkleProof(c, proof, []byte{})
	assert.False(t, result)
}

func TestVerifyMerkleProof_SingleCID_Valid(t *testing.T) {
	t.Parallel()
	c := makeMerkleCID(t, []byte("only"))
	sorted := []cid.Cid{c}
	root := computeExpectedRoot(sorted)

	proof := GenerateMerkleProof(sorted, 0)
	require.NotNil(t, proof)

	assert.True(t, VerifyMerkleProof(c, proof, root))
}

func TestVerifyMerkleProof_TwoCIDs_BothValid(t *testing.T) {
	t.Parallel()
	a := makeMerkleCID(t, []byte("first"))
	b := makeMerkleCID(t, []byte("second"))
	sorted := SortedCIDsByBytes([]cid.Cid{a, b})
	root := computeExpectedRoot(sorted)

	proof0 := GenerateMerkleProof(sorted, 0)
	require.NotNil(t, proof0)
	assert.True(t, VerifyMerkleProof(sorted[0], proof0, root))

	proof1 := GenerateMerkleProof(sorted, 1)
	require.NotNil(t, proof1)
	assert.True(t, VerifyMerkleProof(sorted[1], proof1, root))
}

func TestVerifyMerkleProof_WrongLeaf(t *testing.T) {
	t.Parallel()
	a := makeMerkleCID(t, []byte("a"))
	b := makeMerkleCID(t, []byte("b"))
	sorted := SortedCIDsByBytes([]cid.Cid{a, b})
	root := computeExpectedRoot(sorted)

	proof := GenerateMerkleProof(sorted, 0)
	require.NotNil(t, proof)

	impostor := makeMerkleCID(t, []byte("impostor"))
	assert.False(t, VerifyMerkleProof(impostor, proof, root))
}

func TestVerifyMerkleProof_WrongRoot(t *testing.T) {
	t.Parallel()
	a := makeMerkleCID(t, []byte("a"))
	b := makeMerkleCID(t, []byte("b"))
	sorted := SortedCIDsByBytes([]cid.Cid{a, b})

	proof := GenerateMerkleProof(sorted, 0)
	require.NotNil(t, proof)

	wrongRoot := make([]byte, 32)
	wrongRoot[0] = 0xFF
	assert.False(t, VerifyMerkleProof(sorted[0], proof, wrongRoot))
}

func TestVerifyMerkleProof_SwappedProofAndLeaf(t *testing.T) {
	t.Parallel()
	a := makeMerkleCID(t, []byte("a"))
	b := makeMerkleCID(t, []byte("b"))
	sorted := SortedCIDsByBytes([]cid.Cid{a, b})
	root := computeExpectedRoot(sorted)

	// Proof generated for index 0, but verifying with sorted[1]
	proof := GenerateMerkleProof(sorted, 0)
	require.NotNil(t, proof)
	assert.False(t, VerifyMerkleProof(sorted[1], proof, root))
}

// ===========================================================================
// GenerateMerkleProof + VerifyMerkleProof roundtrip
// ===========================================================================

func TestMerkleProof_Roundtrip_FourCIDs(t *testing.T) {
	t.Parallel()
	cids := []cid.Cid{
		makeMerkleCID(t, []byte("block")),
		makeMerkleCID(t, []byte("tx")),
		makeMerkleCID(t, []byte("log")),
		makeMerkleCID(t, []byte("ale")),
	}
	sorted := SortedCIDsByBytes(cids)
	root := computeExpectedRoot(sorted)

	for idx, c := range sorted {
		proof := GenerateMerkleProof(sorted, idx)
		require.NotNil(t, proof, "proof for index %d", idx)
		assert.True(t, VerifyMerkleProof(c, proof, root), "proof should verify for index %d", idx)
	}
}

func TestMerkleProof_Roundtrip_OddCount(t *testing.T) {
	t.Parallel()
	cids := make([]cid.Cid, 5)
	for i := range cids {
		cids[i] = makeMerkleCID(t, []byte{byte(i + 1)})
	}
	sorted := SortedCIDsByBytes(cids)
	root := computeExpectedRoot(sorted)

	for idx, c := range sorted {
		proof := GenerateMerkleProof(sorted, idx)
		require.NotNil(t, proof, "proof for index %d", idx)
		assert.True(t, VerifyMerkleProof(c, proof, root), "proof should verify for index %d", idx)
	}
}

func TestMerkleProof_Roundtrip_LargeSet(t *testing.T) {
	t.Parallel()
	cids := make([]cid.Cid, 32)
	for i := range cids {
		cids[i] = makeMerkleCID(t, []byte{byte(i), byte(i + 100)})
	}
	sorted := SortedCIDsByBytes(cids)
	root := computeExpectedRoot(sorted)

	for idx, c := range sorted {
		proof := GenerateMerkleProof(sorted, idx)
		require.NotNil(t, proof, "proof for index %d", idx)
		assert.True(t, VerifyMerkleProof(c, proof, root), "proof should verify for index %d", idx)
	}
}

func TestMerkleProof_ProofFromWrongTree_Fails(t *testing.T) {
	t.Parallel()
	treeA := SortedCIDsByBytes([]cid.Cid{
		makeMerkleCID(t, []byte("a1")),
		makeMerkleCID(t, []byte("a2")),
	})
	treeB := SortedCIDsByBytes([]cid.Cid{
		makeMerkleCID(t, []byte("b1")),
		makeMerkleCID(t, []byte("b2")),
	})
	rootA := computeExpectedRoot(treeA)

	// Proof from tree B should not verify against root A.
	proofB := GenerateMerkleProof(treeB, 0)
	require.NotNil(t, proofB)
	assert.False(t, VerifyMerkleProof(treeB[0], proofB, rootA))
}
