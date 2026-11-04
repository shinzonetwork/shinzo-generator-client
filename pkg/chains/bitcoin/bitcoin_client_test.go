package bitcoin

// Client tests, driven by the mock Bitcoin Core server: request shapes on
// the wire, auth paths, error classification, no-retry semantics, metric
// gatings, archive routing, context pre-checks and shutdown.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/shinzonetwork/shinzo-generator-client/config"
)

// Tag literals keep the canned payloads self-consistent and readable:
// testBlockHash is a synthetic 64-hex hash chainhash accepts while the
// parent and merkle constants bridge the canned payloads to each other.
const (
	testBlockHash   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testBlockParent = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testBlockMerkle = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testBlockTime   = int64(1349355010)
)

// serveErr answers one JSON-RPC error object, served with Core's
// RPC-failure convention: HTTP status 500 plus a body.
func serveErr(code int, message string) mockResponse {
	return mockResponse{rpcErr: &mockRPCError{Code: code, Message: message}}
}

// testBlock is a canned verbosity-3 payload: a coinbase transaction paying
// 50.2207 BTC and one witness transaction spending an 857.05 BTC prevout.
func testBlock() Block {
	fee := float64(0.0061)
	return Block{
		Hash:              testBlockHash,
		Confirmations:     2,
		StrippedSize:      1000,
		Size:              2000,
		Weight:            4000,
		Height:            184498,
		Version:           2,
		MerkleRoot:        testBlockMerkle,
		Time:              testBlockTime,
		Nonce:             2642787427,
		Bits:              "1a0facdc",
		Chainwork:         "0000000000000000000000000000000000000000000000012b621dea8f398887",
		PreviousBlockHash: testBlockParent,
		Tx: []Tx{
			{
				TxID:     "cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01",
				Hash:     "cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01",
				Version:  1,
				LockTime: 0,
				Vin: []Vin{{
					Coinbase: "046e482c1c0106",
					Sequence: 4294967295,
				}},
				Vout: []Vout{{
					Index: 0,
					Value: 50.2207,
					ScriptPubKey: ScriptPubKey{
						Hex:  "76a91481b02c27b21004aeb4df5265dd90d4d56f56f84a88ac",
						Type: "pubkeyhash",
					},
				}},
			},
			{
				TxID:     "d001d001d001d001d001d001d001d001d001d001d001d001d001d001d001d001",
				Hash:     "d002d002d002d002d002d002d002d002d002d002d002d002d002d002d002d002",
				Version:  1,
				LockTime: 0,
				Vin: []Vin{{
					TxID:        "e000e000e000e000e000e000e000e000e000e000e000e000e000e000e000e000",
					Vout:        new(uint32(0)),
					ScriptSig:   &ScriptSig{Hex: "00"},
					Sequence:    4294967295,
					TxInWitness: []string{"aa", "bb"},
					Prevout: &PrevOut{
						Generated: false,
						Height:    184497,
						Value:     857.05,
						ScriptPubKey: ScriptPubKey{
							Hex:     "0014abcdef",
							Address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080",
							Type:    "witness_v0_keyhash",
						},
					},
				}},
				Vout: []Vout{{
					Index: 0,
					Value: 857.0439,
					ScriptPubKey: ScriptPubKey{
						Hex:  "76a91481b02c27b21004aeb4df5265dd90d4d56f56f56f84a88ac",
						Type: "pubkeyhash",
					},
				}},
				Fee: &fee,
			},
		},
	}
}

// ptrUint32 is a literal for the Vin.Vout pointer: Core omits the field for
// coinbase inputs, so only regular inputs carry a non-nil zero here.
//
//go:fix inline
func ptrUint32(v uint32) *uint32 {
	return new(v)
}

// firstCall returns the mock server's first recorded request, copied under
// the recording lock so the test's read cannot race the handler goroutine.
func firstCall(t *testing.T, server *coreRPCServer) recordedCall {
	t.Helper()
	calls := server.recordedCalls()
	require.NotEmpty(t, calls)
	return calls[0]
}

func TestNewClient_Guards(t *testing.T) {
	t.Parallel()

	_, err := NewClient(config.BitcoinConfig{RPCUser: "u", RPCPassword: "p"})
	assert.ErrorContains(t, err, "rpc_url is empty")

	_, err = NewClient(config.BitcoinConfig{RPCURL: "http://127.0.0.1:8332"})
	assert.ErrorContains(t, err, "basic auth")
}

