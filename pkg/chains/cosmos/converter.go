package cosmos

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/sourcenetwork/defradb/node"
)

// Cosmos document field names not shared with constants.
const (
	transactionHashFieldName  = "transactionHash"
	transactionIndexFieldName = "transactionIndex"

	// Event phases: emitted by a transaction, or by FinalizeBlock itself.
	eventPhaseTx       = "tx"
	eventPhaseFinalize = "finalize_block"

	// defaultMaxDocsPerTxn mirrors the EVM converter's fallback.
	defaultMaxDocsPerTxn = 1000
)

// Converter implements chains.Converter for cosmos-evm chains. The EVM
// converter builds the Block and EVM groups; this converter appends the
// CosmosTransaction, CosmosMessage and CosmosEvent groups for the same block
// and links them to the shared Block doc.
type Converter struct {
	evm  *evm.Converter
	cols *CollectionNames
	cfg  *config.Config
}

// Compile-time guarantee that Converter implements chains.Converter.
var _ chains.Converter = (*Converter)(nil)

// NewConverter creates a Converter whose collection prefix is derived from
// the chain config exactly as the EVM converter derives it.
func NewConverter(cfg *config.Config) *Converter {
	evmConverter := evm.NewConverter(cfg)
	return &Converter{
		evm:  evmConverter,
		cols: NewCollectionNames(evmConverter.Collections().Prefix()),
		cfg:  cfg,
	}
}

// Convert implements chains.Converter. rawBlock must be a *Bundle whose EVM
// and Cosmos views describe the same block (same height and hash).
func (c *Converter) Convert(ctx context.Context, rawBlock any) (chains.ConversionResult, error) {
	bundle, ok := rawBlock.(*Bundle)
	if !ok {
		return chains.ConversionResult{}, fmt.Errorf("cosmos converter: expected *Bundle, got %T", rawBlock)
	}
	if bundle == nil || bundle.EVM == nil || bundle.EVM.Block == nil || bundle.Cosmos == nil {
		return chains.ConversionResult{}, fmt.Errorf("cosmos converter: incomplete bundle")
	}
	if err := checkSameBlock(bundle); err != nil {
		return chains.ConversionResult{}, err
	}

	result, err := c.evm.Convert(ctx, bundle.EVM)
	if err != nil {
		return chains.ConversionResult{}, err
	}

	block := bundle.Cosmos
	txDocs, msgDocs, eventDocs := c.buildDocs(block)
	batch := c.maxDocsPerTxn()
	for _, g := range []struct {
		collection string
		docs       []map[string]any
	}{
		{c.cols.CosmosTransaction, txDocs},
		{c.cols.CosmosMessage, msgDocs},
		{c.cols.CosmosEvent, eventDocs},
	} {
		if len(g.docs) == 0 {
			continue
		}
		result.Groups = append(result.Groups, chains.DocumentGroup{
			Collection:    g.collection,
			Docs:          g.docs,
			BatchSize:     batch,
			BlockNumField: constants.BlockNumberFieldName,
		})
	}
	result.LinkStamper = newLinkStamper(result.LinkStamper, c.cols)
	return result, nil
}

// checkSameBlock guards against joining two different blocks, e.g. when the
// EVM and Cosmos endpoints point at nodes on different forks.
func checkSameBlock(bundle *Bundle) error {
	evmHeight, err := strconv.ParseInt(bundle.EVM.Block.Number, 10, 64)
	if err != nil {
		return fmt.Errorf("cosmos converter: parse EVM block number %q: %w", bundle.EVM.Block.Number, err)
	}
	if evmHeight != bundle.Cosmos.Height {
		return fmt.Errorf("cosmos converter: EVM block %d joined with Cosmos block %d", evmHeight, bundle.Cosmos.Height) //nolint:err113
	}
	if !strings.EqualFold(bundle.EVM.Block.Hash, bundle.Cosmos.Hash) {
		return fmt.Errorf("cosmos converter: block %d hash mismatch: EVM %s, Cosmos %s", //nolint:err113
			evmHeight, bundle.EVM.Block.Hash, bundle.Cosmos.Hash)
	}
	return nil
}

