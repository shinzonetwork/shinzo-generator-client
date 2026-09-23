package evm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveVariant(t *testing.T) {
	t.Parallel()
	tests := []struct {
		chainName string
		want      chainVariant
	}{
		{"Polygon", variantPolygon},
		{"polygon", variantPolygon},
		{"POLYGON", variantPolygon},
		{" Polygon ", variantPolygon},
		{"Ethereum", variantEthereum},
		{"Arbitrum", variantEthereum},
		{"Optimism", variantEthereum},
		{"", variantEthereum},
		{"polygons", variantEthereum},
	}
	for _, tt := range tests {
		t.Run(tt.chainName, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, resolveVariant(tt.chainName))
		})
	}
}

func TestVariantFromPrefix(t *testing.T) {
	t.Parallel()
	assert.Equal(t, variantPolygon, variantFromPrefix("Polygon__Mainnet"))
	assert.Equal(t, variantEthereum, variantFromPrefix("Ethereum__Mainnet"))
	assert.Equal(t, variantEthereum, variantFromPrefix("Arbitrum__One"))
}

func TestYParity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		txType string
		v      string
		want   string
	}{
		// Legacy: always empty, whatever v says.
		{"legacy hex type, v 27", "0x0", "0x1b", ""},
		{"legacy dec type, v 28", "0", "28", ""},
		// Typed txs, hex spellings (the spec table).
		{"0x1, v 0x0", "0x1", "0x0", "0"},
		{"0x1, v 0x1", "0x1", "0x1", "1"},
		{"0x2, v 0x1b (27)", "0x2", "0x1b", "0"},
		{"0x2, v 0x1c (28)", "0x2", "0x1c", "1"},
		{"0x7e, v 0x0", "0x7e", "0x0", "0"},
		{"0x7f, v 0x0", "0x7f", "0x0", "0"},
		// Typed txs, decimal spellings (what convertTransaction emits).
		{"1, v 0", "1", "0", "0"},
		{"2, v 1", "2", "1", "1"},
		{"2, v 27", "2", "27", "0"},
		{"2, v 28", "2", "28", "1"},
		{"126, v 0", "126", "0", "0"},
		{"127, v 0", "127", "0", "0"},
		// Typed tx, v outside the known set.
		{"2, v 5", "2", "5", ""},
		// Unrecognized types.
		{"0x3, v 0x0", "0x3", "0x0", ""},
		{"4, v 0", "4", "0", ""},
		{"0x64, v 0x0", "0x64", "0x0", ""},
		// Garbage input.
		{"empty type", "", "0x0", ""},
		{"empty v", "0x2", "", ""},
		{"junk", "not-a-number", "also-not", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, yParity(tt.txType, tt.v))
		})
	}
}

func TestCollectionFileForType_PolygonOverrides(t *testing.T) {
	t.Parallel()
	polygon := NewCollectionNames("Polygon__Mainnet")
	assert.Equal(t, "polygon/block.graphql", polygon.CollectionFileForType("Polygon__Mainnet__Block"))
	assert.Equal(t, "polygon/transaction.graphql", polygon.CollectionFileForType("Polygon__Mainnet__Transaction"))
	// Non-overridden collections keep the default mapping.
	assert.Equal(t, "log.graphql", polygon.CollectionFileForType("Polygon__Mainnet__Log"))
	assert.Equal(t, "blockSignature.graphql", polygon.CollectionFileForType("Polygon__Mainnet__BlockSignature"))

	// Ethereum and unknown chains keep the default mapping for everything.
	for _, prefix := range []string{"Ethereum__Mainnet", "Arbitrum__One"} {
		c := NewCollectionNames(prefix)
		assert.Equal(t, "block.graphql", c.CollectionFileForType(prefix+"__Block"))
		assert.Equal(t, "transaction.graphql", c.CollectionFileForType(prefix+"__Transaction"))
	}
}