func TestNewClient_ResolveDialTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		configured int
		want       time.Duration
	}{
		{"zero resolves the deployment default", 0, 10 * time.Second},
		{"negative resolves the deployment default", -3, 10 * time.Second},
		{"configured seconds pass through", 37, 37 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig()
			cfg.RPCURL = "http://127.0.0.1:8332"
			cfg.DialTimeoutSeconds = tt.configured
			client := newTestClient(t, cfg)
			assert.Equal(t, tt.want, client.dialTimeout)
		})
	}
}

func TestGetBlockCount_SuccessAndShape(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(call recordedCall) mockResponse {
		if call.Method == rpcMethodGetBlockCount {
			return mockResponse{result: json.RawMessage("184500")}
		}
		return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "unscripted method")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	count, err := client.GetBlockCount(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(184500), count)

	assert.Equal(t, 1, server.callCount(rpcMethodGetBlockCount),
		"exactly one request; rpcclient retries connection establishment only, never RPC outcomes")
}

func TestGetBlockHash_SuccessAndShape(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(call recordedCall) mockResponse {
		if call.Method == rpcMethodGetBlockHash {
			return mockResponse{result: json.RawMessage(`"` + testBlockHash + `"`)}
		}
		return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "unscripted method")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	hash, err := client.GetBlockHash(t.Context(), 184498)
	require.NoError(t, err)
	assert.Equal(t, testBlockHash, hash)

	params := unmarshalParams(t, firstCall(t, server))
	require.Len(t, params, 1)
	assert.Equal(t, float64(184498), params[0])
}

func TestGetBlockVerbosity3_ShapeAndDecode(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(call recordedCall) mockResponse {
		if call.Method == rpcMethodGetBlock {
			return mockBlockAt(t, testBlock())
		}
		return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "unscripted method")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	block, err := client.GetBlockVerbosity3(t.Context(), testBlockHash)
	require.NoError(t, err)
	require.NotNil(t, block)
	assert.Equal(t, testBlock(), *block)

	call := firstCall(t, server)
	assert.Equal(t, rpcMethodGetBlock, call.Method)
	params := unmarshalParams(t, call)
	require.Len(t, params, 2)
	assert.Equal(t, testBlockHash, params[0])
	assert.Equal(t, float64(blockVerbosityUndoData), params[1], "verbosity 3 travels literally on the wire")
}

