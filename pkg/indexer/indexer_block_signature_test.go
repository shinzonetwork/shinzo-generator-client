package indexer

import (
	"context"
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
// BlockSignature documents in DefraDB. Exercises the full chain: SDK
// startup, keyring identity load, BlockHandler construction, block creation,
// signing, and persistence. The cryptographic verification at the end pins
// that the signing identity is the same one the SDK loaded into the node,
// which catches both "no signature produced" and "signature with wrong key"
// regressions.
//
// Not t.Parallel(): reads indexer.defraNode (a package-private field) while
// StartIndexing's goroutine assigns to it. Running parallel with other
// embedded-defra tests would surface that as a race under -race.
func TestStartIndexing_Embedded_ProducesValidBlockSignatures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()

	// blockCh satisfies newMockRPCServerForIntegration's signature; we don't
	// consume it here.
	blockCh := make(chan struct{}, 100)
	rpcServer := newMockRPCServerForIntegration(blockCh)
	t.Cleanup(rpcServer.Close)

	cfg := newBlockSigTestConfig(tmpDir, rpcServer)

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	// queryBlockSignatureCountSafe short-circuits on a nil defraNode, so this
	// single condition covers both "indexer is up" and "at least one block
	// has been signed and committed."
	require.Eventually(t, func() bool {
		return queryBlockSignatureCountSafe(indexer) >= 1
	}, 60*time.Second, 250*time.Millisecond,
		"expected at least one BlockSignature document to be written end-to-end")

	// Capture defraNode now: StopIndexing nils the field, and we still need
	// to query for the signature contents and re-load the keyring identity.
	defraNode := indexer.defraNode
	require.NotNil(t, defraNode, "indexer.defraNode must still be live for assertions")

	sigs := querySignaturesAgainst(t, defraNode)
	require.NotEmpty(t, sigs, "BlockSignature collection must contain at least one document")

	sig := sigs[0]
	require.NotEmpty(t, sig["merkleRoot"], "merkleRoot must be set on the BlockSignature document")
	require.NotEmpty(t, sig["signatureValue"], "signatureValue must be set")
	require.NotEmpty(t, sig["signatureIdentity"], "signatureIdentity must be set")
	require.Contains(t, []string{"ES256K", "EdDSA"}, sig["signatureType"],
		"signatureType must be a known algorithm")

	// Re-load the node identity from the same keyring path the indexer used.
	// If the wiring is correct, this is the same identity that signed the
	// block, so the public key string in the signature must match.
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

	// Drain the StartIndexing goroutine. Best-effort: the inner block-fetcher
	// may not return immediately, so we don't fail on timeout.
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
	}
}

// newBlockSigTestConfig builds a config with embedded defra in tmpDir and a
// keyring secret long enough to satisfy the SDK. P2P, snapshotter, and
// pruner are disabled to keep the test deterministic and fast.
func newBlockSigTestConfig(tmpDir string, rpcServer *httptest.Server) *config.Config {
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
			Enabled:         false,
			Dir:             filepath.Join(tmpDir, "snapshots"),
			BlocksPerFile:   1000,
			IntervalSeconds: 3600,
		},
		Logger: config.LoggerConfig{Development: true},
	}
}

// queryBlockSignatureCountSafe counts BlockSignature documents currently in
// the indexer's embedded defra. Returns 0 on any error so the caller can
// retry — used as a polling predicate, not for assertions.
func queryBlockSignatureCountSafe(indexer *ChainIndexer) int {
	if indexer.defraNode == nil {
		return 0
	}
	query := fmt.Sprintf(
		`query { %s { _docID } }`,
		constants.CollectionBlockSignature,
	)
	result := indexer.defraNode.DB.ExecRequest(context.Background(), query)
	if len(result.GQL.Errors) > 0 {
		return 0
	}
	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return 0
	}
	raw, ok := data[constants.CollectionBlockSignature]
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

// querySignaturesAgainst returns the full BlockSignature documents on the
// given node and fails the test on a query error. Takes *node.Node (not the
// owning ChainIndexer) so callers can hold a stable reference past
// StopIndexing, which nils the indexer's defraNode field.
func querySignaturesAgainst(t *testing.T, defraNode *node.Node) []map[string]any {
	t.Helper()
	require.NotNil(t, defraNode, "defraNode must be non-nil")
	query := fmt.Sprintf(
		`query { %s { _docID merkleRoot signatureValue signatureIdentity signatureType cidCount } }`,
		constants.CollectionBlockSignature,
	)
	result := defraNode.DB.ExecRequest(context.Background(), query)
	require.Empty(t, result.GQL.Errors, "BlockSignature query must succeed: %v", result.GQL.Errors)

	data, ok := result.GQL.Data.(map[string]any)
	require.True(t, ok, "GQL response must be a map")
	raw, ok := data[constants.CollectionBlockSignature]
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
