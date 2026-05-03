package pruner

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultCollectionConfig(t *testing.T) {
	cfg := DefaultCollectionConfig()
	assert.Equal(t, "Ethereum__Mainnet__Block", cfg.BlockCollection)
	assert.Equal(t, "number", cfg.BlockNumberField)
	assert.Len(t, cfg.DependentCollections, 4)
	assert.Contains(t, cfg.DependentCollections, "Ethereum__Mainnet__Transaction")
	assert.Contains(t, cfg.DependentCollections, "Ethereum__Mainnet__Log")
	assert.Contains(t, cfg.DependentCollections, "Ethereum__Mainnet__AccessListEntry")
	assert.Contains(t, cfg.DependentCollections, "Ethereum__Mainnet__BatchSignature")
}

func TestConfigMaxDocs(t *testing.T) {
	cfg := &Config{MaxBlocks: 100, DocsPerBlock: 1000}
	assert.Equal(t, int64(100000), cfg.MaxDocs())

	cfg2 := &Config{MaxBlocks: 0, DocsPerBlock: 1000}
	assert.Equal(t, int64(0), cfg2.MaxDocs())
}

func TestConfigSetDefaults(t *testing.T) {
	t.Run("fills zero values", func(t *testing.T) {
		cfg := &Config{}
		cfg.SetDefaults()
		assert.Equal(t, int64(10000), cfg.MaxBlocks)
		assert.Equal(t, 1000, cfg.DocsPerBlock)
		assert.Equal(t, 60, cfg.IntervalSeconds)
	})

	t.Run("preserves non-zero values", func(t *testing.T) {
		cfg := &Config{MaxBlocks: 500, DocsPerBlock: 200, IntervalSeconds: 30}
		cfg.SetDefaults()
		assert.Equal(t, int64(500), cfg.MaxBlocks)
		assert.Equal(t, 200, cfg.DocsPerBlock)
		assert.Equal(t, 30, cfg.IntervalSeconds)
	})

	t.Run("fills negative values", func(t *testing.T) {
		cfg := &Config{MaxBlocks: -1, DocsPerBlock: -1, IntervalSeconds: -1}
		cfg.SetDefaults()
		assert.Equal(t, int64(10000), cfg.MaxBlocks)
		assert.Equal(t, 1000, cfg.DocsPerBlock)
		assert.Equal(t, 60, cfg.IntervalSeconds)
	})
}