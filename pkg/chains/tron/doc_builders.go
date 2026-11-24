package tron

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
)

// Data-map builders: one function per collection, each producing the field
// values the BlockHandler writes as a document. Cross-document link fields
// (_blockID, _transactionID) are NOT set here; BlockHandler.Store resolves
// them through the LinkStamper protocol.

// buildBlockData builds the data map for a block document.
func buildBlockData(block *Block) map[string]any {
	return map[string]any{
		constants.HashFieldName:   block.BlockID,
		constants.NumberFieldName: block.BlockHeader.RawData.Number,
		TimestampFieldName:        block.BlockHeader.RawData.Timestamp,
		ParentHashFieldName:       block.BlockHeader.RawData.ParentHash,
		WitnessAddressFieldName:   block.BlockHeader.RawData.WitnessAddress,
		VersionFieldName:          block.BlockHeader.RawData.Version,
		TxTrieRootFieldName:       block.BlockHeader.RawData.TxTrieRoot,
		WitnessSignatureFieldName: block.BlockHeader.WitnessSignature,
		TransactionCountFieldName: int64(len(block.Transactions)),
	}
}

// buildTransactionData builds the data map for a transaction document.
// Fee and energy accounting come from the matching receipt; a transaction
// without a receipt keeps zero accounting. The node's raw_data bytes are
// stored verbatim as a JSON string because DefraDB has no nested object
// arrays and the raw payload must survive byte-for-byte; sender, recipient,
// and amount come from the UseNumber view decode instead of the bytes.
func buildTransactionData(block *Block, tx *Transaction, receipt *TransactionInfo) map[string]any {
	view, _ := decodeRawData(tx.RawData)
	contractType := ""
	if len(view.Contract) > 0 {
		contractType = view.Contract[0].Type
	}
	from, to, amount := extractTransfer(view.Contract)
	ret := ""
	if len(tx.Ret) > 0 {
		ret = tx.Ret[0].ContractRet
	}

	var fee, energyUsage int64
	if receipt != nil {
		fee = receipt.Fee
		energyUsage = receipt.Receipt.EnergyUsage
	}

	return map[string]any{
		constants.HashFieldName:        tx.TxID,
		constants.BlockNumberFieldName: block.BlockHeader.RawData.Number,
		constants.BlockHashFieldName:   block.BlockID,
		TypeFieldName:                  contractType,
		FromFieldName:                  from,
		ToFieldName:                    to,
		AmountFieldName:                amount,
		FeeFieldName:                   fee,
		RawDataFieldName:               string(tx.RawData),
		RetFieldName:                   ret,
		EnergyUsageFieldName:           energyUsage,
	}
}

// decodeRawData decodes the verbatim raw_data bytes into the contract view.
// It decodes with UseNumber so value-map numbers stay json.Number: java-tron
// serves TRC-10 amounts as int64, and a plain float64 decode rounds anything
// above 2^53. Empty bytes yield the zero view without error, and a decode
// failure degrades to the zero view because the verbatim bytes still reach
// the document.
func decodeRawData(raw json.RawMessage) (TransactionRawData, error) {
	if len(raw) == 0 {
		return TransactionRawData{}, nil
	}
	var view TransactionRawData
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&view); err != nil {
		return TransactionRawData{}, err
	}
	return view, nil
}

// extractTransfer picks the transfer trio out of a transaction's first
// contract. The native TransferContract serves owner_address, to_address,
// and amount; a smart-contract call serves owner_address,
// contract_address, and call_value, so the contract address fills in for
// the recipient and the call value fills in for the amount.
func extractTransfer(contracts []TransactionContract) (from, to string, amount int64) {
	if len(contracts) == 0 {
		return "", "", 0
	}
	value := contracts[0].Parameter.Value
	from = stringField(value, "owner_address")
	to = stringField(value, "to_address")
	if to == "" {
		to = stringField(value, "contract_address")
	}
	amount = intField(value, "amount")
	if amount == 0 {
		amount = intField(value, "call_value")
	}
	return from, to, amount
}

// stringField reads one string key from a polymorphic contract value map.
func stringField(value map[string]any, key string) string {
	if v, ok := value[key].(string); ok {
		return v
	}
	return ""
}

// intField reads one numeric key from a polymorphic contract value map.
// The UseNumber view decode serves numbers as json.Number, so int64 amounts
// keep their exact value no matter how large; the float64, int64, and int
// cases cover hand-built maps, and the Float64 fallback covers exponent
// spellings json.Number's Int64 cannot parse.
func intField(value map[string]any, key string) int64 {
	switch v := value[key].(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
		if f, err := v.Float64(); err == nil {
			return int64(f)
		}
		return 0
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	default:
		return 0
	}
}

