package defra

import (
	"bytes"
	"crypto/sha256"
	"sort"

	cid "github.com/ipfs/go-cid"
)

// MerkleProof contains the sibling hashes and path flags needed to verify
// that a single leaf is included in a Merkle tree with a known root.
type MerkleProof struct {
	// Siblings contains the sibling hash at each tree level from leaf to root.
	Siblings [][]byte
	// Path indicates the position at each level. false = sibling is on the right
	// (proven node is left child), true = sibling is on the left (proven node is right child).
	Path []bool
}

// SortedCIDsByBytes returns a copy of cids sorted in canonical byte order.
// This is the same ordering used by node.ComputeMerkleRoot, ensuring the
// stored cids list matches the Merkle tree leaf ordering.
func SortedCIDsByBytes(cids []cid.Cid) []cid.Cid {
	sorted := make([]cid.Cid, len(cids))
	copy(sorted, cids)
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].Bytes(), sorted[j].Bytes()) < 0
	})
	return sorted
}

// SortedCIDStrings returns CID string representations in canonical byte-sorted order.
// Use this when storing CIDs in BlockSignature documents so that the list ordering
// matches the leaf ordering of the Merkle tree computed by node.ComputeMerkleRoot.
func SortedCIDStrings(cids []cid.Cid) []string {
	sorted := SortedCIDsByBytes(cids)
	out := make([]string, len(sorted))
	for i, c := range sorted {
		out[i] = c.String()
	}
	return out
}

func cidLeafHashes(sortedCids []cid.Cid) [][]byte {
	hashes := make([][]byte, len(sortedCids))
	for i, c := range sortedCids {
		h := sha256.Sum256(c.Bytes())
		hashes[i] = h[:]
	}
	return hashes
}

// GenerateMerkleProof builds an inclusion proof for the CID at targetIndex within
// sortedCids. The caller must pass CIDs already sorted in canonical byte order
// (use SortedCIDsByBytes). Returns nil if the list is empty or the index is out of range.
func GenerateMerkleProof(sortedCids []cid.Cid, targetIndex int) *MerkleProof {
	n := len(sortedCids)
	if n == 0 || targetIndex < 0 || targetIndex >= n {
		return nil
	}

	hashes := cidLeafHashes(sortedCids)
	var siblings [][]byte
	var path []bool
	idx := targetIndex
	combined := make([]byte, 64) //nolint:mnd

	for len(hashes) > 1 {
		newLen := (len(hashes) + 1) / 2 //nolint:mnd
		newHashes := make([][]byte, 0, newLen)

		for i := 0; i < len(hashes); i += 2 { //nolint:mnd
			if i+1 < len(hashes) {
				if i == idx || i+1 == idx {
					if idx%2 == 0 { //nolint:mnd
						sibling := make([]byte, 32) //nolint:mnd
						copy(sibling, hashes[i+1])
						siblings = append(siblings, sibling)
						path = append(path, false)
					} else {
						sibling := make([]byte, 32) //nolint:mnd
						copy(sibling, hashes[i])
						siblings = append(siblings, sibling)
						path = append(path, true)
					}
				}
				copy(combined[:32], hashes[i])   //nolint:mnd
				copy(combined[32:], hashes[i+1]) //nolint:mnd
				h := sha256.Sum256(combined)
				newHashes = append(newHashes, h[:])
			} else {
				newHashes = append(newHashes, hashes[i])
			}
		}

		idx /= 2 //nolint:mnd
		hashes = newHashes
	}

	return &MerkleProof{Siblings: siblings, Path: path}
}

// VerifyMerkleProof checks that a leaf CID combined with the proof siblings
// produces the expected Merkle root. Returns true if valid.
func VerifyMerkleProof(leafCID cid.Cid, proof *MerkleProof, expectedRoot []byte) bool {
	if proof == nil || len(expectedRoot) == 0 {
		return false
	}

	current := sha256.Sum256(leafCID.Bytes())
	hash := current[:]

	combined := make([]byte, 64) //nolint:mnd
	for i, sibling := range proof.Siblings {
		if proof.Path[i] {
			copy(combined[:32], sibling) //nolint:mnd
			copy(combined[32:], hash)   //nolint:mnd
		} else {
			copy(combined[:32], hash)    //nolint:mnd
			copy(combined[32:], sibling) //nolint:mnd
		}
		h := sha256.Sum256(combined)
		hash = h[:]
	}

	return bytes.Equal(hash, expectedRoot)
}
