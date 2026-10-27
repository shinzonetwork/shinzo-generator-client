package evm

// Kava is a Cosmos SDK chain with an Ethermint EVM module: the block hash is
// the CometBFT header hash and only standard envelopes are produced, which
// rawStandardClientAdapter handles as-is. Kava logs carry correct block
// fields, so unlike ShinzoHub nothing is restamped.
func init() {
	chainAdapters["kava"] = rawStandardClientAdapter{}
}