// buildDocs builds the data maps for the block's Cosmos transactions,
// messages and events. Link fields are stamped later by the linkStamper.
func (c *Converter) buildDocs(block *Block) (txDocs, msgDocs, eventDocs []map[string]any) {
	for i := range block.Transactions {
		tx := &block.Transactions[i]
		txDocs = append(txDocs, map[string]any{
			constants.HashFieldName:        tx.Hash,
			constants.BlockHashFieldName:   block.Hash,
			constants.BlockNumberFieldName: block.Height,
			transactionIndexFieldName:      tx.Index,
			"code":                         int64(tx.Code),
			"codespace":                    tx.Codespace,
			"log":                          tx.Log,
			"gasWanted":                    tx.GasWanted,
			"gasUsed":                      tx.GasUsed,
			"memo":                         tx.Memo,
			"fee":                          tx.Fee,
			"messageCount":                 len(tx.Messages),
		})
		for _, msg := range tx.Messages {
			msgDocs = append(msgDocs, map[string]any{
				constants.BlockNumberFieldName: block.Height,
				transactionHashFieldName:       tx.Hash,
				transactionIndexFieldName:      tx.Index,
				"messageIndex":                 msg.Index,
				"typeUrl":                      msg.TypeURL,
				"body":                         msg.Body,
			})
		}
		for _, ev := range tx.Events {
			doc := eventDoc(block.Height, eventPhaseTx, ev)
			doc[transactionHashFieldName] = tx.Hash
			doc[transactionIndexFieldName] = tx.Index
			eventDocs = append(eventDocs, doc)
		}
	}
	for _, ev := range block.FinalizeEvents {
		eventDocs = append(eventDocs, eventDoc(block.Height, eventPhaseFinalize, ev))
	}
	return txDocs, msgDocs, eventDocs
}

func eventDoc(height int64, phase string, ev Event) map[string]any {
	return map[string]any{
		constants.BlockNumberFieldName: height,
		"phase":                        phase,
		"eventIndex":                   ev.Index,
		"type":                         ev.Type,
		"attributes":                   ev.Attributes,
	}
}

func (c *Converter) maxDocsPerTxn() int {
	if c.cfg != nil && c.cfg.Indexer.MaxDocsPerTxn > 0 {
		return c.cfg.Indexer.MaxDocsPerTxn
	}
	return defaultMaxDocsPerTxn
}

// GetSchema implements chains.Converter.
func (c *Converter) GetSchema() (string, error) {
	files, err := c.cols.CollectionFiles()
	if err != nil {
		return "", err
	}
	return chains.MergeSDL(files), nil
}

// GetCollections implements chains.Converter.
func (c *Converter) GetCollections() []string {
	return c.cols.AllCollections()
}

// Collections implements chains.Converter.
func (c *Converter) Collections() chains.Collections {
	return c.cols
}

// GetHighestStoredBlockNumber implements chains.Converter. The Block
// collection is shared with the EVM converter.
func (c *Converter) GetHighestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.evm.GetHighestStoredBlockNumber(ctx, n)
}

// GetLowestStoredBlockNumber implements chains.Converter.
func (c *Converter) GetLowestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.evm.GetLowestStoredBlockNumber(ctx, n)
}

// GetDocIDsByBlockRange implements chains.Converter: the EVM collections'
// docIDs plus the Cosmos collections' docIDs in [from, to].
func (c *Converter) GetDocIDsByBlockRange(ctx context.Context, n *node.Node, from, to int64) (map[string][]string, error) {
	result, err := c.evm.GetDocIDsByBlockRange(ctx, n, from, to)
	if err != nil {
		return nil, err
	}
	for _, col := range c.cols.cosmosCollections() {
		docIDs, err := queryDocIDsByBlockRange(ctx, n, col, from, to)
		if err != nil {
			return nil, fmt.Errorf("query docIDs for %s: %w", col, err)
		}
		if len(docIDs) > 0 {
			result[col] = docIDs
		}
	}
	return result, nil
}

// queryDocIDsByBlockRange returns the docIDs of colName whose blockNumber is
// in [from, to], querying in chunks of heights to keep result sets bounded.
func queryDocIDsByBlockRange(ctx context.Context, n *node.Node, colName string, from, to int64) ([]string, error) {
	const chunkSize = 100
	var docIDs []string
	for chunkStart := from; chunkStart <= to; chunkStart += chunkSize {
		chunkEnd := min(chunkStart+chunkSize-1, to)
		query := fmt.Sprintf(`query { %s(filter: {%s: {_geq: %d, _leq: %d}}) { _docID } }`,
			colName, constants.BlockNumberFieldName, chunkStart, chunkEnd)
		result := n.DB.ExecRequest(ctx, query)
		if len(result.GQL.Errors) > 0 {
			return nil, fmt.Errorf("query %s [%d-%d]: %w", colName, chunkStart, chunkEnd, result.GQL.Errors[0])
		}
		data, ok := result.GQL.Data.(map[string]any)
		if !ok {
			continue
		}
		var rows []map[string]any
		switch typed := data[colName].(type) {
		case []map[string]any:
			rows = typed
		case []any:
			for _, r := range typed {
				if m, ok := r.(map[string]any); ok {
					rows = append(rows, m)
				}
			}
		}
		for _, row := range rows {
			if id, ok := row["_docID"].(string); ok && id != "" {
				docIDs = append(docIDs, id)
			}
		}
	}
	return docIDs, nil
}
