package bitcoin

// The consumer-facing error contract, pinned with pkg/errors' own helpers:
// which client outcomes read as not-found for the shared retry and walk-back
// logic, and which deliberately must not.

import (
	"testing"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
)

func TestClientErrorMatchesIsErrNotFound(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return serveErr(int(btcjson.ErrRPCBlockNotFound), "Block not found")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	_, err := client.GetBlockVerbosity3(t.Context(), testBlockHash)
	require.Error(t, err)
	require.True(t, errors.IsErrNotFound(err),
		"an unknown hash keeps Core's not-found text for the walk-back searchers")
}

func TestClientPrunedBlockErrorNeverMatchesIsErrNotFound(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return serveErr(int(btcjson.ErrRPCMisc), "Block not available (pruned data)")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	_, err := client.GetBlockVerbosity3(t.Context(), testBlockHash)
	require.Error(t, err)
	require.ErrorIs(t, err, errBlockPruned)
	require.False(t, errors.IsErrNotFound(err),
		"the pruned-node outcome routes to the archive fallback, never to a not-found retry loop")
}

// TestClientPrunedMarkerMatchIsNarrow pins the sentinel to Core's exact
// pruned-data flavor: the transient download race and a marker-less misc
// error must stay untyped, keeping them in the processor's generic retry
// path instead of the archive fallback.
func TestClientPrunedMarkerMatchIsNarrow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		message string
	}{
		{name: "the transient not-fully-downloaded flavor stays retryable", message: "Block not available (not fully downloaded)"},
		{name: "a misc error without the marker text stays generic", message: "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
				return serveErr(int(btcjson.ErrRPCMisc), tt.message)
			})
			cfg := testConfig()
			cfg.RPCURL = server.URL
			client := newTestClient(t, cfg)

			_, err := client.GetBlockVerbosity3(t.Context(), testBlockHash)
			require.Error(t, err)
			require.NotErrorIs(t, err, errBlockPruned)
			require.False(t, errors.IsErrNotFound(err),
				"neither flavor carries a not-found substring, so both stay in the generic retry bucket")
		})
	}
}

func TestClientHeightOutOfRangeNeverMatchesIsErrNotFound(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return serveErr(int(btcjson.ErrRPCInvalidParameter), "Block height out of range")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	_, err := client.GetBlockHash(t.Context(), 999999999)
	require.Error(t, err)
	require.False(t, errors.IsErrNotFound(err),
		"the fetcher owns wrapping heights past the tip into not-found")
}
