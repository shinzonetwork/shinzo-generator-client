package defra

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"testing"

	gocid "github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

// TestMerkleRootComputation verifies that the merkle root computation
// matches between the indexer and the host verification logic.
func TestMerkleRootComputation(t *testing.T) {
	// Create some test CIDs (simulating what DefraDB would generate)
	testData := [][]byte{
		[]byte("test document 1"),
		[]byte("test document 2"),
		[]byte("test document 3"),
		[]byte("test document 4"),
		[]byte("test document 5"),
	}

	// Create CIDs from test data
	cids := make([]gocid.Cid, len(testData))
	for i, data := range testData {
		hash, err := multihash.Sum(data, multihash.SHA2_256, -1)
		if err != nil {
			t.Fatalf("Failed to create multihash: %v", err)
		}
		cids[i] = gocid.NewCidV1(gocid.Raw, hash)
	}

	// Compute merkle root using indexer method (simulating DefraDB's ComputeMerkleRoot)
	indexerRoot := computeMerkleRootIndexer(cids)

	// Convert CIDs to strings (simulating P2P transfer)
	cidStrings := make([]string, len(cids))
	for i, c := range cids {
		cidStrings[i] = c.String()
	}

	// Compute merkle root using host method (simulating host's ComputeMerkleRootFromStrings)
	hostRoot := computeMerkleRootHost(cidStrings)

	// Compare
	if hex.EncodeToString(indexerRoot) != hex.EncodeToString(hostRoot) {
		t.Errorf("Merkle roots don't match!\nIndexer: %s\nHost:    %s",
			hex.EncodeToString(indexerRoot),
			hex.EncodeToString(hostRoot))
	} else {
		t.Logf("✅ Merkle roots match: %s", hex.EncodeToString(indexerRoot))
	}
}

// TestMerkleRootWithOddNumberOfCIDs tests merkle root with odd number of CIDs
func TestMerkleRootWithOddNumberOfCIDs(t *testing.T) {
	testData := [][]byte{
		[]byte("doc 1"),
		[]byte("doc 2"),
		[]byte("doc 3"),
	}

	cids := make([]gocid.Cid, len(testData))
	for i, data := range testData {
		hash, err := multihash.Sum(data, multihash.SHA2_256, -1)
		if err != nil {
			t.Fatalf("Failed to create multihash: %v", err)
		}
		cids[i] = gocid.NewCidV1(gocid.Raw, hash)
	}

	indexerRoot := computeMerkleRootIndexer(cids)

	cidStrings := make([]string, len(cids))
	for i, c := range cids {
		cidStrings[i] = c.String()
	}

	hostRoot := computeMerkleRootHost(cidStrings)

	if hex.EncodeToString(indexerRoot) != hex.EncodeToString(hostRoot) {
		t.Errorf("Merkle roots don't match for odd CID count!\nIndexer: %s\nHost:    %s",
			hex.EncodeToString(indexerRoot),
			hex.EncodeToString(hostRoot))
	} else {
		t.Logf("✅ Merkle roots match (odd count): %s", hex.EncodeToString(indexerRoot))
	}
}

// TestMerkleRootWithSingleCID tests merkle root with single CID
func TestMerkleRootWithSingleCID(t *testing.T) {
	hash, err := multihash.Sum([]byte("single doc"), multihash.SHA2_256, -1)
	if err != nil {
		t.Fatalf("Failed to create multihash: %v", err)
	}
	cid := gocid.NewCidV1(gocid.Raw, hash)

	indexerRoot := computeMerkleRootIndexer([]gocid.Cid{cid})
	hostRoot := computeMerkleRootHost([]string{cid.String()})

	if hex.EncodeToString(indexerRoot) != hex.EncodeToString(hostRoot) {
		t.Errorf("Merkle roots don't match for single CID!\nIndexer: %s\nHost:    %s",
			hex.EncodeToString(indexerRoot),
			hex.EncodeToString(hostRoot))
	} else {
		t.Logf("✅ Merkle roots match (single): %s", hex.EncodeToString(indexerRoot))
	}
}

// TestMerkleRootDeterminism verifies that merkle root is deterministic regardless of input order
func TestMerkleRootDeterminism(t *testing.T) {
	testData := [][]byte{
		[]byte("alpha"),
		[]byte("beta"),
		[]byte("gamma"),
		[]byte("delta"),
	}

	cids := make([]gocid.Cid, len(testData))
	for i, data := range testData {
		hash, err := multihash.Sum(data, multihash.SHA2_256, -1)
		if err != nil {
			t.Fatalf("Failed to create multihash: %v", err)
		}
		cids[i] = gocid.NewCidV1(gocid.Raw, hash)
	}

	// Compute with original order
	root1 := computeMerkleRootIndexer(cids)

	// Reverse the order
	reversed := make([]gocid.Cid, len(cids))
	for i, c := range cids {
		reversed[len(cids)-1-i] = c
	}
	root2 := computeMerkleRootIndexer(reversed)

	// Shuffle randomly
	shuffled := []gocid.Cid{cids[2], cids[0], cids[3], cids[1]}
	root3 := computeMerkleRootIndexer(shuffled)

	if hex.EncodeToString(root1) != hex.EncodeToString(root2) {
		t.Errorf("Merkle root not deterministic! original vs reversed")
	}
	if hex.EncodeToString(root1) != hex.EncodeToString(root3) {
		t.Errorf("Merkle root not deterministic! original vs shuffled")
	}

	t.Logf("✅ Merkle root is deterministic: %s", hex.EncodeToString(root1))
}

