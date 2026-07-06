package defradb

import (
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/stretchr/testify/require"
)

func TestNewSchemaApplierFromDir_Default(t *testing.T) {
	t.Parallel()
	applier := NewSchemaApplierFromDir("")
	if applier.ChainPrefix != "" {
		t.Error("expected empty ChainPrefix for default")
	}
}

func TestNewSchemaApplierFromDir_WithPrefix(t *testing.T) {
	t.Parallel()
	applier := NewSchemaApplierFromDir("Arbitrum__Mainnet")
	if applier.ChainPrefix != "Arbitrum__Mainnet" {
		t.Errorf("expected Arbitrum__Mainnet, got %s", applier.ChainPrefix)
	}
}

func TestSchemaApplierFromDir_ProvidesDefaultSchema(t *testing.T) {
	t.Parallel()
	applier := NewSchemaApplierFromDir("")
	prefix := applier.ChainPrefix
	if prefix == "" {
		prefix = constants.DefaultCollectionPrefix
	}
	files, err := schema.ListCollectionFiles()
	require.NoError(t, err)
	found := false
	for _, file := range files {
		sdl, err := schema.LoadCollectionSDLForChain(file, prefix)
		require.NoError(t, err)
		if strings.Contains(sdl, constants.DefaultCollectionPrefix+"__Block") {
			found = true
			break
		}
	}
	if !found {
		t.Error("at least one collection file should contain default Block type")
	}
}

func TestSchemaApplierFromDir_ChainPrefixReplaces(t *testing.T) {
	t.Parallel()
	applier := NewSchemaApplierFromDir("Arbitrum__Mainnet")
	files, err := schema.ListCollectionFiles()
	require.NoError(t, err)
	for _, file := range files {
		sdl, err := schema.LoadCollectionSDLForChain(file, applier.ChainPrefix)
		require.NoError(t, err)
		if strings.Contains(sdl, constants.DefaultCollectionPrefix) {
			t.Errorf("collection file %s should not contain default prefix", file)
		}
	}
}