func TestGetBlockHeaderByHash_ShapeAndMapping(t *testing.T) {
	t.Parallel()
	header := map[string]any{
		"hash":              testBlockHash,
		"confirmations":     2,
		"height":            184498,
		"version":           2,
		"merkleroot":        testBlockMerkle,
		"time":              testBlockTime,
		"nonce":             2642787427,
		"bits":              "1a0facdc",
		"difficulty":        1234.5,
		"previousblockhash": testBlockParent,
		"nextblockhash":     "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}
	server := newCoreRPCServer(t, func(call recordedCall) mockResponse {
		if call.Method == rpcMethodGetBlockHeader {
			return mockResponse{result: mustJSONMarshal(t, header)}
		}
		return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "unscripted method")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	mapped, err := client.GetBlockHeaderByHash(t.Context(), testBlockHash)
	require.NoError(t, err)
	require.NotNil(t, mapped)
	assert.Equal(t, testBlockHash, mapped.Hash)
	assert.Equal(t, int64(184498), mapped.Height)
	assert.Equal(t, uint32(2), mapped.Version)
	assert.Equal(t, testBlockMerkle, mapped.MerkleRoot)
	assert.Equal(t, testBlockParent, mapped.PreviousBlockHash)
	assert.Equal(t, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", mapped.NextBlockHash)

	call := firstCall(t, server)
	assert.Equal(t, rpcMethodGetBlockHeader, call.Method)
	params := unmarshalParams(t, call)
	require.Len(t, params, 2)
	assert.Equal(t, testBlockHash, params[0])
	assert.Equal(t, true, params[1], "verbose header travels as a JSON true")

	_, err = client.GetBlockHeaderByHash(t.Context(), "not-a-hash")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not found", "a malformed hash is a caller bug, never a not-found")
}

func TestGetChainInfo_MapsArchiveFloor(t *testing.T) {
	t.Parallel()
	// The library's typed call probes the backend version on first use:
	// getinfo misses on Core, getnetworkinfo answers, and
	// getblockchaininfo carries the archive floor. The mock scripts all
	// three exactly as Bitcoin Core would serve them.
	chainInfo := map[string]any{
		"chain":         "main",
		"blocks":        800000,
		"headers":       800000,
		"bestblockhash": testBlockHash,
		"pruned":        true,
		"pruneheight":   300000,
		// Core 27 serves softforks as an object keyed by fork name, which is
		// the form btcjson's unified-format branch expects; the array shape
		// belongs to pre-0.19 backends.
		"softforks": map[string]any{
			"segwit": map[string]any{"type": "buried", "height": 481824, "active": true},
		},
	}
	networkInfo := map[string]any{
		"version":         270000,
		"subversion":      "/Satoshi:27.0.0/",
		"protocolversion": 70016,
		"relayfee":        0.00001,
	}
	server := newCoreRPCServer(t, func(call recordedCall) mockResponse {
		switch call.Method {
		case rpcMethodGetBlockchainInfo:
			return mockResponse{result: mustJSONMarshal(t, chainInfo)}
		case "getinfo":
			return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "Method not found")
		case "getnetworkinfo":
			return mockResponse{result: mustJSONMarshal(t, networkInfo)}
		default:
			return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "unscripted method")
		}
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	info, err := client.GetChainInfo(t.Context())
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, "main", info.Chain)
	assert.Equal(t, int64(800000), info.Blocks)
	assert.Equal(t, int64(800000), info.Headers)
	assert.Equal(t, testBlockHash, info.BestBlockHash)
	assert.True(t, info.Pruned)
	assert.Equal(t, int64(300000), info.PruneHeight)
}

func TestNewClient_BasicAuthHeader(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return mockResponse{result: json.RawMessage("184500")}
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	_, err := client.GetBlockCount(t.Context())
	require.NoError(t, err)

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("testrpcuser:testrpcpass"))
	assert.Equal(t, want, firstCall(t, server).Header.Get("Authorization"))
}

func TestNewClient_CookieAuthHeader(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return mockResponse{result: json.RawMessage("184500")}
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	cfg.RPCUser = ""
	cfg.RPCPassword = ""
	cfg.CookiePath = t.TempDir() + "/.cookie"
	require.NoError(t, os.WriteFile(cfg.CookiePath, []byte("cookieuser:cookiepass"), 0o600))
	client := newTestClient(t, cfg)

	_, err := client.GetBlockCount(t.Context())
	require.NoError(t, err)

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("cookieuser:cookiepass"))
	assert.Equal(t, want, firstCall(t, server).Header.Get("Authorization"))
}

func TestNewClient_APIKeyHeaderDefaultType(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return mockResponse{result: json.RawMessage("184500")}
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	cfg.APIKey = "sk-test-key"
	client := newTestClient(t, cfg)

	_, err := client.GetBlockCount(t.Context())
	require.NoError(t, err)

	call := firstCall(t, server)
	assert.Equal(t, "sk-test-key", call.Header.Get("X-Api-Key"),
		"an unset api_key_type falls back to the default header name")
	assert.NotEmpty(t, call.Header.Get("Authorization"),
		"basic auth stays configured alongside the api-key header")
}

func TestNewClient_APIKeyHeaderCustomType(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return mockResponse{result: json.RawMessage("184500")}
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	cfg.APIKey = "sk-test-key"
	cfg.APIKeyType = "X-Provider-Key"
	client := newTestClient(t, cfg)

	_, err := client.GetBlockCount(t.Context())
	require.NoError(t, err)

	call := firstCall(t, server)
	assert.Equal(t, "sk-test-key", call.Header.Get("X-Provider-Key"),
		"a configured api_key_type becomes the lowercased header name")
	assert.Empty(t, call.Header.Get("X-Api-Key"))
}

