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
// Version 2 snapshots contain per-collection sections with embedded field mappings;
// each section is imported via ImportRawKVsWithMapping so short IDs are remapped
// to match the destination node's schema even when they differ from the source.
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

	// Read header (length-prefixed JSON).
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

	logger.Sugar.Infof("Importing KV snapshot: blocks %d-%d (version=%d, created=%s, collections=%d)",
		header.StartBlock, header.EndBlock, header.Version, header.CreatedAt, len(header.FieldMappings))

	total, err := importSections(ctx, defraNode, gr, header.FieldMappings)
	if err != nil {
		return nil, err
	}

	logger.Sugar.Infof("KV import complete: %d KV pairs for blocks %d-%d",
		total, header.StartBlock, header.EndBlock)

	return &ImportResult{
		StartBlock: header.StartBlock,
		EndBlock:   header.EndBlock,
	}, nil
}

// importSections reads per-collection sections from gr and imports each with its field mapping.
// Each section is: [4-byte name_len][name bytes][KV pairs + uint32(0) sentinel].
// A name_len of 0 signals the end of all sections.
func importSections(ctx context.Context, defraNode *node.Node, gr *gzip.Reader, fieldMappings map[string]json.RawMessage) (int, error) {
	total := 0
	var lenBuf [4]byte

	for {
		if _, err := io.ReadFull(gr, lenBuf[:]); err != nil {
			return total, fmt.Errorf("read section name length: %w", err) //nolint: err113
		}
		nameLen := binary.BigEndian.Uint32(lenBuf[:])
		if nameLen == 0 {
			break // section terminator
		}

		nameBytes := make([]byte, nameLen)
		if _, err := io.ReadFull(gr, nameBytes); err != nil {
			return total, fmt.Errorf("read section name: %w", err) //nolint: err113
		}
		collectionName := string(nameBytes)

		var count int
		var err error
		if mappingJSON, ok := fieldMappings[collectionName]; ok && len(mappingJSON) > 0 {
			// Remap collection and field short IDs from source node to this node.
			count, err = defraNode.DB.ImportRawKVsWithMapping(ctx, gr, []byte(mappingJSON))
		} else {
			// No mapping available — import as-is (same-node restore or schema not found).
			count, err = defraNode.DB.ImportRawKVs(ctx, gr)
		}
		if err != nil {
			return total, fmt.Errorf("import KVs for %s: %w", collectionName, err) //nolint: err113
		}
		total += count
		logger.Sugar.Debugf("Imported %d KV pairs for %s", count, collectionName)
	}

	return total, nil
}
