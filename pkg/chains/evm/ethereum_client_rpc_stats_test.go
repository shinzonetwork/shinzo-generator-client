package evm

// Tests that the ctx-carried rpcStats collector records per-method network
// round-trip and local conversion time from the EthereumClient methods
// (against the shared mock JSON-RPC server, see helpers_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetBlockByNumber_RecordsRPCStats(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			return fullBlockResponse(testBlockNumberHex, nil), nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	stats := newRPCStats()
	ctx := withRPCStats(context.Background(), stats)

	_, err = client.GetBlockByNumber(ctx, big.NewInt(testBlockNumber))
	require.NoError(t, err)

	m, ok := stats.methods["GetBlockByNumber"]
	if !ok {
		t.Fatal("expected GetBlockByNumber stats")
	}
	assert.Equal(t, 1, m.calls)
	assert.Equal(t, 0, m.failed)
	assert.True(t, m.net > 0, "network round-trip time should be recorded")
	assert.True(t, m.local > 0, "local conversion time should be recorded")
	assert.NotEmpty(t, stats.render())
}

func TestGetBlockByNumber_RecordsFailedRPCStats(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		return nil, fmt.Errorf("block not found")
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	stats := newRPCStats()
	ctx := withRPCStats(context.Background(), stats)

	_, err = client.GetBlockByNumber(ctx, big.NewInt(999999))
	require.Error(t, err)

	m, ok := stats.methods["GetBlockByNumber"]
	if !ok {
		t.Fatal("expected GetBlockByNumber stats")
	}
	assert.Equal(t, 1, m.calls)
	assert.Equal(t, 1, m.failed)
}

func TestGetLatestBlockNumber_RecordsRPCStats(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		return "0x1", nil
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	stats := newRPCStats()
	ctx := withRPCStats(context.Background(), stats)

	_, err = client.GetLatestBlockNumber(ctx)
	require.NoError(t, err)

	m, ok := stats.methods["GetLatestBlockNumber"]
	if !ok {
		t.Fatal("expected GetLatestBlockNumber stats")
	}
	assert.Equal(t, 1, m.calls)
	assert.Equal(t, 0, m.failed)
	assert.True(t, m.net > 0, "network round-trip time should be recorded")
}

// TestGetBlockByNumber_NoStatsInContext verifies the nil-safe record path:
// calls made without a collector in the context must not panic.
func TestGetBlockByNumber_NoStatsInContext(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			return fullBlockResponse(testBlockNumberHex, nil), nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	assert.NotPanics(t, func() {
		_, err := client.GetBlockByNumber(t.Context(), big.NewInt(testBlockNumber))
		require.NoError(t, err)
	})
}
