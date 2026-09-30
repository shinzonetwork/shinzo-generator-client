package solana

// Tests that the ctx-carried rpcStats collector records per-method network
// round-trip and local conversion time from the Client methods (against the
// shared fake JSON-RPC server, see solana_client_test.go).

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetBlock_RecordsRPCStats(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getBlock" {
			return 0, nil, jsonRPCResult(t, fixtureResult(t, "getBlock_confirmed_full.json"))
		}
		return 0, nil, getSlotResultBody(1000)
	})
	client := testClient(t, srv.URL)

	stats := newRPCStats()
	ctx := withRPCStats(context.Background(), stats)

	block, err := client.GetBlock(ctx, 397234561)
	require.NoError(t, err)
	require.NotNil(t, block)

	m, ok := stats.methods["GetBlock"]
	require.True(t, ok, "expected GetBlock stats")
	assert.Equal(t, 1, m.calls)
	assert.Equal(t, 0, m.failed)
	assert.True(t, m.net > 0, "network round-trip time should be recorded")
	assert.True(t, m.local > 0, "local conversion time should be recorded")
	assert.NotEmpty(t, stats.render())
}

func TestGetBlock_RecordsFailedRPCStats(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(string) (int, http.Header, string) {
		return 0, nil, jsonRPCErrorEnvelope(rpcCodeBlockNotAvailable, "block not available")
	})
	client := testClient(t, srv.URL)

	stats := newRPCStats()
	_, err := client.GetBlock(withRPCStats(context.Background(), stats), 999)
	require.Error(t, err)

	m, ok := stats.methods["GetBlock"]
	require.True(t, ok, "expected GetBlock stats")
	assert.Equal(t, 1, m.calls)
	assert.Equal(t, 1, m.failed)
}

func TestGetSlot_RecordsRPCStats(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(string) (int, http.Header, string) {
		return 0, nil, getSlotResultBody(1234)
	})
	client := testClient(t, srv.URL)

	stats := newRPCStats()
	slot, err := client.GetSlot(withRPCStats(context.Background(), stats))
	require.NoError(t, err)
	assert.Equal(t, uint64(1234), slot)

	m, ok := stats.methods["GetSlot"]
	require.True(t, ok, "expected GetSlot stats")
	assert.Equal(t, 1, m.calls)
	assert.Equal(t, 0, m.failed)
	assert.True(t, m.net > 0, "network round-trip time should be recorded")
	assert.Equal(t, time.Duration(0), m.local, "GetSlot has no local conversion")
}

func TestGetBlockFromArchive_RecordsRPCStats(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getBlock" {
			return 0, nil, jsonRPCResult(t, fixtureResult(t, "getBlock_confirmed_full.json"))
		}
		return 0, nil, getSlotResultBody(1000)
	})
	client := testClient(t, srv.URL, func(opts *ClientOptions) {
		opts.ArchiveRPCURL = srv.URL
	})

	stats := newRPCStats()
	block, err := client.GetBlockFromArchive(withRPCStats(context.Background(), stats), 397234561)
	require.NoError(t, err)
	require.NotNil(t, block)

	m, ok := stats.methods["GetBlockFromArchive"]
	require.True(t, ok, "expected GetBlockFromArchive stats")
	assert.Equal(t, 1, m.calls)
	assert.Equal(t, 0, m.failed)
	assert.True(t, m.net > 0, "network round-trip time should be recorded")
}

func TestGetBlockFromArchive_NotConfiguredRecordsNothing(t *testing.T) {
	t.Parallel()

	// No archive endpoint: the early return must not invent an observation.
	srv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getBlock" {
			return 0, nil, jsonRPCResult(t, fixtureResult(t, "getBlock_confirmed_full.json"))
		}
		return 0, nil, getSlotResultBody(1000)
	})
	client := testClient(t, srv.URL)

	stats := newRPCStats()
	_, err := client.GetBlockFromArchive(withRPCStats(context.Background(), stats), 397234561)
	require.ErrorIs(t, err, errArchiveNotConfigured)
	assert.Empty(t, stats.methods, "no RPC was issued, so nothing may be recorded")
}

// TestClient_NoStatsInContext verifies the nil-safe record path: calls made
// without a collector in the context must not panic.
func TestClient_NoStatsInContext(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getBlock" {
			return 0, nil, jsonRPCResult(t, fixtureResult(t, "getBlock_confirmed_full.json"))
		}
		return 0, nil, getSlotResultBody(1000)
	})
	client := testClient(t, srv.URL)

	assert.NotPanics(t, func() {
		block, err := client.GetBlock(context.Background(), 397234561)
		require.NoError(t, err)
		require.NotNil(t, block)

		slot, err := client.GetSlot(context.Background())
		require.NoError(t, err)
		assert.Equal(t, uint64(1000), slot)
	})
}
