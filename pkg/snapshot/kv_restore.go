package snapshot

import (
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/node"
)

// ImportResult holds the block range of an import operation.
type ImportResult struct {
	StartBlock int64 `json:"start_block"`
	EndBlock   int64 `json:"end_block"`
}

// ImportKV reads a .kvsnap.gz file and writes KV pairs directly to the rootstore.
func ImportKV(ctx context.Context, defraNode *node.Node, filePath string) (*ImportResult, error) {
	f, err := os.Open(filepath.Clean(filePath))
	if err != nil {
		return nil, fmt.Errorf("open snapshot: %w", err) //nolint: err113
	}
	defer func() { _ = f.Close() }()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err) //nolint: err113
	}
	defer func() { _ = gr.Close() }()

	// Read header (length-prefixed JSON)
	var lenBuf [4]byte
	if _, readErr := io.ReadFull(gr, lenBuf[:]); readErr != nil {
		return nil, fmt.Errorf("read header length: %w", readErr) //nolint: err113
	}
	headerLen := binary.BigEndian.Uint32(lenBuf[:])

	headerBytes := make([]byte, headerLen)
	if _, readErr := io.ReadFull(gr, headerBytes); readErr != nil {
		return nil, fmt.Errorf("read header: %w", readErr) //nolint: err113
	}

	var header kvSnapshotHeader
	if unmarshalErr := json.Unmarshal(headerBytes, &header); unmarshalErr != nil {
		return nil, fmt.Errorf("parse header: %w", unmarshalErr) //nolint: err113
	}

	if header.Magic != constants.HeaderMagicValue {
		return nil, fmt.Errorf("invalid snapshot magic: %q (expected %q)", header.Magic, constants.HeaderMagicValue) //nolint: err113
	}

	logger.Sugar.Infof("Importing KV snapshot: blocks %d-%d (version=%d, created=%s)",
		header.StartBlock, header.EndBlock, header.Version, header.CreatedAt)

	count, err := defraNode.DB.ImportRawKVs(ctx, gr)
	if err != nil {
		return nil, fmt.Errorf("import raw KVs: %w", err) //nolint: err113
	}

	// Raw KV import writes document data directly and bypasses index maintenance, leaving the
	// destination's secondary indexes empty. Rebuild them for each imported collection.
	logger.Sugar.Infof("Rebuilding secondary indexes for %d collections", len(snapshotCollections))
	for _, col := range snapshotCollections {
		if rebuildErr := defraNode.DB.RebuildCollectionIndexes(ctx, col.name); rebuildErr != nil {
			return nil, fmt.Errorf("rebuild indexes for %s: %w", col.name, rebuildErr) //nolint: err113
		}
	}

	logger.Sugar.Infof("KV import complete: %d KV pairs imported for blocks %d-%d",
		count, header.StartBlock, header.EndBlock)

	return &ImportResult{
		StartBlock: header.StartBlock,
		EndBlock:   header.EndBlock,
	}, nil
}
