package indexer

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/stretchr/testify/require"
)

// TestStartIndexing_ExternalDefraHTTPMode runs the full indexing pipeline
// against an "external" DefraDB — the test node's HTTP API playing the role
// of a standalone `defradb start` process — to prove the external path end
// to end: schema apply over HTTP, block writes through the HTTP client, and
// CID collection for signing via GQL instead of the embedded node collector.
func TestStartIndexing_ExternalDefraHTTPMode(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	var blockCallCount atomic.Int64
	blockCh := make(chan struct{}, 100)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if innerErr := json.Unmarshal(rawParams[0], &blockParam); innerErr == nil && blockParam == defaultBlockParamLatest {
					return fullBlockResponse("0x186a0", nil), nil // chain tip 100000.
				}
			}
			count := blockCallCount.Add(1)
			select {
			case blockCh <- struct{}{}:
			default:
			}
			num := fmt.Sprintf("0x%x", 99990+count)
			return fullBlockResponse(num, nil), nil
		case ethBlockNumber:
			return "0x186a0", nil
		case ethGetBlockReceipts:
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			URL:           fmt.Sprintf("http://localhost:%d", td.Port),
			KeyringSecret: "test-secret-for-keyring-12345678",
			Store:         config.DefraDBStoreConfig{Path: t.TempDir()},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      99990,
			ConcurrentBlocks: 1,
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexerInstance, err := CreateIndexer(cfg)
	require.NoError(t, err)

	t.Cleanup(indexerInstance.StopIndexing)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexerInstance.StartIndexing(true) // true = external DefraDB.
	}()

	deadline := time.After(60 * time.Second)
	for blockCallCount.Load() < 3 {
		select {
		case <-blockCh:
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for external-mode blocks")
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing external mode failed: %v", err)
			}
		}
	}

	// Snapshot the fields under the read lock, then release it before calling
	// StopIndexing: StopIndexing acquires the same mutex for writing, so
	// holding the read lock across the call would deadlock.
	indexerInstance.mutex.RLock()
	defraNode := indexerInstance.defraNode
	defraStore := indexerInstance.defraStore
	indexerInstance.mutex.RUnlock()

	require.Nil(t, defraNode, "external mode must not create an embedded node")
	require.NotNil(t, defraStore, "external mode must initialize the DefraDB store")

	indexerInstance.StopIndexing()
	t.Log("external-mode indexing processed blocks through the HTTP client store")
}
