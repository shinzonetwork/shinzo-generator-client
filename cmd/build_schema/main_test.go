package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
)

func TestRun_DefaultSchema(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := run([]string{"build_schema"}, &buf); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	sdl := buf.String()
	if sdl == "" {
		t.Fatal("expected non-empty schema output")
	}
	for _, typeName := range constants.DefaultCollections() {
		if !strings.Contains(sdl, typeName) {
			t.Errorf("schema missing expected type %q", typeName)
		}
	}
}

func TestRun_WithPrefix(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := run([]string{"build_schema", "--prefix", "Arbitrum__Mainnet"}, &buf); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	sdl := buf.String()
	if sdl == "" {
		t.Fatal("expected non-empty schema output")
	}
	if strings.Contains(sdl, constants.DefaultCollectionPrefix) {
		t.Errorf("schema with custom prefix should not contain default prefix %q", constants.DefaultCollectionPrefix)
	}
	if !strings.Contains(sdl, "Arbitrum__Mainnet__Block") {
		t.Error("schema should contain prefixed Block type")
	}
}

func TestRun_PrefixReplacesAllCollectionTypes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	prefix := "Optimism__Mainnet"
	if err := run([]string{"build_schema", "--prefix", prefix}, &buf); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	sdl := buf.String()
	collections := constants.NewCollectionNames(prefix)
	for _, name := range collections.AllCollections() {
		if !strings.Contains(sdl, name) {
			t.Errorf("schema missing expected type %q", name)
		}
	}
}

func TestRun_InvalidFlag(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := run([]string{"build_schema", "--nonexistent"}, &buf)
	if err == nil {
		t.Fatal("expected error for invalid flag")
	}
}

func TestRun_OutputMatchesGetSchema(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := run([]string{"build_schema"}, &buf); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	expected := schema.GetSchema()
	if buf.String() != expected {
		t.Error("output should match schema.GetSchema()")
	}
}

func TestRun_OutputWithPrefixMatchesGetSchemaForChain(t *testing.T) {
	t.Parallel()
	prefix := "Arbitrum__Mainnet"
	var buf bytes.Buffer
	if err := run([]string{"build_schema", "--prefix", prefix}, &buf); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	expected := schema.GetSchemaForChain(prefix)
	if buf.String() != expected {
		t.Error("output should match schema.GetSchemaForChain()")
	}
}