func TestGetBlockVerbosity3_UndoDataUnavailableSentinel(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return serveErr(int(btcjson.ErrRPCBlockNotFound), "Block not available (undo data is not available)")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	_, err := client.GetBlockVerbosity3(t.Context(), testBlockHash)
	require.Error(t, err)
	require.ErrorIs(t, err, errUndoDataUnavailable)
	assert.Contains(t, err.Error(), "not available")
	assert.Equal(t, 1, server.callCount(rpcMethodGetBlock),
		"an RPC error surfaces on the first attempt: no in-client retry")
}

func TestGetBlockVerbosity3_UnknownHashKeepsNotFound(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return serveErr(int(btcjson.ErrRPCBlockNotFound), "Block not found")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	_, err := client.GetBlockVerbosity3(t.Context(), testBlockHash)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errUndoDataUnavailable, "message text keeps the two -5 variants distinct")
	var rpcErr *btcjson.RPCError
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, btcjson.ErrRPCBlockNotFound, rpcErr.Code)
	assert.Contains(t, err.Error(), "not found")
}

func TestGetBlockHash_HeightOutOfRangePassthrough(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return serveErr(int(btcjson.ErrRPCInvalidParameter), "Block height out of range")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	_, err := client.GetBlockHash(t.Context(), 999999999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
	assert.Equal(t, 1, server.callCount(rpcMethodGetBlockHash),
		"Core's height-out-of-range answer is a single request, never retried in-client")
}

func TestGetBlockCount_NonJSONBodyClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		status   int
		body     string
		wantType string
	}{
		{"429 counts as rate limited", http.StatusTooManyRequests, "<html>slow down</html>", "rate_limited"},
		{"503 counts as an http error", http.StatusServiceUnavailable, "<html>oops</html>", "http_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
				return mockResponse{httpStatus: tt.status, httpBody: tt.body}
			})
			cfg := testConfig()
			cfg.RPCURL = server.URL
			client := newTestClient(t, cfg)

			_, err := client.GetBlockCount(t.Context())
			require.Error(t, err)
			var statusErr *httpStatusError
			require.ErrorAs(t, err, &statusErr, "non-JSON-RPC transport answers get a typed error")
			assert.Equal(t, tt.status, statusErr.statusCode)
			assert.Equal(t, tt.wantType, rpcErrorType(err))
		})
	}
}

func TestRPCErrorType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"method not found", &btcjson.RPCError{Code: btcjson.ErrRPCMethodNotFound.Code, Message: "Method not found"}, "method_not_found"},
		{"wrapped method not found", errors.Join(errors.New("wrap"), &btcjson.RPCError{Code: btcjson.ErrRPCMethodNotFound.Code}), "method_not_found"},
		{"block not found", &btcjson.RPCError{Code: btcjson.ErrRPCBlockNotFound, Message: "Block not found"}, "block_not_found"},
		{"undo data rides the -5 bucket", &undoDataRPCError{rpcErr: &btcjson.RPCError{Code: btcjson.ErrRPCBlockNotFound, Message: "Block not available"}}, "block_not_found"},
		{"height out of range", &btcjson.RPCError{Code: btcjson.ErrRPCInvalidParameter, Message: "Block height out of range"}, "height_out_of_range"},
		{"other rpc code", &btcjson.RPCError{Code: -32000, Message: "boom"}, "rpc_error"},
		{"http 429", &httpStatusError{statusCode: http.StatusTooManyRequests, cause: errors.New("status code: 429, response: \"<html>\"")}, "rate_limited"},
		{"http 503", &httpStatusError{statusCode: http.StatusServiceUnavailable, cause: errors.New("status code: 503, response: \"<html>\"")}, "http_error"},
		{"raw url error stays other", &url.Error{Op: "Post", URL: "http://127.0.0.1:8332", Err: errors.New("connection refused")}, "_OTHER"},
		{"context deadline", context.DeadlineExceeded, "timeout"},
		{"context canceled", context.Canceled, "timeout"},
		{"unknown", errors.New("boom"), "_OTHER"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, rpcErrorType(tt.err))
		})
	}
}

// countTestClient attaches a manual-reader-backed counter to a client so
// metric assertions can inspect exactly what one call recorded.
func countTestClient(t *testing.T, server *coreRPCServer) (*Client, *sdkmetric.ManualReader) {
	t.Helper()
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)
	reader := sdkmetric.NewManualReader()
	counter, counterErr := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).
		Meter("test").Int64Counter("shinzo.generator.rpc.errors")
	require.NoError(t, counterErr)
	client.rpcErrors = counter
	return client, reader
}

