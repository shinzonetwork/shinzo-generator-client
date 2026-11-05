package indexer

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStartIndexing_RPCServesAnotherChain checks that a generator whose RPC reports another
// chain ID refuses to start before DefraDB opens its store.
func TestStartIndexing_RPCServesAnotherChain(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	// The RPC reports chain ID 56 to a generator configured for chain 1.
	rpcServer := newMockRPCServer(func(string, json.RawMessage) (any, error) { return "0x38", nil })
	t.Cleanup(rpcServer.Close)

	storePath := t.TempDir()
	cfg := &config.Config{
		Chain: config.ChainConfig{Name: "Ethereum", Network: "Mainnet", ChainID: testChainID},
		DefraDB: config.DefraDBConfig{
			URL:           testDefraRandomURL,
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           testDefraP2PDisabled,
			Store:         config.DefraDBStoreConfig{Path: storePath},
		},
		Geth:    config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{ReceiptWorkers: 1, MaxDocsPerTxn: 100},
		Logger:  config.LoggerConfig{Development: true},
	}
	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	err = indexer.StartIndexing(false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect fetcher for Ethereum Mainnet (chain_id 1)")

	entries, err := os.ReadDir(storePath)
	require.NoError(t, err)
	assert.Empty(t, entries, "DefraDB started before the RPC's chain ID was checked")
}
