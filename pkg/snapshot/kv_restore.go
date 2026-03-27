package snapshot

import (
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/sourcenetwork/defradb/node"
)

// ImportResult holds the block range of an import operation.
type ImportResult struct {
	StartBlock int64 `json:"start_block"`
	EndBlock   int64 `json:"end_block"`
}

// ImportKV reads a .kvsnap.gz file and writes KV pairs directly to the rootstore.
func ImportKV(ctx context.Context, defraNode *node.Node, filePath string) (*ImportResult, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()

	// Read header (length-prefixed JSON)
	var lenBuf [4]byte
	if _, err := io.ReadFull(gr, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read header length: %w", err)
	}
	headerLen := binary.BigEndian.Uint32(lenBuf[:])

	headerBytes := make([]byte, headerLen)
	if _, err := io.ReadFull(gr, headerBytes); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	var header kvSnapshotHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}

	if header.Magic != "DFKV" {
		return nil, fmt.Errorf("invalid snapshot magic: %q (expected DFKV)", header.Magic)
	}

	logger.Sugar.Infof("Importing KV snapshot: blocks %d-%d (version=%d, created=%s)",
		header.StartBlock, header.EndBlock, header.Version, header.CreatedAt)

	count, err := defraNode.DB.ImportRawKVs(ctx, gr)
	if err != nil {
		return nil, fmt.Errorf("import raw KVs: %w", err)
	}

	logger.Sugar.Infof("KV import complete: %d KV pairs imported for blocks %d-%d",
		count, header.StartBlock, header.EndBlock)

	return &ImportResult{
		StartBlock: header.StartBlock,
		EndBlock:   header.EndBlock,
	}, nil
}
