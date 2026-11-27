package chains

// Shared block-number and range query logic for chain converters. This file
// is the single home for the purge-residue and corruption handling the
// converters' progress queries need: every chain family inherits the same
// empty-collection, numberless-row, and parse-failure behavior instead of
// maintaining copies that drift apart. Callers name the collection and the
// field that carries block numbers, so nothing chain-specific is baked in.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/node"
)

// docIDField is DefraDB's implicit document-ID field: it appears both as a
// GraphQL query field and as the result-map key identifying each row.
const docIDField = "_docID"

// chunkSize bounds each QueryCollectionDocIDs query window.
const chunkSize = 100

// HighestBlockQueryLimit keeps the tip query at a single row: corruption
// at the highest block indicates a writer bug, not purge residue.
const HighestBlockQueryLimit = 1

// QueryBlockNumber runs the block-number query with the given ordering
// ("DESC" or "ASC") and row limit against blockCol, returning the first row
// whose field is parseable. The `_geq: 0` filter excludes rows whose number
// is missing or null (purge residue): block numbers are non-negative, so
// every real block including genesis is admitted, while numberless rows,
// which sort ahead of real numbers under ASC, can never fill the query
// window. Rows that still arrive unparseable are skipped and reported; all
// errors are tagged with opName.
func QueryBlockNumber(ctx context.Context, n *node.Node, blockCol, field, order, opName string, queryLimit int) (int64, error) {
	query := `query {` + blockCol + ` (filter: {` + field + `: {_geq: 0}}, order: {` + field + `: ` + order + `}, limit: ` + strconv.Itoa(queryLimit) + `) { ` + field + ` ` + docIDField + `}}`

	result := n.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0, errors.NewQueryFailed("defra", opName, query, result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return 0, errors.NewDocumentNotFound("defra", opName, blockCol, "no data")
	}

	var rows []any
	switch arr := data[blockCol].(type) {
	case []any:
		rows = arr
	case []map[string]any:
		rows = make([]any, len(arr))
		for i, m := range arr {
			rows[i] = m
		}
	default:
		return 0, errors.NewDocumentNotFound("defra", opName, blockCol, "no blocks")
	}

	if len(rows) == 0 {
		// The number filter hides rows without a number, so an empty result
		// does not by itself mean the collection is empty. Distinguish the two
		// cases: no documents at all is benign ("not found"), while documents
		// without any usable number are corruption and must hard-fail so the
		// pruner maps them to ErrNoValidBlocks instead of skipping pruning.
		present, err := hasAnyBlockDocs(ctx, n, blockCol, opName)
		if err != nil {
			return 0, err
		}
		if present {
			return 0, fmt.Errorf("%s: %w (no rows have a usable number field)", opName, ErrBlockNumberCorrupt)
		}
		return 0, errors.NewDocumentNotFound("defra", opName, blockCol, "no blocks")
	}

	return firstUsableRow(rows, field, opName)
}

// firstUsableRow returns the block number of the first parseable row,
// skipping and summarizing unparseable ones. It reports
// ErrBlockNumberCorrupt when no row in the window is usable.
func firstUsableRow(rows []any, field, opName string) (int64, error) {
	var skipped []string
	for _, r := range rows {
		block, ok := r.(map[string]any)
		if !ok {
			skipped = append(skipped, fmt.Sprintf("row=%#v", r))
			continue
		}
		num, ok := parseBlockNumberRow(block, field)
		if ok {
			if len(skipped) > 0 {
				logger.Sugar.Warnf("%s: skipped %d corrupt block row(s): %s",
					opName, len(skipped), strings.Join(skipped, "; "))
			}
			return num, nil
		}
		docID, _ := block[docIDField].(string)
		raw := block[field]
		skipped = append(skipped, fmt.Sprintf("docID=%s number=%T(%v)", docID, raw, raw))
	}

	return 0, fmt.Errorf("%s: %w (all %d rows corrupt: %s)",
		opName, ErrBlockNumberCorrupt, len(rows), strings.Join(skipped, "; "))
}

// hasAnyBlockDocs reports whether the collection holds any document,
// including rows whose number field is missing.
func hasAnyBlockDocs(ctx context.Context, n *node.Node, blockCol, opName string) (bool, error) {
	query := `query {` + blockCol + ` (limit: 1) { ` + docIDField + ` }}`

	result := n.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return false, errors.NewQueryFailed("defra", opName, query, result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return false, nil
	}
	switch rows := data[blockCol].(type) {
	case []any:
		return len(rows) > 0, nil
	case []map[string]any:
		return len(rows) > 0, nil
	}
	return false, nil
}

// parseBlockNumberRow extracts the block number from a block query row. It
// reports false when the number field is missing or has an unparseable type.
func parseBlockNumberRow(block map[string]any, field string) (int64, bool) {
	switch v := block[field].(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

// QueryCollectionDocIDs queries a single collection for all document IDs
// whose block-number field falls in [from, to] inclusive. It uses chunked
// _geq/_leq GraphQL range filters.
func QueryCollectionDocIDs(ctx context.Context, n *node.Node, colName, field string, from, to int64) ([]string, error) {
	var allDocIDs []string

	for chunkStart := from; chunkStart <= to; chunkStart += chunkSize {
		chunkEnd := chunkStart + chunkSize - 1
		chunkEnd = min(chunkEnd, to)

		query := fmt.Sprintf(
			`query { %s(filter: {%s: {_geq: %d, _leq: %d}}) { %s } }`,
			colName, field, chunkStart, chunkEnd, docIDField,
		)

		result := n.DB.ExecRequest(ctx, query)
		if len(result.GQL.Errors) > 0 {
			return nil, fmt.Errorf("query %s [%d-%d]: %w", colName, chunkStart, chunkEnd, result.GQL.Errors[0])
		}

		data, ok := result.GQL.Data.(map[string]any)
		if !ok {
			continue
		}

		raw := data[colName]
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
			if docID, ok := m[docIDField].(string); ok {
				allDocIDs = append(allDocIDs, docID)
			}
		}
	}

	return allDocIDs, nil
}