// collectSumPoints flattens the recorded counter's data points; an empty
// result means nothing at all was counted.
func collectSumPoints(t *testing.T, reader *sdkmetric.ManualReader) []metricdata.DataPoint[int64] {
	t.Helper()
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &collected))
	if len(collected.ScopeMetrics) == 0 {
		return nil
	}
	require.Len(t, collected.ScopeMetrics, 1)
	require.Len(t, collected.ScopeMetrics[0].Metrics, 1)
	sum, ok := collected.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64])
	require.True(t, ok)
	return sum.DataPoints
}

func TestCountRPCError_UndoDataCountsAsArchiveFloorSignal(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return serveErr(int(btcjson.ErrRPCBlockNotFound), "Block not available (undo data is not available)")
	})
	client, reader := countTestClient(t, server)

	_, err := client.GetBlockVerbosity3(t.Context(), testBlockHash)
	require.Error(t, err)

	points := collectSumPoints(t, reader)
	require.Len(t, points, 1)
	assert.Equal(t, int64(1), points[0].Value)
	assert.Equal(t, attribute.NewSet(
		attribute.String("rpc.method", rpcMethodGetBlock),
		attribute.String("error.type", "block_not_found"),
	), points[0].Attributes)
}

func TestCountRPCError_HeightOutOfRangeSkipped(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return serveErr(int(btcjson.ErrRPCInvalidParameter), "Block height out of range")
	})
	client, reader := countTestClient(t, server)

	_, err := client.GetBlockHash(t.Context(), 999999999)
	require.Error(t, err)

	assert.Empty(t, collectSumPoints(t, reader),
		"heights past the tip are the routine wait-for-tip outcome, not counted")
	assert.Equal(t, 1, server.callCount(rpcMethodGetBlockHash))
}

func TestCountRPCError_ContextCanceledSkipped(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return mockResponse{result: json.RawMessage("184500")}
	})
	client, reader := countTestClient(t, server)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.GetBlockCount(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)

	assert.Empty(t, collectSumPoints(t, reader), "caller-aborted calls never touch the wire or the counter")
	assert.Equal(t, 0, server.callCount(rpcMethodGetBlockCount))
}

func TestArchiveRouting(t *testing.T) {
	t.Parallel()
	primary := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return serveErr(int(btcjson.ErrRPCBlockNotFound), "Block not available (undo data is not available)")
	})
	archive := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return mockBlockAt(t, testBlock())
	})
	cfg := testConfig()
	cfg.RPCURL = primary.URL
	cfg.ArchiveRPCURL = archive.URL
	client := newTestClient(t, cfg)
	require.True(t, client.ArchiveConfigured())

	_, err := client.GetBlockVerbosity3(t.Context(), testBlockHash)
	require.ErrorIs(t, err, errUndoDataUnavailable)

	fallback, err := client.GetBlockVerbosity3FromArchive(t.Context(), testBlockHash)
	require.NoError(t, err)
	require.NotNil(t, fallback)
	assert.Equal(t, testBlockHash, fallback.Hash)

	assert.Equal(t, 1, primary.callCount(rpcMethodGetBlock), "the primary client serves the first attempt")
	assert.Equal(t, 1, archive.callCount(rpcMethodGetBlock), "the archive endpoint serves the fallback")
}

func TestArchiveNotConfigured(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return serveErr(int(btcjson.ErrRPCBlockNotFound), "Block not available (undo data is not available)")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)
	require.False(t, client.ArchiveConfigured())

	_, err := client.GetBlockVerbosity3FromArchive(t.Context(), testBlockHash)
	require.Error(t, err)
	assert.ErrorIs(t, err, errArchiveNotConfigured)
	assert.Contains(t, err.Error(), "BITCOIN_ARCHIVE_RPC_URL")
}

func TestMethodContextsArePrechecked(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return mockResponse{result: json.RawMessage("184500")}
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.GetBlockCount(ctx)
	require.ErrorIs(t, err, context.Canceled)
	_, err = client.GetBlockHash(ctx, 1)
	require.ErrorIs(t, err, context.Canceled)
	_, err = client.GetBlockHeaderByHash(ctx, testBlockHash)
	require.ErrorIs(t, err, context.Canceled)
	_, err = client.GetChainInfo(ctx)
	require.ErrorIs(t, err, context.Canceled)
	_, err = client.GetBlockVerbosity3(ctx, testBlockHash)
	require.ErrorIs(t, err, context.Canceled)

	assert.Empty(t, server.recordedCalls(), "a dead context reaches no endpoint")
}

