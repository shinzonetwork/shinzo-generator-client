package indexer

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appsdk "github.com/shinzonetwork/shinzo-app-sdk/pkg/defra"
	"github.com/shinzonetwork/shinzo-app-sdk/pkg/pruner"
	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/snapshot"
	acpIdentity "github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/node"
)

// End-to-end check that the embedded-defra startup path produces real
// SnapshotSignature documents in DefraDB with valid cryptographic content.
// Asserts the signature DID matches the keyring identity, the merkle root
// is a valid 32-byte hash, and signatureValue decodes as hex.
//
// Not t.Parallel(): reads a package-private field that StartIndexing's
// goroutine writes.
func TestStartIndexing_Embedded_ProducesValidSnapshotSignatures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()

	blockCh := make(chan struct{}, 100)
	rpcServer := newMockRPCServerForIntegration(blockCh)
	t.Cleanup(rpcServer.Close)

	cfg := newSnapshotSigTestConfig(tmpDir, rpcServer)

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	require.Eventually(t, func() bool {
		return querySnapshotSignatureCountSafe(indexer) >= 1
	}, 90*time.Second, 500*time.Millisecond,
		"expected at least one SnapshotSignature document to be written end-to-end")

	defraNode := indexer.defraNode
	require.NotNil(t, defraNode, "indexer.defraNode must still be live for assertions")

	sigs := querySnapshotSignaturesAgainst(t, defraNode)
	require.NotEmpty(t, sigs, "SnapshotSignature collection must contain at least one document")

	sig := sigs[0]
	require.NotEmpty(t, sig["merkleRoot"], "merkleRoot must be set on the SnapshotSignature document")
	require.NotEmpty(t, sig["signatureValue"], "signatureValue must be set")
	require.NotEmpty(t, sig["signatureIdentity"], "signatureIdentity must be set")
	require.Contains(t, []string{"ES256K", "Ed25519"}, sig["signatureType"],
		"signatureType must be a known algorithm")

	// signatureValue is stored as hex; decode to confirm it is well-formed.
	sigValueStr, ok := sig["signatureValue"].(string)
	require.True(t, ok, "signatureValue must be a string")
	sigBytes, err := hex.DecodeString(sigValueStr)
	require.NoError(t, err, "signatureValue must be valid hex")
	assert.NotEmpty(t, sigBytes, "decoded signatureValue must be non-empty")

	merkleRootStr, ok := sig["merkleRoot"].(string)
	require.True(t, ok, "merkleRoot must be a string")
	merkleBytes, err := hex.DecodeString(merkleRootStr)
	require.NoError(t, err, "merkleRoot must be valid hex")
	assert.Len(t, merkleBytes, 32, "merkle root must be a 32-byte SHA256 hash")

	// Re-load the node identity from the same keyring and confirm the
	// signature carries that DID.
	appCfg := toAppConfig(cfg)
	loadedIdent, err := appsdk.GetOrCreateNodeIdentity(appCfg)
	require.NoError(t, err)
	loadedFullIdent, ok := loadedIdent.(acpIdentity.FullIdentity)
	require.True(t, ok, "loaded node identity must be a FullIdentity")
	expectedDID := loadedFullIdent.PublicKey().String()
	assert.Equal(t, expectedDID, sig["signatureIdentity"],
		"signatureIdentity must match the keyring-loaded node identity DID")

	indexer.StopIndexing()
	require.Eventually(t, func() bool {
		return !indexer.IsStarted()
	}, 5*time.Second, 50*time.Millisecond, "indexer must report stopped after StopIndexing")

	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
	}
}

// newSnapshotSigTestConfig enables the snapshotter with a small block window
// and a 1-second tick so a snapshot fires within the test deadline.
func newSnapshotSigTestConfig(tmpDir string, rpcServer *httptest.Server) *config.Config {
	return &config.Config{
		DefraDB: config.DefraDBConfig{
			Url:           "",
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P: config.DefraDBP2PConfig{
				Enabled: false,
			},
			Store: config.DefraDBStoreConfig{
				Path: tmpDir,
			},
		},
		Geth: config.GethConfig{
			NodeURL: rpcServer.URL,
		},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 1,
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Pruner: pruner.Config{
			Enabled:         false,
			MaxBlocks:       1000,
			PruneThreshold:  500,
			IntervalSeconds: 3600,
		},
		Snapshot: snapshot.Config{
			Enabled:         true,
			Dir:             filepath.Join(tmpDir, "snapshots"),
			BlocksPerFile:   2,
			IntervalSeconds: 1,
		},
		Logger: config.LoggerConfig{Development: true},
	}
}

// querySnapshotSignatureCountSafe counts SnapshotSignature documents in the
// indexer's embedded defra. Returns 0 on any error so polling can retry.
func querySnapshotSignatureCountSafe(indexer *ChainIndexer) int {
	if indexer.defraNode == nil {
		return 0
	}
	query := fmt.Sprintf(
		`query { %s { _docID } }`,
		constants.CollectionSnapshotSignature,
	)
	result := indexer.defraNode.DB.ExecRequest(context.Background(), query)
	if len(result.GQL.Errors) > 0 {
		return 0
	}
	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return 0
	}
	raw, ok := data[constants.CollectionSnapshotSignature]
	if !ok || raw == nil {
		return 0
	}
	switch typed := raw.(type) {
	case []any:
		return len(typed)
	case []map[string]any:
		return len(typed)
	}
	return 0
}

// querySnapshotSignaturesAgainst returns the full SnapshotSignature documents
// on the given node and fails the test on a query error.
func querySnapshotSignaturesAgainst(t *testing.T, defraNode *node.Node) []map[string]any {
	t.Helper()
	require.NotNil(t, defraNode, "defraNode must be non-nil")
	query := fmt.Sprintf(
		`query { %s { _docID merkleRoot signatureValue signatureIdentity signatureType blockCount snapshotFile startBlock endBlock } }`,
		constants.CollectionSnapshotSignature,
	)
	result := defraNode.DB.ExecRequest(context.Background(), query)
	require.Empty(t, result.GQL.Errors, "SnapshotSignature query must succeed: %v", result.GQL.Errors)

	data, ok := result.GQL.Data.(map[string]any)
	require.True(t, ok, "GQL response must be a map")
	raw, ok := data[constants.CollectionSnapshotSignature]
	if !ok || raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			m, ok := item.(map[string]any)
			require.True(t, ok, "expected each document to be a map[string]any, got %T", item)
			out = append(out, m)
		}
		return out
	case []map[string]any:
		return typed
	default:
		t.Fatalf("unexpected document list shape: %T", raw)
		return nil
	}
}
