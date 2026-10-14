package defradb

import (
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/stretchr/testify/require"
)

func TestNewSchemaApplierFromDir_Default(t *testing.T) {
	t.Parallel()
	applier := NewSchemaApplierFromDir(evm.NewCollectionNames(evm.DefaultCollectionPrefix))
	if applier.Collections == nil {
		t.Error("expected non-nil Collections")
	}
}

func TestNewSchemaApplierFromDir_WithPrefix(t *testing.T) {
	t.Parallel()
	applier := NewSchemaApplierFromDir(evm.NewCollectionNames("Arbitrum__Mainnet"))
	if applier.Collections.Prefix() != "Arbitrum__Mainnet" {
		t.Errorf("expected Arbitrum__Mainnet, got %s", applier.Collections.Prefix())
	}
}

func TestSchemaApplierFromDir_ProvidesDefaultSchema(t *testing.T) {
	t.Parallel()
	applier := NewSchemaApplierFromDir(evm.NewCollectionNames(evm.DefaultCollectionPrefix))
	files, err := applier.Collections.CollectionFiles()
	require.NoError(t, err)
	found := false
	for _, file := range files {
		if strings.Contains(file.SDL, evm.DefaultCollectionPrefix+"__Block") {
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
	applier := NewSchemaApplierFromDir(evm.NewCollectionNames("Arbitrum__Mainnet"))
	files, err := applier.Collections.CollectionFiles()
	require.NoError(t, err)
	for _, file := range files {
		if strings.Contains(file.SDL, evm.DefaultCollectionPrefix) {
			t.Errorf("collection file %s should not contain default prefix", file.File)
		}
	}
}
