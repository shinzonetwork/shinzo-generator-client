package defradb

import (
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
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
	var sdl string
	if applier.ChainPrefix != "" {
		sdl = schema.GetSchemaForChain(applier.ChainPrefix)
	} else {
		sdl = schema.GetSchema()
	}
	if sdl == "" {
		t.Fatal("schema should not be empty")
	}
	if !strings.Contains(sdl, constants.DefaultCollectionPrefix+"__Block") {
		t.Error("schema should contain default Block type")
	}
}

func TestSchemaApplierFromDir_ChainPrefixReplaces(t *testing.T) {
	t.Parallel()
	applier := NewSchemaApplierFromDir("Arbitrum__Mainnet")
	sdl := schema.GetSchemaForChain(applier.ChainPrefix)
	if strings.Contains(sdl, constants.DefaultCollectionPrefix) {
		t.Error("schema with custom prefix should not contain default prefix")
	}
	if !strings.Contains(sdl, "Arbitrum__Mainnet__Block") {
		t.Error("schema should contain prefixed Block type")
	}
}
