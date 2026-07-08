package snapshot

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// kvSnapshotHeader is written at the start of a .kvsnap.gz file.
type kvSnapshotHeader struct {
	Magic               string   `json:"magic"`
	Version             int      `json:"version"`
	StartBlock          int64    `json:"start_block"`
	EndBlock            int64    `json:"end_block"`
	CreatedAt           string   `json:"created_at"`
	BlockSigMerkleRoots []string `json:"block_sig_merkle_roots,omitempty"`
}

// createKVSnapshot exports raw Badger KV pairs for a block range to a binary file.
func (s *Snapshotter) createKVSnapshot(ctx context.Context, startBlock, endBlock int64) error {
	filename := fmt.Sprintf("snapshot_%d_%d.kvsnap.gz", startBlock, endBlock)
	filePath := filepath.Join(s.cfg.Dir, filename)

	roots, err := s.writeKVSnapshotFile(ctx, filePath, startBlock, endBlock)
	if err != nil {
		return err
	}

	if err := signSnapshotWithRoots(ctx, s.defraNode, filename, startBlock, endBlock, roots, len(roots)); err != nil {
		return fmt.Errorf("snapshot signing failed for %s: %w", filename, err)
	}

	return nil
}

// writeKVSnapshotFile writes the snapshot to a temp file and atomically renames it.
func (s *Snapshotter) writeKVSnapshotFile(ctx context.Context, filePath string, startBlock, endBlock int64) ([][]byte, error) {
	tmpPath := filePath + ".tmp"

	f, err := os.Create(filepath.Clean(tmpPath))
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}

	gw := gzip.NewWriter(f)
	committed := false
	defer func() {
		if !committed {
			_ = gw.Close()
			_ = f.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	roots, err := s.writeKVSnapshotContents(ctx, gw, startBlock, endBlock)
	if err != nil {
		return nil, err
	}

	if err := gw.Close(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(filepath.Clean(tmpPath))
		return nil, err
	}

	committed = true
	if err := os.Rename(filepath.Clean(tmpPath), filepath.Clean(filePath)); err != nil {
		_ = os.Remove(filepath.Clean(tmpPath))
		return nil, err
	}

	return roots, nil
}

// writeKVSnapshotContents writes the header, KV pairs, and EOF marker to the gzip writer.
func (s *Snapshotter) writeKVSnapshotContents(ctx context.Context, gw *gzip.Writer, startBlock, endBlock int64) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	roots, _, err := getBlockSigMerkleRoots(ctx, s.defraNode, startBlock, endBlock)
	if err != nil {
		logger.Sugar.Warnf("KV snapshot: failed to get block sig roots: %v", err)
	}

	var rootsHex []string
	for _, r := range roots {
		rootsHex = append(rootsHex, hex.EncodeToString(r))
	}

	header := kvSnapshotHeader{
		Magic:               constants.HeaderMagicValue,
		Version:             1,
		StartBlock:          startBlock,
		EndBlock:            endBlock,
		CreatedAt:           time.Now().UTC().Format(time.RFC3339),
		BlockSigMerkleRoots: rootsHex,
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("marshal header: %w", err)
	}

	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(headerBytes))) //nolint:gosec
	if _, err := gw.Write(lenBuf[:]); err != nil {
		return nil, err
	}
	if _, err := gw.Write(headerBytes); err != nil {
		return nil, err
	}

	totalKVs, err := s.exportCollectionKVs(ctx, gw, startBlock, endBlock)
	if err != nil {
		return nil, err
	}

	binary.BigEndian.PutUint32(lenBuf[:], 0)
	if _, err := gw.Write(lenBuf[:]); err != nil {
		return nil, err
	}

	logger.Sugar.Infof("KV snapshot written (%d KV pairs)", totalKVs)

	return roots, nil
}

// exportCollectionKVs exports KV pairs for all collections in the block range.
func (s *Snapshotter) exportCollectionKVs(ctx context.Context, gw *gzip.Writer, startBlock, endBlock int64) (int, error) {
	collections := []struct {
		name       string
		blockField string
	}{
		{constants.CollectionBlock, "number"},                //nolint:goconst
		{constants.CollectionTransaction, "blockNumber"},     //nolint:goconst
		{constants.CollectionLog, "blockNumber"},             //nolint:goconst
		{constants.CollectionAccessListEntry, "blockNumber"}, //nolint:goconst
		{constants.CollectionBlockSignature, "blockNumber"},  //nolint:goconst
	}

	totalKVs := 0
	for _, col := range collections {
		docIDs, err := s.queryDocIDs(ctx, col.name, col.blockField, startBlock, endBlock)
		if err != nil {
			return 0, fmt.Errorf("query docIDs for %s: %w", col.name, err)
		}
		if len(docIDs) == 0 {
			continue
		}

		// ExportDocKVs writes its own EOF sentinel (uint32(0)) after the KV pairs.
		// Buffer each call so we can strip that sentinel before writing to the shared
		// gzip stream; our caller (writeKVSnapshotContents) writes the single final sentinel.
		var buf bytes.Buffer
		n, err := s.defraNode.DB.ExportDocKVs(ctx, col.name, docIDs, &buf, true)
		if err != nil {
			return 0, fmt.Errorf("export KVs for %s: %w", col.name, err)
		}
		data := buf.Bytes()
		const sentinelLen = 4
		if len(data) >= sentinelLen {
			data = data[:len(data)-sentinelLen]
		}
		if _, err := gw.Write(data); err != nil {
			return 0, fmt.Errorf("write KVs for %s: %w", col.name, err)
		}
		totalKVs += n
		logger.Sugar.Debugf("Exported %d KV pairs for %s (%d docs)", n, col.name, len(docIDs))
	}

	return totalKVs, nil
}

// queryDocIDs returns all document IDs for a collection in the given block range.
func (s *Snapshotter) queryDocIDs(ctx context.Context, collection, blockField string, startBlock, endBlock int64) ([]string, error) {
	var allDocIDs []string

	for chunkStart := startBlock; chunkStart <= endBlock; chunkStart += queryChunkSize {
		chunkEnd := chunkStart + queryChunkSize - 1
		chunkEnd = min(chunkEnd, endBlock)

		query := fmt.Sprintf(
			`query { %s(filter: {%s: {_geq: %d, _leq: %d}}) { _docID } }`,
			collection, blockField, chunkStart, chunkEnd,
		)

		result := s.defraNode.DB.ExecRequest(ctx, query)
		if len(result.GQL.Errors) > 0 {
			return nil, fmt.Errorf("query %s [%d-%d]: %w", collection, chunkStart, chunkEnd, result.GQL.Errors[0])
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
