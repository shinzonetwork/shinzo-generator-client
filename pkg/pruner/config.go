package pruner

// Config represents pruner configuration for removing old documents.
type Config struct {
	Enabled         bool  `yaml:"enabled"`
	MaxBlocks       int64 `yaml:"max_blocks"`      // Number of blocks to retain
	DocsPerBlock    int   `yaml:"docs_per_block"`  // Average docs per block (~1057 on Ethereum mainnet)
	PruneThreshold  int64 `yaml:"prune_threshold"` // Deprecated: kept for backward compatibility, unused by pruner
	IntervalSeconds int   `yaml:"interval_seconds"`
	PruneHistory    bool  `yaml:"prune_history"`
	// MaxBlocksPerCycle caps how many blocks one cycle may delete. A cycle's wall time scales with
	// what it deletes, so without a cap a large backlog produces a cycle long enough that the queue
	// cannot be checkpointed for hours. Zero leaves a cycle unbounded.
	MaxBlocksPerCycle int64 `yaml:"max_blocks_per_cycle"`
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
	if c.MaxBlocksPerCycle <= 0 {
		c.MaxBlocksPerCycle = 50
	}
}