// buildLogData builds the data map for a log (event) document. The wallet
// API serves only address, topics, and data inside the receipt; the parent
// receipt contributes the transaction hash, the block contributes hash and
// number, and the tx's position in the block contributes transactionIndex,
// while logIndex is the event's ordinal within that transaction.
func buildLogData(block *Block, receipt *TransactionInfo, txIndex, logIndex int, log *TxLog) map[string]any {
	return map[string]any{
		AddressFieldName:               log.Address,
		TopicsFieldName:                log.Topics,
		DataFieldName:                  log.Data,
		TransactionHashFieldName:       receipt.ID,
		constants.BlockHashFieldName:   block.BlockID,
		constants.BlockNumberFieldName: block.BlockHeader.RawData.Number,
		TransactionIndexFieldName:      txIndex,
		LogIndexFieldName:              logIndex,
	}
}

// buildInternalTransferData builds the data map for an internal transfer
// document. The parent receipt contributes the transaction hash; the
// ordinal within the parent receipt's internal_transactions list becomes
// transferIndex, so repeated identical movements stay distinct documents.
func buildInternalTransferData(block *Block, receipt *TransactionInfo, transferIndex int, itx *InternalTransaction) map[string]any {
	amount, assetName := firstCallValue(itx.CallValueInfo)
	return map[string]any{
		FromFieldName:                  itx.CallerAddress,
		ToFieldName:                    itx.TransferToAddress,
		AmountFieldName:                amount,
		AssetNameFieldName:             assetName,
		RejectedFieldName:              itx.Rejected,
		TransactionHashFieldName:       receipt.ID,
		constants.BlockNumberFieldName: block.BlockHeader.RawData.Number,
		TransferIndexFieldName:         transferIndex,
	}
}

// firstCallValue reads the first asset movement of an internal transaction:
// native sun via CallValue, or a TRC-10 asset id via AssetName, normalized
// to its string form whether the node served a string or a number.
func firstCallValue(infos []CallValueInfo) (int64, string) {
	if len(infos) == 0 {
		return 0, ""
	}
	return infos[0].CallValue, assetNameString(infos[0].AssetName)
}

// assetNameString normalizes the TRC-10 asset id: the node serves it as a
// JSON string or a JSON number depending on its protobuf-JSON conversion.
// A json.Number keeps its digits verbatim; the float64 case covers decodes
// without UseNumber.
func assetNameString(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatInt(int64(v), 10)
	default:
		return ""
	}
}

// buildTransactionDocs builds data maps for all transactions in the bundle,
// joined to their receipt by tx id. The bundle order is the block's
// transaction order, kept by the fetcher.
func buildTransactionDocs(bundle *BlockBundle, receiptMap map[string]*TransactionInfo) []map[string]any {
	var docs []map[string]any
	block := bundle.Block
	for i := range bundle.Transactions {
		tx := bundle.Transactions[i]
		if tx == nil {
			continue
		}
		docs = append(docs, buildTransactionData(block, tx, receiptMap[tx.TxID]))
	}
	return docs
}

// buildLogDocs builds data maps for every event log across the bundle's
// receipts. The tx's position in the block becomes transactionIndex and the
// per-transaction ordinal becomes logIndex.
func buildLogDocs(bundle *BlockBundle, receiptMap map[string]*TransactionInfo) []map[string]any {
	var docs []map[string]any
	block := bundle.Block
	for i := range bundle.Transactions {
		tx := bundle.Transactions[i]
		if tx == nil {
			continue
		}
		receipt, ok := receiptMap[tx.TxID]
		if !ok || receipt == nil {
			continue
		}
		for j := range receipt.Log {
			docs = append(docs, buildLogData(block, receipt, i, j, &receipt.Log[j]))
		}
	}
	return docs
}

// buildInternalTransferDocs builds data maps for every internal transfer
// across the bundle's receipts; the per-receipt ordinal becomes
// transferIndex.
func buildInternalTransferDocs(bundle *BlockBundle, receiptMap map[string]*TransactionInfo) []map[string]any {
	var docs []map[string]any
	block := bundle.Block
	for i := range bundle.Transactions {
		tx := bundle.Transactions[i]
		if tx == nil {
			continue
		}
		receipt, ok := receiptMap[tx.TxID]
		if !ok || receipt == nil {
			continue
		}
		for j := range receipt.InternalTransactions {
			docs = append(docs, buildInternalTransferData(block, receipt, j, &receipt.InternalTransactions[j]))
		}
	}
	return docs
}