// TestEmptyCIDs verifies behavior with empty CID list
func TestEmptyCIDs(t *testing.T) {
	indexerRoot := computeMerkleRootIndexer([]gocid.Cid{})
	hostRoot := computeMerkleRootHost([]string{})

	if indexerRoot != nil {
		t.Errorf("Indexer should return nil for empty CIDs, got: %s", hex.EncodeToString(indexerRoot))
	}
	if hostRoot != nil {
		t.Errorf("Host should return nil for empty CIDs, got: %s", hex.EncodeToString(hostRoot))
	}

	t.Log("✅ Empty CID handling correct")
}

// TestLargeBatch tests with a larger batch of CIDs (simulating a real block)
func TestLargeBatch(t *testing.T) {
	// Simulate a block with 100 documents (block + txs + logs + ALEs)
	numDocs := 100
	testData := make([][]byte, numDocs)
	for i := 0; i < numDocs; i++ {
		testData[i] = []byte("document " + string(rune('0'+i%10)) + string(rune('0'+i/10)))
	}

	cids := make([]gocid.Cid, len(testData))
	for i, data := range testData {
		hash, err := multihash.Sum(data, multihash.SHA2_256, -1)
		if err != nil {
			t.Fatalf("Failed to create multihash: %v", err)
		}
		cids[i] = gocid.NewCidV1(gocid.Raw, hash)
	}

	indexerRoot := computeMerkleRootIndexer(cids)

	cidStrings := make([]string, len(cids))
	for i, c := range cids {
		cidStrings[i] = c.String()
	}

	hostRoot := computeMerkleRootHost(cidStrings)

	if hex.EncodeToString(indexerRoot) != hex.EncodeToString(hostRoot) {
		t.Errorf("Merkle roots don't match for large batch (%d CIDs)!\nIndexer: %s\nHost:    %s",
			numDocs,
			hex.EncodeToString(indexerRoot),
			hex.EncodeToString(hostRoot))
	} else {
		t.Logf("✅ Merkle roots match for %d CIDs: %s", numDocs, hex.EncodeToString(indexerRoot)[:16]+"...")
	}
}

// computeMerkleRootIndexer simulates DefraDB's ComputeMerkleRoot function
// This is a copy of the logic from defradb/internal/core/block/batch_signing.go
func computeMerkleRootIndexer(cids []gocid.Cid) []byte {
	if len(cids) == 0 {
		return nil
	}

	// Sort CIDs by string representation
	sortedCids := make([]gocid.Cid, len(cids))
	copy(sortedCids, cids)
	sort.Slice(sortedCids, func(i, j int) bool {
		return sortedCids[i].String() < sortedCids[j].String()
	})

	// Hash each CID's raw bytes
	hashes := make([][]byte, len(sortedCids))
	for i, c := range sortedCids {
		hash := sha256.Sum256(c.Bytes())
		hashes[i] = hash[:]
	}

	// Reduce to single root
	for len(hashes) > 1 {
		var newHashes [][]byte
		for i := 0; i < len(hashes); i += 2 {
			if i+1 < len(hashes) {
				combined := append(hashes[i], hashes[i+1]...)
				hash := sha256.Sum256(combined)
				newHashes = append(newHashes, hash[:])
			} else {
				newHashes = append(newHashes, hashes[i])
			}
		}
		hashes = newHashes
	}

	return hashes[0]
}

// computeMerkleRootHost simulates the host's ComputeMerkleRootFromStrings function
// This is a copy of the logic from shinzo-host-client/pkg/attestation/batchSignatureVerifier.go
func computeMerkleRootHost(cidStrings []string) []byte {
	if len(cidStrings) == 0 {
		return nil
	}

	// Parse CID strings into CID objects
	parsedCids := make([]gocid.Cid, 0, len(cidStrings))
	for _, cidStr := range cidStrings {
		c, err := gocid.Decode(cidStr)
		if err != nil {
			continue
		}
		parsedCids = append(parsedCids, c)
	}

	if len(parsedCids) == 0 {
		return nil
	}

	// Sort CIDs by their string representation
	sort.Slice(parsedCids, func(i, j int) bool {
		return parsedCids[i].String() < parsedCids[j].String()
	})

	// Hash each CID's raw bytes
	hashes := make([][]byte, len(parsedCids))
	for i, c := range parsedCids {
		hash := sha256.Sum256(c.Bytes())
		hashes[i] = hash[:]
	}

	// Reduce to single root
	for len(hashes) > 1 {
		var newHashes [][]byte
		for i := 0; i < len(hashes); i += 2 {
			if i+1 < len(hashes) {
				combined := append(hashes[i], hashes[i+1]...)
				hash := sha256.Sum256(combined)
				newHashes = append(newHashes, hash[:])
			} else {
				newHashes = append(newHashes, hashes[i])
			}
		}
		hashes = newHashes
	}

	return hashes[0]
}
