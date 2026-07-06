package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_DefaultSchema(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema"}, &buf))
	sdl := buf.String()
	assert.NotEmpty(t, sdl)
	for _, typeName := range constants.DefaultCollections() {
		assert.Contains(t, sdl, typeName)
	}
}

func TestRun_WithPrefix(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--prefix", "Arbitrum__Mainnet"}, &buf))
	sdl := buf.String()
	assert.NotEmpty(t, sdl)
	assert.NotContains(t, sdl, constants.DefaultCollectionPrefix)
	assert.Contains(t, sdl, "Arbitrum__Mainnet__Block")
}

func TestRun_PrefixReplacesAllCollectionTypes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	prefix := "Optimism__Mainnet"
	require.NoError(t, run([]string{"build_schema", "--prefix", prefix}, &buf))
	sdl := buf.String()
	collections := constants.NewCollectionNames(prefix)
	for _, name := range collections.AllCollections() {
		assert.Contains(t, sdl, name)
	}
}

func TestRun_InvalidFlag(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.Error(t, run([]string{"build_schema", "--nonexistent"}, &buf))
}

func TestRun_OutputMatchesGetSchema(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema"}, &buf))
	expected, err := schema.GetSchema()
	require.NoError(t, err)
	assert.Equal(t, expected, buf.String())
}

func TestRun_OutputWithPrefixMatchesGetSchemaForChain(t *testing.T) {
	t.Parallel()
	prefix := "Arbitrum__Mainnet"
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--prefix", prefix}, &buf))
	expected, err := schema.GetSchemaForChain(prefix)
	require.NoError(t, err)
	assert.Equal(t, expected, buf.String())
}

func TestRun_SingleFile(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--file", "block.graphql"}, &buf))
	sdl := buf.String()
	assert.NotEmpty(t, sdl)
	assert.Contains(t, sdl, "Ethereum__Mainnet__Block")
	assert.NotContains(t, sdl, "type Ethereum__Mainnet__Transaction")
}

func TestRun_SingleFileWithPrefix(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--file", "block.graphql", "--prefix", "Arbitrum__Mainnet"}, &buf))
	sdl := buf.String()
	assert.NotContains(t, sdl, "Ethereum__Mainnet")
	assert.Contains(t, sdl, "Arbitrum__Mainnet__Block")
}

func TestRun_SingleFileNotFound(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.Error(t, run([]string{"build_schema", "--file", "nonexistent.graphql"}, &buf))
}

func TestRun_ListFiles(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--list-files"}, &buf))
	output := strings.TrimSpace(buf.String())
	assert.NotEmpty(t, output)
	lines := strings.Split(output, "\n")
	expected, err := schema.ListCollectionFiles()
	require.NoError(t, err)
	assert.Equal(t, expected, lines)
}

func TestRun_ListFilesIgnoresPrefix(t *testing.T) {
	t.Parallel()
	var bufNoPrefix, bufWithPrefix bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--list-files"}, &bufNoPrefix))
	require.NoError(t, run([]string{"build_schema", "--list-files", "--prefix", "Arbitrum__Mainnet"}, &bufWithPrefix))
	assert.Equal(t, bufNoPrefix.String(), bufWithPrefix.String())
}
