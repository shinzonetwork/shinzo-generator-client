package solana_test

import (
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCollectionNames_DefaultPrefix(t *testing.T) {
	t.Parallel()

	c := solana.NewCollectionNames(solana.DefaultCollectionPrefix)

	assert.Equal(t, solana.DefaultCollectionPrefix, c.Prefix())
	assert.Equal(t, solana.DefaultCollections(), c.AllCollections())
}

func TestNewCollectionNames_CustomPrefix(t *testing.T) {
	t.Parallel()

	c := solana.NewCollectionNames("Solana__Devnet")

	assert.Equal(t, "Solana__Devnet", c.Prefix())
	assert.Equal(t, []string{
		"Solana__Devnet__Block",
		"Solana__Devnet__BlockSignature",
		"Solana__Devnet__SnapshotSignature",
		"Solana__Devnet__Transaction",
		"Solana__Devnet__Instruction",
		"Solana__Devnet__TokenBalanceChange",
		"Solana__Devnet__Reward",
	}, c.AllCollections())
}

func TestSchemaApplyOrder_MatchesAllCollections(t *testing.T) {
	t.Parallel()

	c := solana.NewCollectionNames("Solana__Testnet")
	assert.Equal(t, c.AllCollections(), c.SchemaApplyOrder())
	assert.Equal(t, solana.SchemaApplyOrder(), solana.NewCollectionNames(solana.DefaultCollectionPrefix).SchemaApplyOrder())
}

func TestCollectionFileForType(t *testing.T) {
	t.Parallel()

	c := solana.NewCollectionNames("Solana__Devnet")

	assert.Equal(t, "block.graphql", c.CollectionFileForType("Solana__Devnet__Block"))
	assert.Equal(t, "transaction.graphql", c.CollectionFileForType("Solana__Devnet__Transaction"))
	assert.Equal(t, "instruction.graphql", c.CollectionFileForType("Solana__Devnet__Instruction"))
	assert.Equal(t, "tokenBalanceChange.graphql", c.CollectionFileForType("Solana__Devnet__TokenBalanceChange"))
	assert.Equal(t, "reward.graphql", c.CollectionFileForType("Solana__Devnet__Reward"))
	assert.Equal(t, "blockSignature.graphql", c.CollectionFileForType("Solana__Devnet__BlockSignature"))
	assert.Equal(t, "snapshotSignature.graphql", c.CollectionFileForType("Solana__Devnet__SnapshotSignature"))

	// A type name from a different chain's prefix maps to no file.
	assert.Empty(t, c.CollectionFileForType("Ethereum__Mainnet__Block"))
	assert.Empty(t, c.CollectionFileForType("Block"))
}

func TestGetCollection(t *testing.T) {
	t.Parallel()

	c := solana.NewCollectionNames(solana.DefaultCollectionPrefix)

	for _, tt := range []struct {
		role string
		want string
	}{
		{chains.TypeBlock, solana.CollectionBlock},
		{chains.TypeBlockSignature, solana.CollectionBlockSignature},
		{chains.TypeSnapshotSignature, solana.CollectionSnapshotSignature},
		{chains.TypeTransaction, solana.CollectionTransaction},
		{chains.TypeInstruction, solana.CollectionInstruction},
		{chains.TypeTokenBalanceChange, solana.CollectionTokenBalanceChange},
		{chains.TypeReward, solana.CollectionReward},
	} {
		got, err := c.GetCollection(tt.role)
		require.NoError(t, err, "role %q", tt.role)
		assert.Equal(t, tt.want, got, "role %q", tt.role)
	}
}

func TestGetCollection_UnknownRoles(t *testing.T) {
	t.Parallel()

	c := solana.NewCollectionNames(solana.DefaultCollectionPrefix)

	// EVM-only roles are not defined by the Solana adapter.
	for _, role := range []string{chains.TypeAccessListEntry, chains.TypeLog, "nonexistent"} {
		got, err := c.GetCollection(role)
		require.Error(t, err, "role %q", role)
		assert.Empty(t, got, "role %q", role)
		assert.ErrorIs(t, err, chains.ErrUnknownCollection, "role %q", role)
	}
}

func TestCollectionSDL_PrefixSwap(t *testing.T) {
	t.Parallel()

	c := solana.NewCollectionNames("Solana__Devnet")

	sdl, err := c.CollectionSDL("block.graphql")
	require.NoError(t, err)
	assert.Contains(t, sdl, "type Solana__Devnet__Block {")
	assert.NotContains(t, sdl, solana.DefaultCollectionPrefix, "embedded prefix must be fully swapped")

	sdl, err = c.CollectionSDL("reward.graphql")
	require.NoError(t, err)
	assert.Contains(t, sdl, "type Solana__Devnet__Reward {")
}

func TestCollectionSDL_Errors(t *testing.T) {
	t.Parallel()

	c := solana.NewCollectionNames(solana.DefaultCollectionPrefix)

	_, err := c.CollectionSDL("nonexistent.graphql")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read")

	_, err = c.CollectionSDL("")
	require.Error(t, err)
}

func TestSchemaLoader_Integration(t *testing.T) {
	t.Parallel()

	// The generic schema loader must serve the Solana adapter's own SDL via
	// the CollectionSDLProvider extension.
	c := solana.NewCollectionNames("Solana__Devnet")

	files, err := schema.ListCollectionFiles(c)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"block.graphql",
		"blockSignature.graphql",
		"snapshotSignature.graphql",
		"transaction.graphql",
		"instruction.graphql",
		"tokenBalanceChange.graphql",
		"reward.graphql",
	}, files)

	sdl, err := schema.LoadSchemaSDLForChain(c)
	require.NoError(t, err)
	for _, typeName := range c.AllCollections() {
		assert.Contains(t, sdl, "type "+typeName+" {", typeName)
	}
	assert.NotContains(t, sdl, solana.DefaultCollectionPrefix, "embedded prefix must be fully swapped")
	assert.NotContains(t, sdl, "Ethereum__Mainnet", "EVM SDL must not leak into Solana schema")
}

