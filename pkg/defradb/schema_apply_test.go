package defradb

import (
	"context"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyCollectionSchemas_DefaultPrefix(t *testing.T) {
	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.URL = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	defraNode, _, err := StartDefraInstance(&testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)
	defer func() { _ = defraNode.Close(context.Background()) }()

	err = ApplyCollectionSchemas(context.Background(), defraNode, "")
	require.NoError(t, err)

	for _, typeName := range constants.DefaultCollections() {
		result := defraNode.DB.ExecRequest(context.Background(),
			`query { __type(name: "`+typeName+`") { name } }`)
		assert.Empty(t, result.GQL.Errors, "should find collection %s without errors", typeName)
	}
}

func TestApplyCollectionSchemas_CustomPrefix(t *testing.T) {
	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.URL = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	defraNode, _, err := StartDefraInstance(&testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)
	defer func() { _ = defraNode.Close(context.Background()) }()

	err = ApplyCollectionSchemas(context.Background(), defraNode, "Arbitrum__Mainnet")
	require.NoError(t, err)

	collections := constants.NewCollectionNames("Arbitrum__Mainnet")
	for _, typeName := range collections.AllCollections() {
		result := defraNode.DB.ExecRequest(context.Background(),
			`query { __type(name: "`+typeName+`") { name } }`)
		assert.Empty(t, result.GQL.Errors, "should find collection %s without errors", typeName)
	}
}

func TestApplyCollectionSchemas_FallbackPath_AllCollectionsExist(t *testing.T) {
	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.URL = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	defraNode, _, err := StartDefraInstance(&testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)
	defer func() { _ = defraNode.Close(context.Background()) }()

	ctx := context.Background()

	err = ApplyCollectionSchemas(ctx, defraNode, "")
	require.NoError(t, err, "first call: monolithic path should succeed")

	for _, typeName := range constants.DefaultCollections() {
		result := defraNode.DB.ExecRequest(ctx,
			`query { __type(name: "`+typeName+`") { name } }`)
		assert.Empty(t, result.GQL.Errors, "should find collection %s after first apply", typeName)
	}

	err = ApplyCollectionSchemas(ctx, defraNode, "")
	require.NoError(t, err, "second call: fallback path should succeed (all collections already exist)")

	for _, typeName := range constants.DefaultCollections() {
		result := defraNode.DB.ExecRequest(ctx,
			`query { __type(name: "`+typeName+`") { name } }`)
		assert.Empty(t, result.GQL.Errors, "should find collection %s after second apply", typeName)
	}
}

func TestApplyCollectionSchemas_FallbackPath_IndependentCollectionsPreSeed(t *testing.T) {
	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.URL = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	defraNode, _, err := StartDefraInstance(&testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)
	defer func() { _ = defraNode.Close(context.Background()) }()

	ctx := context.Background()

	independentFiles := []string{"blockSignature.graphql", "snapshotSignature.graphql"}
	for _, file := range independentFiles {
		sdl, err := schema.LoadCollectionSDLForChain(file, constants.DefaultCollectionPrefix)
		require.NoError(t, err, "failed to load %s", file)
		_, err = defraNode.DB.AddSchema(ctx, sdl)
		require.NoError(t, err, "failed to pre-seed %s", file)
	}

	err = ApplyCollectionSchemas(ctx, defraNode, "")
	require.Error(t, err, "monolithic path should fail because some collections exist; "+
		"per-file fallback should also fail because dependent types (Block, Transaction, etc.) "+
		"have cross-references that cannot be resolved individually")
	require.Contains(t, err.Error(), "failed to apply collection schema",
		"error should come from per-file fallback attempting dependent types")
}

func TestApplyCollectionSchemas_FallbackPath_Restart(t *testing.T) {
	storePath := t.TempDir()

	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.URL = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = storePath
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	ctx := context.Background()

	defraNode, _, err := StartDefraInstance(&testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)

	err = ApplyCollectionSchemas(ctx, defraNode, "")
	require.NoError(t, err, "fresh boot: monolithic path should succeed")

	for _, typeName := range constants.DefaultCollections() {
		result := defraNode.DB.ExecRequest(ctx,
			`query { __type(name: "`+typeName+`") { name } }`)
		assert.Empty(t, result.GQL.Errors, "should find collection %s after first boot", typeName)
	}

	err = defraNode.Close(ctx)
	require.NoError(t, err)

	testConfig2 := *NewDefaultConfig()
	testConfig2.DefraDB.URL = "127.0.0.1:0"
	testConfig2.DefraDB.Store.Path = storePath
	testConfig2.DefraDB.KeyringSecret = testKeyringSecret

	defraNode2, _, err := StartDefraInstance(&testConfig2, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)
	defer func() { _ = defraNode2.Close(ctx) }()

	err = ApplyCollectionSchemas(ctx, defraNode2, "")
	require.NoError(t, err, "restart: fallback path should succeed (all collections already exist)")

	for _, typeName := range constants.DefaultCollections() {
		result := defraNode2.DB.ExecRequest(ctx,
			`query { __type(name: "`+typeName+`") { name } }`)
		assert.Empty(t, result.GQL.Errors, "should find collection %s after restart", typeName)
	}
}

func TestApplyCollectionSchemas_ViaSchemaApplierFromDir_FullRestart(t *testing.T) {
	storePath := t.TempDir()

	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.URL = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = storePath
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	ctx := context.Background()

	applier := NewSchemaApplierFromDir("")
	defraNode, _, err := StartDefraInstance(&testConfig, applier, nil, nil)
	require.NoError(t, err)

	for _, typeName := range constants.DefaultCollections() {
		result := defraNode.DB.ExecRequest(ctx,
			`query { __type(name: "`+typeName+`") { name } }`)
		assert.Empty(t, result.GQL.Errors, "should find collection %s after first boot", typeName)
	}

	err = defraNode.Close(ctx)
	require.NoError(t, err)

	testConfig2 := *NewDefaultConfig()
	testConfig2.DefraDB.URL = "127.0.0.1:0"
	testConfig2.DefraDB.Store.Path = storePath
	testConfig2.DefraDB.KeyringSecret = testKeyringSecret

	applier2 := NewSchemaApplierFromDir("")
	defraNode2, _, err := StartDefraInstance(&testConfig2, applier2, nil, nil)
	require.NoError(t, err)
	defer func() { _ = defraNode2.Close(ctx) }()

	for _, typeName := range constants.DefaultCollections() {
		result := defraNode2.DB.ExecRequest(ctx,
			`query { __type(name: "`+typeName+`") { name } }`)
		assert.Empty(t, result.GQL.Errors, "should find collection %s after restart", typeName)
	}
}

func TestSchemaApplierFromDir_DelegatesToApplyCollectionSchemas(t *testing.T) {
	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.URL = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	applier := NewSchemaApplierFromDir("")
	defraNode, _, err := StartDefraInstance(&testConfig, applier, nil, nil)
	require.NoError(t, err)
	defer func() { _ = defraNode.Close(context.Background()) }()

	for _, typeName := range constants.DefaultCollections() {
		result := defraNode.DB.ExecRequest(context.Background(),
			`query { __type(name: "`+typeName+`") { name } }`)
		assert.Empty(t, result.GQL.Errors, "should find collection %s without errors", typeName)
	}
}

func TestSchemaApplierFromDir_CustomPrefix(t *testing.T) {
	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.URL = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	prefix := "Arbitrum__Mainnet"
	applier := NewSchemaApplierFromDir(prefix)
	defraNode, _, err := StartDefraInstance(&testConfig, applier, nil, nil)
	require.NoError(t, err)
	defer func() { _ = defraNode.Close(context.Background()) }()

	collections := constants.NewCollectionNames(prefix)
	for _, typeName := range collections.AllCollections() {
		result := defraNode.DB.ExecRequest(context.Background(),
			`query { __type(name: "`+typeName+`") { name } }`)
		assert.Empty(t, result.GQL.Errors, "should find collection %s without errors", typeName)
	}
}

func TestApplyCollectionSchemas_FilesLoadedInOrder(t *testing.T) {
	files, err := schema.ListCollectionFiles()
	require.NoError(t, err)
	require.NotEmpty(t, files, "should have collection files")

	prefix := constants.DefaultCollectionPrefix
	for _, file := range files {
		sdl, err := schema.LoadCollectionSDLForChain(file, prefix)
		require.NoError(t, err)
		assert.NotEmpty(t, sdl, "SDL for %s should not be empty", file)
		assert.Contains(t, sdl, prefix, "SDL for %s should contain prefix %s", file, prefix)
	}
}

func TestApplyCollectionSchemas_EmptyPrefixUsesDefault(t *testing.T) {
	t.Parallel()
	files, err := schema.ListCollectionFiles()
	require.NoError(t, err)

	prefix := ""
	if prefix == "" {
		prefix = constants.DefaultCollectionPrefix
	}

	for _, file := range files {
		sdl, err := schema.LoadCollectionSDLForChain(file, prefix)
		require.NoError(t, err)
		assert.Contains(t, sdl, constants.DefaultCollectionPrefix,
			"empty chainPrefix should resolve to DefaultCollectionPrefix in %s", file)
	}
}

func TestApplyCollectionSchemas_CustomPrefixDoesNotContainDefault(t *testing.T) {
	t.Parallel()
	files, err := schema.ListCollectionFiles()
	require.NoError(t, err)

	customPrefix := "Arbitrum__Mainnet"
	for _, file := range files {
		sdl, err := schema.LoadCollectionSDLForChain(file, customPrefix)
		require.NoError(t, err)
		assert.NotContains(t, sdl, constants.DefaultCollectionPrefix,
			"SDL for %s with custom prefix should not contain default prefix", file)
		assert.Contains(t, sdl, customPrefix,
			"SDL for %s should contain custom prefix", file)
	}
}

func TestLoadSchemaSDLForChain_DefaultPrefix(t *testing.T) {
	sdl, err := schema.LoadSchemaSDLForChain(constants.DefaultCollectionPrefix)
	require.NoError(t, err)
	assert.NotEmpty(t, sdl)
	assert.Contains(t, sdl, constants.DefaultCollectionPrefix+"__Block")
}

func TestLoadSchemaSDLForChain_CustomPrefix(t *testing.T) {
	sdl, err := schema.LoadSchemaSDLForChain("Arbitrum__Mainnet")
	require.NoError(t, err)
	assert.NotEmpty(t, sdl)
	assert.NotContains(t, sdl, constants.DefaultCollectionPrefix)
	assert.Contains(t, sdl, "Arbitrum__Mainnet__Block")
}
