package solana

import (
	"encoding/base64"
	"fmt"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/chains"
)

// DocumentBuilder implements chains.ChainDocumentBuilder for Solana
type DocumentBuilder struct {
	network     chains.NetworkType
	collections chains.CollectionSet
}

// NewDocumentBuilder creates a new Solana document builder
func NewDocumentBuilder(network chains.NetworkType) *DocumentBuilder {
	return &DocumentBuilder{
		network:     network,
		collections: GetCollectionNames(network),
	}
}

// BuildBlockDocument implements chains.ChainDocumentBuilder - builds a Slot document
func (b *DocumentBuilder) BuildBlockDocument(block chains.ChainBlock) (map[string]interface{}, error) {
	solBlock, ok := block.(*SolanaBlock)
	if !ok {
		return nil, fmt.Errorf("expected *SolanaBlock, got %T", block)
	}

	internalBlock := solBlock.GetInternalBlock()
	if internalBlock == nil {
		return nil, fmt.Errorf("internal block is nil")
	}

	doc := map[string]interface{}{
		"slot":              int64(solBlock.GetSlot()),
		"blockhash":         internalBlock.Blockhash.String(),
		"parentSlot":        int64(internalBlock.ParentSlot),
		"previousBlockhash": internalBlock.PreviousBlockhash.String(),
		"transactionCount":  len(internalBlock.Transactions),
	}

	// Add optional fields
	if internalBlock.BlockTime != nil {
		doc["blockTime"] = int64(*internalBlock.BlockTime)
	}

	if internalBlock.BlockHeight != nil {
		doc["blockHeight"] = int64(*internalBlock.BlockHeight)
	}

	return doc, nil
}

// BuildTransactionDocument implements chains.ChainDocumentBuilder
func (b *DocumentBuilder) BuildTransactionDocument(tx chains.ChainTransaction, slotDocID string) (map[string]interface{}, error) {
	solTx, ok := tx.(*SolanaTransaction)
	if !ok {
		return nil, fmt.Errorf("expected *SolanaTransaction, got %T", tx)
	}

	internalTx := solTx.GetInternalTransaction()
	if internalTx == nil {
		return nil, fmt.Errorf("internal transaction is nil")
	}

	signature := solTx.Hash()
	if signature == "" {
		return nil, fmt.Errorf("transaction has no signature")
	}

	doc := map[string]interface{}{
		"signature":        signature,
		"slot":             int64(solTx.BlockNumber()),
		"transactionIndex": solTx.Index(),
		"fee":              int64(solTx.GetFee()),
		"successful":       solTx.IsSuccessful(),
		"_slot_refID":      slotDocID,
	}

	// Add error if present
	if err := solTx.GetError(); err != nil {
		doc["err"] = fmt.Sprintf("%v", err)
	}

	// Add balances
	if preBalances := solTx.GetPreBalances(); preBalances != nil {
		intBalances := make([]int64, len(preBalances))
		for i, bal := range preBalances {
			intBalances[i] = int64(bal)
		}
		doc["preBalances"] = intBalances
	}

	if postBalances := solTx.GetPostBalances(); postBalances != nil {
		intBalances := make([]int64, len(postBalances))
		for i, bal := range postBalances {
			intBalances[i] = int64(bal)
		}
		doc["postBalances"] = intBalances
	}

	// Add log messages if available
	if internalTx.Meta != nil && internalTx.Meta.LogMessages != nil {
		doc["logMessages"] = internalTx.Meta.LogMessages
	}

	return doc, nil
}

// BuildLogDocument implements chains.ChainDocumentBuilder
// For Solana, we don't have traditional logs like Ethereum - log messages are stored in transactions
func (b *DocumentBuilder) BuildLogDocument(log chains.ChainLog, slotDocID, txDocID string) (map[string]interface{}, error) {
	// Solana logs are stored as log messages in the transaction document
	// This method is kept for interface compatibility
	return nil, fmt.Errorf("Solana logs are stored in transaction documents as logMessages")
}

// BuildReceiptDocuments implements chains.ChainDocumentBuilder
// For Solana, transaction metadata is included with the transaction
func (b *DocumentBuilder) BuildReceiptDocuments(receipt chains.ChainReceipt, slotDocID, txDocID string) ([]map[string]interface{}, error) {
	// Solana receipts are part of the transaction metadata
	// No separate documents needed
	return nil, nil
}

// BuildInstructionDocument builds a document for a Solana instruction
func (b *DocumentBuilder) BuildInstructionDocument(ix *SolanaInstruction, slot uint64, txSignature, txDocID string) (map[string]interface{}, error) {
	if ix == nil {
		return nil, fmt.Errorf("instruction is nil")
	}

	return map[string]interface{}{
		"instructionIndex":     ix.Index(),
		"slot":                 int64(slot),
		"transactionSignature": txSignature,
		"programId":            ix.ProgramID(),
		"accounts":             ix.Accounts(),
		"data":                 base64.StdEncoding.EncodeToString([]byte(ix.Data())),
		"stackHeight":          0, // Top-level instruction
		"_transactionID":       txDocID,
	}, nil
}

// GetCollectionNames implements chains.ChainDocumentBuilder
func (b *DocumentBuilder) GetCollectionNames() chains.CollectionSet {
	return b.collections
}

// ChainName implements chains.ChainDocumentBuilder
func (b *DocumentBuilder) ChainName() string {
	return string(chains.ChainTypeSolana)
}

// NetworkName implements chains.ChainDocumentBuilder
func (b *DocumentBuilder) NetworkName() string {
	return string(b.network)
}

// BuildFullBlockDocuments builds all documents for a Solana block including instructions
func (b *DocumentBuilder) BuildFullBlockDocuments(block *SolanaBlock, slotDocID string) ([]map[string]interface{}, map[string]string, error) {
	if block == nil || block.block == nil {
		return nil, nil, fmt.Errorf("block is nil")
	}

	var txDocs []map[string]interface{}
	txSigToDocID := make(map[string]string)

	for i := range block.block.Transactions {
		tx := NewSolanaTransaction(block.GetSlot(), i, &block.block.Transactions[i])

		txDoc, err := b.BuildTransactionDocument(tx, slotDocID)
		if err != nil {
			continue
		}

		sig := tx.Hash()
		if sig != "" {
			txDocs = append(txDocs, txDoc)
			// Note: actual doc ID would be set after creation in DefraDB
			txSigToDocID[sig] = fmt.Sprintf("placeholder_%d", i)
		}
	}

	return txDocs, txSigToDocID, nil
}

// init registers the Solana document builder factory
func init() {
	chains.RegisterDocumentBuilderFactory(chains.ChainTypeSolana, func(chain chains.ChainType, network chains.NetworkType) chains.ChainDocumentBuilder {
		return NewDocumentBuilder(network)
	})
}
