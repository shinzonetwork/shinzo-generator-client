package pruner

import (
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

// Config represents pruner configuration for removing old documents.
type Config struct {
	Enabled         bool  `yaml:"enabled"`
	MaxBlocks       int64 `yaml:"max_blocks"`      // Number of blocks to retain
	DocsPerBlock    int   `yaml:"docs_per_block"`  // Average docs per block (~1057 on Ethereum mainnet)
	PruneThreshold  int64 `yaml:"prune_threshold"` // Deprecated: kept for backward compatibility, unused by pruner
	IntervalSeconds int   `yaml:"interval_seconds"`
	PruneHistory    bool  `yaml:"prune_history"`
}

// CollectionConfig defines which collections to prune and how.
type CollectionConfig struct {
	// BlockCollection is the name of the block collection (e.g. "Ethereum__Mainnet__Block").
	BlockCollection string
	// BlockNumberField is the field name for block number in the block collection (e.g. "number").
	BlockNumberField string
	// DependentCollections are collections that reference blocks via "blockNumber" field,
	// listed in deletion order (deleted before the block collection).
	DependentCollections []string
}

// DefaultCollectionConfig returns the default Ethereum mainnet collection config.
func DefaultCollectionConfig() CollectionConfig {
	return CollectionConfig{
		BlockCollection:  constants.CollectionBlock,
		BlockNumberField: "number", //nolint:goconst
		DependentCollections: []string{
			constants.CollectionBlockSignature,
			constants.CollectionAccessListEntry,
			constants.CollectionLog,
			constants.CollectionTransaction,
		},
	}
}

// MaxDocs returns the effective maximum document count: max_blocks * docs_per_block.
func (c *Config) MaxDocs() int64 {
	return c.MaxBlocks * int64(c.DocsPerBlock)
}

// SetDefaults fills in zero-value fields with sensible defaults.
func (c *Config) SetDefaults() {
	if c.MaxBlocks <= 0 {
		c.MaxBlocks = 10000
	}
	if c.DocsPerBlock <= 0 {
		c.DocsPerBlock = 1000
	}
	if c.IntervalSeconds <= 0 {
		c.IntervalSeconds = 60
	}
}
