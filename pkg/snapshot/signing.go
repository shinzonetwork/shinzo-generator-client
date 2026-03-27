package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/node"
)

// SnapshotSignatureData holds the cryptographic signature for a snapshot file.
type SnapshotSignatureData struct {
	Version              int      `json:"version"`
	SnapshotFile         string   `json:"snapshot_file"`
	StartBlock           int64    `json:"start_block"`
	EndBlock             int64    `json:"end_block"`
	MerkleRoot           string   `json:"merkle_root"`
	BlockCount           int      `json:"block_count"`
	SignatureType        string   `json:"signature_type"`
	SignatureIdentity    string   `json:"signature_identity"`
	SignatureValue       string   `json:"signature_value"`
	CreatedAt            string   `json:"created_at"`
	BlockSigMerkleRoots  []string `json:"block_sig_merkle_roots,omitempty"`
}

// ComputeSnapshotMerkleRoot computes a Merkle root from per-block block signature
// Merkle roots. The input must be sorted by block number (ascending).
// This mirrors the algorithm in defradb/internal/core/block/block_signing.go.
func ComputeSnapshotMerkleRoot(blockSigMerkleRoots [][]byte) []byte {
	if len(blockSigMerkleRoots) == 0 {
		return nil
	}

	hashes := make([][]byte, len(blockSigMerkleRoots))
	for i, root := range blockSigMerkleRoots {
		hash := sha256.Sum256(root)
		hashes[i] = hash[:]
	}

	combined := make([]byte, 64)
	for len(hashes) > 1 {
		newLen := (len(hashes) + 1) / 2
		newHashes := make([][]byte, 0, newLen)
		for i := 0; i < len(hashes); i += 2 {
			if i+1 < len(hashes) {
				copy(combined[:32], hashes[i])
				copy(combined[32:], hashes[i+1])
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

// getBlockSigMerkleRoots queries DefraDB for block signature Merkle roots
// in the given block range, returned sorted by blockNumber ASC.
func getBlockSigMerkleRoots(ctx context.Context, defraNode *node.Node, startBlock, endBlock int64) ([][]byte, int, error) {
	query := fmt.Sprintf(
		`query { %s(filter: {blockNumber: {_geq: %d, _leq: %d}}, order: {blockNumber: ASC}) { merkleRoot } }`,
		constants.CollectionBlockSignature, startBlock, endBlock,
	)

	result := defraNode.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return nil, 0, fmt.Errorf("query block signatures: %v", result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return nil, 0, nil
	}

	raw := data[constants.CollectionBlockSignature]
	if raw == nil {
		return nil, 0, nil
	}

	var docs []map[string]any
	switch typed := raw.(type) {
	case []any:
		for _, d := range typed {
			if m, ok := d.(map[string]any); ok {
				docs = append(docs, m)
			}
		}
	case []map[string]any:
		docs = typed
	default:
		return nil, 0, nil
	}

	roots := make([][]byte, 0, len(docs))
	for _, doc := range docs {
		mrStr, ok := doc["merkleRoot"].(string)
		if !ok || mrStr == "" {
			continue
		}
		mrBytes, err := hex.DecodeString(mrStr)
		if err != nil {
			logger.Sugar.Warnf("Snapshot signing: invalid merkleRoot hex %q: %v", mrStr, err)
			continue
		}
		roots = append(roots, mrBytes)
	}

	return roots, len(docs), nil
}

// signMerkleRoot signs the given Merkle root using the identity from context.
// Returns signature type, identity string, and signature bytes.
func signMerkleRoot(ctx context.Context, merkleRoot []byte) (sigType, sigIdentity string, sigValue []byte, err error) {
	ident := identity.FromContext(ctx)
	if !ident.HasValue() {
		return "", "", nil, fmt.Errorf("no identity in context")
	}

	fullIdent, ok := ident.Value().(identity.FullIdentity)
	if !ok {
		return "", "", nil, fmt.Errorf("identity is not a full identity (no private key)")
	}

	switch fullIdent.PrivateKey().Type() {
	case crypto.KeyTypeSecp256k1:
		sigType = "ES256K"
	case crypto.KeyTypeEd25519:
		sigType = "Ed25519"
	default:
		return "", "", nil, fmt.Errorf("unsupported key type: %v", fullIdent.PrivateKey().Type())
	}

	sigValue, err = fullIdent.PrivateKey().Sign(merkleRoot)
	if err != nil {
		return "", "", nil, fmt.Errorf("sign merkle root: %w", err)
	}

	sigIdentity = fullIdent.PublicKey().String()
	return sigType, sigIdentity, sigValue, nil
}

// createSnapshotSignatureDoc creates a SnapshotSignature document in DefraDB.
func createSnapshotSignatureDoc(ctx context.Context, defraNode *node.Node, sig *SnapshotSignatureData) error {
	txn, err := defraNode.DB.NewBlindWriteTxn()
	if err != nil {
		return fmt.Errorf("new txn: %w", err)
	}

	col, err := txn.GetCollectionByName(ctx, constants.CollectionSnapshotSignature)
	if err != nil {
		txn.Discard()
		return fmt.Errorf("get collection: %w", err)
	}

	data := map[string]any{
		"startBlock":          sig.StartBlock,
		"endBlock":            sig.EndBlock,
		"merkleRoot":          sig.MerkleRoot,
		"blockCount":          sig.BlockCount,
		"signatureType":       sig.SignatureType,
		"signatureIdentity":   sig.SignatureIdentity,
		"signatureValue":      sig.SignatureValue,
		"snapshotFile":        sig.SnapshotFile,
		"createdAt":           sig.CreatedAt,
		"blockSigMerkleRoots": sig.BlockSigMerkleRoots,
	}

	doc, err := client.NewDocFromMap(ctx, data, col.Version())
	if err != nil {
		txn.Discard()
		return fmt.Errorf("create doc from map: %w", err)
	}

	if err := col.Create(ctx, doc); err != nil {
		txn.Discard()
		return fmt.Errorf("create doc: %w", err)
	}

	if err := txn.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// QuerySnapshotSignatures queries DefraDB for all SnapshotSignature documents
// and returns them keyed by snapshot filename for easy lookup.
func QuerySnapshotSignatures(ctx context.Context, defraNode *node.Node) (map[string]*SnapshotSignatureData, error) {
	query := fmt.Sprintf(
		`query { %s { startBlock endBlock merkleRoot blockCount signatureType signatureIdentity signatureValue snapshotFile createdAt blockSigMerkleRoots } }`,
		constants.CollectionSnapshotSignature,
	)

	result := defraNode.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return nil, fmt.Errorf("query snapshot signatures: %v", result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return make(map[string]*SnapshotSignatureData), nil
	}

	raw := data[constants.CollectionSnapshotSignature]
	if raw == nil {
		return make(map[string]*SnapshotSignatureData), nil
	}

	var docs []map[string]any
	switch typed := raw.(type) {
	case []any:
		for _, d := range typed {
			if m, ok := d.(map[string]any); ok {
				docs = append(docs, m)
			}
		}
	case []map[string]any:
		docs = typed
	}

	sigs := make(map[string]*SnapshotSignatureData, len(docs))
	for _, doc := range docs {
		sig := &SnapshotSignatureData{
			Version: 1,
		}
		if v, ok := doc["snapshotFile"].(string); ok {
			sig.SnapshotFile = v
		}
		if v, ok := doc["startBlock"].(int64); ok {
			sig.StartBlock = v
		}
		if v, ok := doc["endBlock"].(int64); ok {
			sig.EndBlock = v
		}
		if v, ok := doc["merkleRoot"].(string); ok {
			sig.MerkleRoot = v
		}
		if v, ok := doc["blockCount"].(int64); ok {
			sig.BlockCount = int(v)
		}
		if v, ok := doc["signatureType"].(string); ok {
			sig.SignatureType = v
		}
		if v, ok := doc["signatureIdentity"].(string); ok {
			sig.SignatureIdentity = v
		}
		if v, ok := doc["signatureValue"].(string); ok {
			sig.SignatureValue = v
		}
		if v, ok := doc["createdAt"].(string); ok {
			sig.CreatedAt = v
		}
		if raw, ok := doc["blockSigMerkleRoots"]; ok && raw != nil {
			switch typed := raw.(type) {
			case []any:
				for _, item := range typed {
					if s, ok := item.(string); ok {
						sig.BlockSigMerkleRoots = append(sig.BlockSigMerkleRoots, s)
					}
				}
			case []string:
				sig.BlockSigMerkleRoots = typed
			}
		}
		if sig.SnapshotFile != "" {
			sigs[sig.SnapshotFile] = sig
		}
	}

	return sigs, nil
}

// signSnapshotWithRoots signs a snapshot using pre-queried block sig roots.
// This avoids re-querying roots (which could race with concurrent block commits).
func signSnapshotWithRoots(ctx context.Context, defraNode *node.Node, snapshotFilename string, startBlock, endBlock int64, roots [][]byte, blockCount int) error {
	if len(roots) == 0 {
		logger.Sugar.Warnf("Snapshot signing: no block signatures found for blocks %d-%d, skipping signing", startBlock, endBlock)
		return nil
	}

	// Compute snapshot Merkle root
	snapshotRoot := ComputeSnapshotMerkleRoot(roots)
	if snapshotRoot == nil {
		return fmt.Errorf("failed to compute snapshot merkle root")
	}

	// Sign
	sigType, sigIdentity, sigValue, err := signMerkleRoot(ctx, snapshotRoot)
	if err != nil {
		logger.Sugar.Warnf("Snapshot signing: %v, skipping signing", err)
		return nil
	}

	// Convert block sig roots to hex strings for storage
	blockSigRootStrs := make([]string, len(roots))
	for i, r := range roots {
		blockSigRootStrs[i] = hex.EncodeToString(r)
	}

	sig := &SnapshotSignatureData{
		Version:             1,
		SnapshotFile:        snapshotFilename,
		StartBlock:          startBlock,
		EndBlock:            endBlock,
		MerkleRoot:          hex.EncodeToString(snapshotRoot),
		BlockCount:          blockCount,
		SignatureType:       sigType,
		SignatureIdentity:   sigIdentity,
		SignatureValue:      hex.EncodeToString(sigValue),
		CreatedAt:           time.Now().UTC().Format(time.RFC3339),
		BlockSigMerkleRoots: blockSigRootStrs,
	}

	// Create DefraDB document
	if err := createSnapshotSignatureDoc(ctx, defraNode, sig); err != nil {
		logger.Sugar.Warnf("Snapshot signing: failed to create DefraDB document: %v", err)
		// Don't fail the whole operation - sidecar was written successfully
	}

	logger.Sugar.Infof("Snapshot signed: %s (merkle: %s, blocks: %d, signer: %s)",
		snapshotFilename, sig.MerkleRoot[:16]+"...", blockCount, sigIdentity[:16]+"...")
	return nil
}
