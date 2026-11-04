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

func TestClientUndoDataErrorNeverMatchesIsErrNotFound(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return serveErr(int(btcjson.ErrRPCBlockNotFound), "Block not available (undo data is not available)")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	_, err := client.GetBlockVerbosity3(t.Context(), testBlockHash)
	require.Error(t, err)
	require.False(t, errors.IsErrNotFound(err),
		"the pruned-node outcome routes to the archive fallback, never to a not-found retry loop")
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
