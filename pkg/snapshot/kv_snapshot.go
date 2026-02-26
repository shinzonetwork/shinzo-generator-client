package snapshot

import (
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
)

// kvSnapshotHeader is written at the start of a .kvsnap.gz file.
type kvSnapshotHeader struct {
	Magic                string   `json:"magic"`
	Version              int      `json:"version"`
	StartBlock           int64    `json:"start_block"`
	EndBlock             int64    `json:"end_block"`
	CreatedAt            string   `json:"created_at"`
	BlockSigMerkleRoots  []string `json:"block_sig_merkle_roots,omitempty"`
}

// createKVSnapshot exports raw Badger KV pairs for a block range to a binary file.
func (s *Snapshotter) createKVSnapshot(ctx context.Context, startBlock, endBlock int64) error {
	filename := fmt.Sprintf("snapshot_%d_%d.kvsnap.gz", startBlock, endBlock)
	filePath := filepath.Join(s.cfg.Dir, filename)
	tmpPath := filePath + ".tmp"

	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	gw := gzip.NewWriter(f)

	// Ensure cleanup on any error path
	committed := false
	defer func() {
		if !committed {
			gw.Close()
			f.Close()
			os.Remove(tmpPath)
		}
	}()

	// Query block sig merkle roots to embed in header for host-side verification
	roots, _, err := getBlockSigMerkleRoots(ctx, s.defraNode, startBlock, endBlock)
	if err != nil {
		logger.Sugar.Warnf("KV snapshot: failed to get block sig roots: %v", err)
	}
	var rootsHex []string
	for _, r := range roots {
		rootsHex = append(rootsHex, hex.EncodeToString(r))
	}

	// Write header as length-prefixed JSON
	header := kvSnapshotHeader{
		Magic:               "DFKV",
		Version:             1,
		StartBlock:          startBlock,
		EndBlock:            endBlock,
		CreatedAt:           time.Now().UTC().Format(time.RFC3339),
		BlockSigMerkleRoots: rootsHex,
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("marshal header: %w", err)
	}

	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(headerBytes)))
	if _, err := gw.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := gw.Write(headerBytes); err != nil {
		return err
	}

	// Collections to export with their block number field name
	collections := []struct {
		name       string
		blockField string
	}{
		{constants.CollectionBlock, "number"},
		{constants.CollectionTransaction, "blockNumber"},
		{constants.CollectionLog, "blockNumber"},
		{constants.CollectionAccessListEntry, "blockNumber"},
		{constants.CollectionBlockSignature, "blockNumber"},
	}

	totalKVs := 0
	for _, col := range collections {
		docIDs, err := s.queryDocIDs(ctx, col.name, col.blockField, startBlock, endBlock)
		if err != nil {
			return fmt.Errorf("query docIDs for %s: %w", col.name, err)
		}

		if len(docIDs) == 0 {
			continue
		}

		n, err := s.defraNode.DB.ExportDocKVs(ctx, col.name, docIDs, gw, true)
		if err != nil {
			return fmt.Errorf("export KVs for %s: %w", col.name, err)
		}
		totalKVs += n
		logger.Sugar.Debugf("Exported %d KV pairs for %s (%d docs)", n, col.name, len(docIDs))
	}

	// Write EOF marker (key_len = 0)
	binary.BigEndian.PutUint32(lenBuf[:], 0)
	if _, err := gw.Write(lenBuf[:]); err != nil {
		return err
	}

	if err := gw.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// Atomic rename — past this point, cleanup should not remove the file
	committed = true
	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	logger.Sugar.Infof("KV snapshot created: %s (%d KV pairs)", filename, totalKVs)

	// Sign the snapshot using the same roots embedded in the header
	if err := signSnapshotWithRoots(s.ctx, s.defraNode, filename, startBlock, endBlock, roots, len(roots)); err != nil {
		return fmt.Errorf("snapshot signing failed for %s: %w", filename, err)
	}

	return nil
}

// queryDocIDs returns all document IDs for a collection in the given block range.
func (s *Snapshotter) queryDocIDs(ctx context.Context, collection, blockField string, startBlock, endBlock int64) ([]string, error) {
	var allDocIDs []string

	for chunkStart := startBlock; chunkStart <= endBlock; chunkStart += queryChunkSize {
		chunkEnd := chunkStart + queryChunkSize - 1
		if chunkEnd > endBlock {
			chunkEnd = endBlock
		}

		query := fmt.Sprintf(
			`query { %s(filter: {%s: {_geq: %d, _leq: %d}}) { _docID } }`,
			collection, blockField, chunkStart, chunkEnd,
		)

		result := s.defraNode.DB.ExecRequest(ctx, query)
		if len(result.GQL.Errors) > 0 {
			return nil, fmt.Errorf("query %s [%d-%d]: %v", collection, chunkStart, chunkEnd, result.GQL.Errors[0])
		}

		data, ok := result.GQL.Data.(map[string]any)
		if !ok {
			continue
		}

		raw := data[collection]
		if raw == nil {
			continue
		}

		var docs []any
		switch typed := raw.(type) {
		case []any:
			docs = typed
		case []map[string]any:
			docs = make([]any, len(typed))
			for i, d := range typed {
				docs[i] = d
			}
		default:
			continue
		}

		for _, doc := range docs {
			m, ok := doc.(map[string]any)
			if !ok {
				continue
			}
			if docID, ok := m["_docID"].(string); ok {
				allDocIDs = append(allDocIDs, docID)
			}
		}
	}

	return allDocIDs, nil
}
