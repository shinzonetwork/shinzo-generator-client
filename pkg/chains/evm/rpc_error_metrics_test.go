package evm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	ethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type codedRPCError int

func (e codedRPCError) Error() string  { return fmt.Sprintf("rpc error %d", int(e)) }
func (e codedRPCError) ErrorCode() int { return int(e) }

func TestRPCErrorType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"method not found", codedRPCError(jsonRPCMethodNotFound), "method_not_found"},
		{"wrapped method not found", fmt.Errorf("get receipts: %w", codedRPCError(jsonRPCMethodNotFound)), "method_not_found"},
		{"other rpc error", codedRPCError(-32000), "rpc_error"},
		{"http 429", ethrpc.HTTPError{StatusCode: http.StatusTooManyRequests}, "rate_limited"},
		{"http 503", ethrpc.HTTPError{StatusCode: http.StatusServiceUnavailable}, "http_error"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"socket deadline", os.ErrDeadlineExceeded, "timeout"},
		{"unknown", errors.New("boom"), "_OTHER"}, //nolint: err113
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, rpcErrorType(tt.err))
		})
	}
}

func TestCountRPCError_MethodNotFound(t *testing.T) {
	t.Parallel()
	// A node without eth_getBlockReceipts answers with JSON-RPC -32601.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"error":   map[string]any{"code": jsonRPCMethodNotFound, "message": "the method eth_getBlockReceipts does not exist/is not available"},
		})
	}))
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	reader := sdkmetric.NewManualReader()
	counter, err := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test").Int64Counter("rpc.errors")
	require.NoError(t, err)
	client.rpcErrors = counter

	_, err = client.GetBlockReceipts(t.Context(), big.NewInt(1))
	require.Error(t, err)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &rm))
	require.Len(t, rm.ScopeMetrics, 1)
	require.Len(t, rm.ScopeMetrics[0].Metrics, 1)
	sum, ok := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)

	dp := sum.DataPoints[0]
	assert.Equal(t, int64(1), dp.Value)
	assert.Equal(t, attribute.NewSet(
		attribute.String("method", "eth_getBlockReceipts"),
		attribute.String("error.type", "method_not_found"),
	), dp.Attributes)
}