func TestFactory_Collections(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Chain: config.ChainConfig{
			Adapter: solana.AdapterName,
			Name:    "Solana",
			Network: "Devnet",
		},
	}

	c, err := chains.NewCollections(cfg)
	require.NoError(t, err)
	assert.Equal(t, "Solana__Devnet", c.Prefix())

	// Nil chain fields fall back to the Solana defaults.
	c, err = chains.NewCollections(&config.Config{Chain: config.ChainConfig{Adapter: solana.AdapterName}})
	require.NoError(t, err)
	assert.Equal(t, solana.DefaultCollectionPrefix, c.Prefix())
}

func TestFactory_FetcherAndConverter(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Chain: config.ChainConfig{
			Adapter: solana.AdapterName,
			Name:    "Solana",
			Network: "Devnet",
		},
		Solana: config.SolanaConfig{
			RPCURL:                         "https://api.devnet.solana.com",
			Commitment:                     config.DefaultSolanaCommitment,
			MaxSupportedTransactionVersion: config.DefaultSolanaMaxSupportedTxVersion,
		},
	}

	// Both fetcher and converter construct without dialing (Connect
	// performs the RPC health check).
	f, err := chains.NewFetcher(cfg)
	require.NoError(t, err)
	require.NotNil(t, f)
	sf, ok := f.(*solana.Fetcher)
	require.True(t, ok, "factory must return the concrete *solana.Fetcher")
	assert.NotNil(t, sf)

	c, err := chains.NewConverter(cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
	sc, ok := c.(*solana.Converter)
	require.True(t, ok, "factory must return the concrete *solana.Converter")
	assert.Equal(t, "Solana__Devnet", sc.Collections().Prefix(), "converter must use the configured prefix")
	assert.Equal(t, "Solana__Devnet__BlockSignature", sc.SignatureCollection())

	sdl, err := c.GetSchema()
	require.NoError(t, err)
	assert.Contains(t, sdl, "type Solana__Devnet__Block {")
}
