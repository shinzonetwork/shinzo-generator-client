package evm

// shinzohubClientAdapter covers the cosmos-evm JSON-RPC differences of the
// ShinzoHub chain:
//
//   - The block hash is the CometBFT header hash, which go-ethereum cannot
//     recompute from the EVM header, and only standard envelopes (legacy,
//     0x1, 0x2) are produced; both are handled by rawStandardClientAdapter.
//   - Logs emitted by the hub's precompiles carry blockNumber 0x0 (they are
//     built by hand in stateDB.AddLog), so every log is restamped from its
//     receipt, whose block number and hash are correct.
//
// Sender recovery and yParity keep the default behaviour.
type shinzohubClientAdapter struct {
	rawStandardClientAdapter
}

func init() {
	chainAdapters["shinzohub"] = shinzohubClientAdapter{}
}

// StampReceiptLogs sets each log's block number and hash from its receipt.
func (shinzohubClientAdapter) StampReceiptLogs(receipt *TransactionReceipt) {
	for i := range receipt.Logs {
		receipt.Logs[i].BlockNumber = receipt.BlockNumber
		receipt.Logs[i].BlockHash = receipt.BlockHash
	}
}