func TestClose_ShutsDownBothClients(t *testing.T) {
	t.Parallel()
	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return mockResponse{result: json.RawMessage("184500")}
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	cfg.ArchiveRPCURL = server.URL
	client := newTestClient(t, cfg)

	require.NoError(t, client.Close())
	require.NoError(t, client.Close(), "shutdown is idempotent")

	_, err := client.GetBlockCount(t.Context())
	require.Error(t, err)
	require.ErrorIs(t, err, rpcclient.ErrClientShutdown,
		"a closed client fails fast instead of hanging on the wire")
}

func TestFixtureRoundTripThroughRawRequest(t *testing.T) {
	fixture := loadFixture(t)
	server := newCoreRPCServer(t, func(call recordedCall) mockResponse {
		if call.Method == rpcMethodGetBlock {
			return mockResponse{rawBody: serveFixtureEnvelope(fixture)}
		}
		return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "unscripted method")
	})
	cfg := testConfig()
	cfg.RPCURL = server.URL
	client := newTestClient(t, cfg)

	raw, err := client.rawGetBlockVerbosity3(t.Context(), client.client, testBlockHash)
	require.NoError(t, err)
	require.True(t, bytes.Equal(raw, fixture),
		"the verbosity-3 envelope passes through RawRequest untouched, byte for byte")
	assert.Equal(t, rpcMethodGetBlock, firstCall(t, server).Method)

	block, err := client.getBlockVerbosity3(t.Context(), client.client, testBlockHash)
	require.NoError(t, err)

	// Header shape.
	assert.Equal(t, int64(184498), block.Height)
	assert.Len(t, block.Hash, 64)
	assert.NotEmpty(t, block.PreviousBlockHash)
	assert.NotEmpty(t, block.MerkleRoot)
	assert.NotEmpty(t, block.Bits)
	assert.NotEmpty(t, block.Chainwork)
	assert.Positive(t, block.Time)
	assert.Positive(t, block.Nonce)

	require.Len(t, block.Tx, 327, "real mainnet block 184498 carries 327 transactions")

	// The coinbase transaction: no fee, no prevout, and its outputs sum to
	// the pinned 50.2207 BTC (50 subsidy plus the block's fees).
	var coinbase *Tx
	for idx := range block.Tx {
		if block.Tx[idx].Vin[0].Coinbase != "" {
			coinbase = &block.Tx[idx]
			break
		}
	}
	require.NotNil(t, coinbase, "the block holds a coinbase")
	assert.Nil(t, coinbase.Fee)
	assert.Nil(t, coinbase.Vin[0].Prevout)
	coinbaseSats := int64(0)
	for _, vout := range coinbase.Vout {
		sats, convErr := btcToSatoshis(vout.Value)
		require.NoError(t, convErr)
		coinbaseSats += sats
	}
	assert.Equal(t, int64(5022070000), coinbaseSats, "the coinbase pays exactly 50.2207 BTC in satoshis")

	// The pinned prevout: 857.05 BTC exactly, converted sat-exactly.
	prevoutFound := false
	for _, tx := range block.Tx {
		if tx.Vin[0].Prevout == nil {
			continue
		}
		if tx.Vin[0].Prevout.Value == 857.05 {
			prevoutFound = true
			sats, convErr := btcToSatoshis(tx.Vin[0].Prevout.Value)
			require.NoError(t, convErr)
			assert.Equal(t, int64(85705000000), sats)
		}
	}
	require.True(t, prevoutFound, "the fixture holds an 857.05 BTC prevout as pinned")

	// A witness transaction exists: its wtxid differs from the txid and its
	// input carries witness entries.
	witnessSeen := false
	for _, tx := range block.Tx {
		if tx.Hash != tx.TxID && len(tx.Vin) > 0 && len(tx.Vin[0].TxInWitness) > 0 {
			witnessSeen = true
			break
		}
	}
	assert.True(t, witnessSeen, "the SegWit payload keeps wtxid and txid apart")
}
