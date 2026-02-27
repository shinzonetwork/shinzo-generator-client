package snapshot

import (
	"bufio"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sourcenetwork/defradb/crypto"
)

// VerifyResult holds the result of a snapshot verification.
type VerifyResult struct {
	Valid             bool   `json:"valid"`
	SnapshotFile      string `json:"snapshot_file"`
	StartBlock        int64  `json:"start_block"`
	EndBlock          int64  `json:"end_block"`
	BlockCount        int    `json:"block_count"`
	BlockSigsFound    int    `json:"block_sigs_found"`
	MerkleRootMatch   bool   `json:"merkle_root_match"`
	SignatureValid    bool   `json:"signature_valid"`
	SignerIdentity    string `json:"signer_identity"`
	Error             string `json:"error,omitempty"`
}

// VerifySnapshot verifies a snapshot file against its sidecar signature.
// It reads the snapshot to extract block signature Merkle roots, recomputes
// the snapshot Merkle root, and verifies both the root match and the
// cryptographic signature.
func VerifySnapshot(snapshotPath string) (*VerifyResult, error) {
	// Derive sidecar path
	sigPath := strings.TrimSuffix(snapshotPath, ".jsonl.gz") + ".sig.json"

	sigData, err := os.ReadFile(sigPath)
	if err != nil {
		return nil, fmt.Errorf("read signature file: %w", err)
	}
	var sig SnapshotSignatureData
	if err := json.Unmarshal(sigData, &sig); err != nil {
		return nil, fmt.Errorf("parse signature file: %w", err)
	}

	return VerifySnapshotWithSig(snapshotPath, &sig)
}

// VerifySnapshotWithSig verifies a snapshot file against a provided signature.
func VerifySnapshotWithSig(snapshotPath string, sig *SnapshotSignatureData) (*VerifyResult, error) {
	result := &VerifyResult{
		SnapshotFile:   sig.SnapshotFile,
		StartBlock:     sig.StartBlock,
		EndBlock:       sig.EndBlock,
		BlockCount:     sig.BlockCount,
		SignerIdentity: sig.SignatureIdentity,
	}

	// Extract block sig merkle roots from the snapshot file
	roots, err := extractBlockSigMerkleRoots(snapshotPath)
	if err != nil {
		result.Error = fmt.Sprintf("extract block sig roots: %v", err)
		return result, nil
	}
	result.BlockSigsFound = len(roots)

	if len(roots) == 0 {
		result.Error = "no block signatures found in snapshot"
		return result, nil
	}

	// Recompute Merkle root
	computedRoot := ComputeSnapshotMerkleRoot(roots)
	computedRootHex := hex.EncodeToString(computedRoot)
	result.MerkleRootMatch = computedRootHex == sig.MerkleRoot

	if !result.MerkleRootMatch {
		result.Error = fmt.Sprintf("merkle root mismatch: computed %s, expected %s", computedRootHex, sig.MerkleRoot)
		return result, nil
	}

	// Verify cryptographic signature
	merkleRootBytes, err := hex.DecodeString(sig.MerkleRoot)
	if err != nil {
		result.Error = fmt.Sprintf("decode merkle root hex: %v", err)
		return result, nil
	}

	sigValueBytes, err := hex.DecodeString(sig.SignatureValue)
	if err != nil {
		result.Error = fmt.Sprintf("decode signature hex: %v", err)
		return result, nil
	}

	var keyType crypto.KeyType
	switch sig.SignatureType {
	case "ES256K", "ecdsa-256k":
		keyType = crypto.KeyTypeSecp256k1
	case "Ed25519", "ed25519":
		keyType = crypto.KeyTypeEd25519
	default:
		result.Error = fmt.Sprintf("unsupported signature type: %s", sig.SignatureType)
		return result, nil
	}

	pubKey, err := crypto.PublicKeyFromString(keyType, sig.SignatureIdentity)
	if err != nil {
		result.Error = fmt.Sprintf("parse public key: %v", err)
		return result, nil
	}

	valid, err := pubKey.Verify(merkleRootBytes, sigValueBytes)
	if err != nil {
		result.Error = fmt.Sprintf("verify signature: %v", err)
		return result, nil
	}
	result.SignatureValid = valid

	result.Valid = result.MerkleRootMatch && result.SignatureValid
	if !result.Valid && result.Error == "" {
		result.Error = "signature verification failed"
	}

	return result, nil
}

// extractBlockSigMerkleRoots reads a snapshot file and extracts the merkleRoot
// values from block_signature entries, in the order they appear (by blockNumber ASC).
func extractBlockSigMerkleRoots(snapshotPath string) ([][]byte, error) {
	f, err := os.Open(snapshotPath)
	if err != nil {
		return nil, fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()

	var reader *bufio.Scanner
	if strings.HasSuffix(snapshotPath, ".gz") {
		gr, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gr.Close()
		reader = bufio.NewScanner(gr)
	} else {
		reader = bufio.NewScanner(f)
	}
	reader.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	var roots [][]byte

	for reader.Scan() {
		var entry struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(reader.Bytes(), &entry); err != nil {
			continue
		}

		if entry.Type != "block_signature" || entry.Data == nil {
			continue
		}

		mrStr, ok := entry.Data["merkleRoot"].(string)
		if !ok || mrStr == "" {
			continue
		}

		mrBytes, err := hex.DecodeString(mrStr)
		if err != nil {
			continue
		}
		roots = append(roots, mrBytes)
	}

	if err := reader.Err(); err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}

	return roots, nil
}
