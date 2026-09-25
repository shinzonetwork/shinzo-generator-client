package evm

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- NewEthereumClient ---

func TestNewEthereumClient_HTTPOnly(t *testing.T) {
	t.Parallel()
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	assert.NotNil(t, client.httpClient)
	assert.Nil(t, client.wsClient)
	assert.Equal(t, server.URL, client.nodeURL)
}

func TestNewEthereumClient_WithAPIKey(t *testing.T) {
	t.Parallel()
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "test-api-key-12345", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	assert.NotNil(t, client.httpClient)
	assert.Equal(t, "test-api-key-12345", client.apiKey)
}

func TestNewEthereumClient_EndpointVariants(t *testing.T) {
	t.Parallel()
	server := simpleRPCServer()
	defer server.Close()

	const testAPIKey = "test-api-key-12345"
	cases := []struct {
		name       string
		httpURL    string
		wsURL      string
		apiKey     string
		wantErr    bool
		wantClient bool
		wantHTTP   bool
	}{
		{name: "InvalidHTTP", httpURL: "invalid-url", wantErr: true},
		{name: "InvalidHTTPWithAPIKey", httpURL: "invalid-url", apiKey: testAPIKey, wantErr: true},
		{
			name: "InvalidWebSocket_FallsBackToHTTP", httpURL: server.URL, wsURL: "ws://invalid-websocket-url:9999",
			wantClient: true, wantHTTP: true,
		},
		{
			name: "InvalidWS_WithAPIKey_FallsBackToHTTP", httpURL: server.URL, wsURL: "ws://invalid-ws:9999", apiKey: testAPIKey,
			wantClient: true,
		},
		{name: "NoEndpoints", wantErr: true},
		{name: "OnlyInvalidWS_NoHTTP", wsURL: "ws://invalid:9999", wantErr: true},
		{name: "OnlyInvalidWS_WithAPIKey_NoHTTP", wsURL: "ws://invalid:9999", apiKey: testAPIKey, wantErr: true},
		{
			name: "InvalidWS_NoAPIKey_FallsBackToHTTP", httpURL: server.URL, wsURL: "ws://invalid-ws-url:9999",
			// WS is invalid but HTTP works — should succeed with HTTP only
			wantClient: true, wantHTTP: true,
		},
		{
			// WS with API key fails completely and there's no HTTP fallback
			name: "InvalidWS_WithAPIKey_NoHTTP", wsURL: "ws://invalid:9999", apiKey: testAPIKey, wantErr: true,
		},
		{
			// WS URL contains "?" to exercise the "&key=" path in createWebSocketWithHeaders
			name: "InvalidWSWithQueryParam_WithAPIKey_FallsBackToHTTP", httpURL: server.URL, wsURL: "ws://invalid:9999?param=value", apiKey: testAPIKey,
			wantClient: true, wantHTTP: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, err := NewEthereumClient(t.Context(), tc.httpURL, tc.wsURL, tc.apiKey, "X-Api-Key", 0)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.wantClient {
				assert.NotNil(t, client)
			}
			if tc.wantHTTP {
				assert.NotNil(t, client.httpClient)
				assert.Nil(t, client.wsClient)
			}
		})
	}
}

// --- apiKeyTransport ---

func TestApiKeyTransport_RoundTrip_Success(t *testing.T) {
	t.Parallel()
	var receivedAPIKey string
	headerName := "X-Api-Key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAPIKey = r.Header.Get(headerName)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	defer server.Close()

	transport := &apiKeyTransport{ //nolint:gosec
		apiKey:       "my-api-key-1234567890",
		apiKeyHeader: "X-Api-Key",
		base:         http.DefaultTransport,
	}
	client := &http.Client{Transport: transport}
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, "my-api-key-1234567890", receivedAPIKey)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestApiKeyTransport_RoundTrip_Failure(t *testing.T) {
	t.Parallel()
	transport := &apiKeyTransport{
		apiKey:       "my-api-key-1234567890",
		apiKeyHeader: "X-Api-Key",
		base:         http.DefaultTransport,
	}
	client := &http.Client{Transport: transport}
	resp, err := client.Get("http://192.0.2.1:9999") // non-routable
	if resp != nil {
		_ = resp.Body.Close()
	}
	assert.Error(t, err)
}

// --- getPreferredClient ---

func TestGetPreferredClient_WSAvailable(t *testing.T) {
	t.Parallel()
	server := simpleRPCServer()
	defer server.Close()

	httpClient, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)

	// Simulate both clients
	client := &EthereumClient{
		httpClient: httpClient.httpClient,
		wsClient:   httpClient.httpClient, // reuse as "ws" for testing
	}
	result := client.getPreferredClient()
	assert.NotNil(t, result)
	assert.Equal(t, client.wsClient, result)
}

func TestGetPreferredClient_OnlyHTTP(t *testing.T) {
	t.Parallel()
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)

	result := client.getPreferredClient()
	assert.NotNil(t, result)
	assert.Equal(t, client.httpClient, result)
}

func TestGetPreferredClient_NoneAvailable(t *testing.T) {
	t.Parallel()
	client := &EthereumClient{}
	result := client.getPreferredClient()
	assert.Nil(t, result)
}

// --- Methods with nil client ---

func TestNilClient_Guards(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		call func(*EthereumClient) error
	}{
		{name: "GetNetworkID", call: func(c *EthereumClient) error { _, err := c.GetNetworkID(context.Background()); return err }},
		{name: "GetLatestBlockNumber", call: func(c *EthereumClient) error { _, err := c.GetLatestBlockNumber(context.Background()); return err }},
		{name: "GetLatestBlock", call: func(c *EthereumClient) error { _, err := c.GetLatestBlock(context.Background()); return err }},
		{name: "GetBlockByNumber", call: func(c *EthereumClient) error {
			_, err := c.GetBlockByNumber(context.Background(), big.NewInt(1))
			return err
		}},
		{name: "GetTransactionReceipt", call: func(c *EthereumClient) error {
			_, err := c.GetTransactionReceipt(context.Background(), "0xabc")
			return err
		}},
		{name: "GetBlockReceipts", call: func(c *EthereumClient) error {
			_, err := c.GetBlockReceipts(context.Background(), big.NewInt(1))
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &EthereumClient{}
			assert.Error(t, tc.call(client))
		})
	}
}

// --- GetFromAddress ---

func TestGetFromAddress(t *testing.T) {
	t.Parallel()
	tx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), []byte("data"))

	// Unsigned transaction - should fail gracefully
	addr, err := GetFromAddress(tx)
	// Either returns an address (from homestead/frontier recovery) or an error
	if err != nil {
		assert.Nil(t, addr)
	}
}

func TestGetFromAddress_SignedEIP155(t *testing.T) {
	t.Parallel()
	chainID := big.NewInt(1)
	key, expectedAddr := defaultTestKey()

	inner := &ethtypes.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       new(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}

	signer := ethtypes.NewEIP155Signer(chainID)
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	addr, err := GetFromAddress(tx)
	require.NoError(t, err)
	require.NotNil(t, addr)
	assert.Equal(t, expectedAddr, *addr)
}

func TestGetFromAddress_SignedDynamicFee(t *testing.T) {
	t.Parallel()
	chainID := big.NewInt(1)
	key, expectedAddr := defaultTestKey()

	inner := &ethtypes.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(1000000000),
		GasFeeCap: big.NewInt(2000000000),
		Gas:       21000,
		To:        new(common.HexToAddress("0xto")),
		Value:     big.NewInt(1000),
	}

	signer := ethtypes.NewLondonSigner(chainID)
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	addr, err := GetFromAddress(tx)
	require.NoError(t, err)
	require.NotNil(t, addr)
	assert.Equal(t, expectedAddr, *addr)
}

// --- Close ---

func TestClose_NilClients(t *testing.T) {
	t.Parallel()
	client := &EthereumClient{}
	err := client.Close()
	assert.NoError(t, err)
}

func TestClose_WithHTTPClient(t *testing.T) {
	t.Parallel()
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)

	err = client.Close()
	assert.NoError(t, err)
}

// --- Close with both clients ---

func TestClose_WithBothClients(t *testing.T) {
	t.Parallel()
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)

	// Set wsClient to a copy of httpClient for testing
	client.wsClient = client.httpClient

	err = client.Close()
	assert.NoError(t, err)
}

// --- GetFromAddress pre-EIP-155 (Homestead signer) ---

func TestGetFromAddress_HomesteadSigner(t *testing.T) {
	t.Parallel()
	key, expectedAddr := defaultTestKey()

	inner := &ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       new(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}

	// Sign with HomesteadSigner (pre-EIP-155, no chain ID)
	signer := ethtypes.HomesteadSigner{}
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	addr, err := GetFromAddress(tx)
	require.NoError(t, err)
	require.NotNil(t, addr)
	assert.Equal(t, expectedAddr, *addr)
}

func TestGetFromAddress_FrontierSigner(t *testing.T) {
	t.Parallel()
	key, expectedAddr := defaultTestKey()

	inner := &ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       new(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}

	// Sign with FrontierSigner
	signer := ethtypes.FrontierSigner{}
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	addr, err := GetFromAddress(tx)
	require.NoError(t, err)
	require.NotNil(t, addr)
	assert.Equal(t, expectedAddr, *addr)
}

// --- GetFromAddress all signers fail ---

func TestGetFromAddress_AllSignersFail(t *testing.T) {
	t.Parallel()
	// Create a DynamicFeeTx with a non-zero chain ID but completely invalid signature
	// so that all post-EIP-155 signers fail
	inner := &ethtypes.DynamicFeeTx{
		ChainID:   big.NewInt(1),
		Nonce:     0,
		GasTipCap: big.NewInt(1000000000),
		GasFeeCap: big.NewInt(2000000000),
		Gas:       21000,
		To:        new(common.HexToAddress("0xto")),
		Value:     big.NewInt(0),
	}
	// Create unsigned DynamicFeeTx — has non-zero ChainID but no valid signature
	tx := ethtypes.NewTx(inner)

	addr, err := GetFromAddress(tx)
	// Should try all post-EIP-155 signers and fail
	assert.Error(t, err)
	assert.Nil(t, addr)
	assert.Contains(t, err.Error(), "no sender")
}

// --- NewEthereumClient WebSocket without API key (invalid, falls back to HTTP) ---

// --- createWebSocketWithHeaders URL with existing query parameter ---

func TestCreateWebSocketWithHeaders_URLWithQueryParam(t *testing.T) {
	t.Parallel()
	// Test the branch where the WS URL already contains "?" (query string),
	// so the function appends with "&" instead of "?"
	_, err := createWebSocketWithHeaders(t.Context(), "ws://invalid-host:9999?existing=param", "test-api-key", "X-Api-Key")
	// Connection will fail, but we exercise the URL construction path with "&key=" and "&api_key="
	assert.Error(t, err)
}

func TestCreateWebSocketWithHeaders_URLWithoutQueryParam(t *testing.T) {
	t.Parallel()
	// Test the branch where the WS URL has no query string,
	// so the function appends with "?" for both key= and api_key=
	_, err := createWebSocketWithHeaders(t.Context(), "ws://invalid-host:9999", "test-api-key", "X-Api-Key")
	assert.Error(t, err)
}

func TestCreateWebSocketWithHeaders_Success(t *testing.T) {
	t.Parallel()
	// Start a real WebSocket server to exercise the success path
	server := newWSMockServer()
	defer server.Close()

	// Convert http://... to ws://...
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	client, err := createWebSocketWithHeaders(t.Context(), wsURL, "test-api-key", "X-Api-Key")
	require.NoError(t, err)
	require.NotNil(t, client)
	client.Close()
}

func TestCreateWebSocketWithHeaders_SuccessWithQueryParam(t *testing.T) {
	t.Parallel()
	// Test the success path when the URL already contains "?"
	server := newWSMockServer()
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?existing=param"

	client, err := createWebSocketWithHeaders(t.Context(), wsURL, "test-api-key", "X-Api-Key")
	require.NoError(t, err)
	require.NotNil(t, client)
	client.Close()
}

func TestNewEthereumClient_WSSuccess_WithAPIKey(t *testing.T) {
	t.Parallel()
	// HTTP server for the HTTP client
	httpServer := simpleRPCServer()
	defer httpServer.Close()

	// WebSocket server for the WS client
	wsServer := newWSMockServer()
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	client, err := NewEthereumClient(t.Context(), httpServer.URL, wsURL, "test-api-key-12345", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	assert.NotNil(t, client.httpClient)
	assert.NotNil(t, client.wsClient)
}

func TestNewEthereumClient_WSSuccess_NoAPIKey(t *testing.T) {
	t.Parallel()
	// HTTP server for the HTTP client
	httpServer := simpleRPCServer()
	defer httpServer.Close()

	// WebSocket server for the WS client
	wsServer := newWSMockServer()
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	client, err := NewEthereumClient(t.Context(), httpServer.URL, wsURL, "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	assert.NotNil(t, client.httpClient)
	assert.NotNil(t, client.wsClient)
}

func TestNewEthereumClient_WSFallback_WithAPIKey(t *testing.T) {
	t.Parallel()
	// Test the path where createWebSocketWithHeaders fails (rejects URLs with
	// query params) but the standard ethclient.Dial fallback succeeds.
	// This exercises lines 83-96 in NewEthereumClient.

	// This WS server rejects connections that have query parameters (simulating
	// a server that doesn't accept API key in query string), but accepts
	// plain WebSocket connections.
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			http.Error(w, "query params not allowed", http.StatusForbidden)
			return
		}
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req jsonRPCRequest
			if err := json.Unmarshal(msg, &req); err != nil {
				return
			}
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  "0x1",
			}
			respBytes, _ := json.Marshal(resp)
			if err := conn.WriteMessage(websocket.TextMessage, respBytes); err != nil {
				return
			}
		}
	}))
	defer wsServer.Close()

	httpServer := simpleRPCServer()
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	client, err := NewEthereumClient(t.Context(), httpServer.URL, wsURL, "test-api-key-12345", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	assert.NotNil(t, client.httpClient)
	// The WS client should be set via the fallback path
	assert.NotNil(t, client.wsClient)
}

// --- GetFromAddress pre-EIP-155 where both Homestead and Frontier fail ---

func TestGetFromAddress_PreEIP155_BothSignersFail(t *testing.T) {
	t.Parallel()
	// Create a legacy tx with V=27 (pre-EIP-155 marker) and R=0, S=0.
	// deriveChainId(27) returns 0, so ChainId().Sign() == 0, entering the pre-EIP-155 path.
	// Both HomesteadSigner and FrontierSigner will fail to recover a sender
	// because ecrecover cannot work with zero R,S values.
	inner := &ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       new(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
		V:        big.NewInt(27),
		R:        big.NewInt(0),
		S:        big.NewInt(0),
	}
	tx := ethtypes.NewTx(inner)

	addr, err := GetFromAddress(tx)
	assert.Error(t, err)
	assert.Nil(t, addr)
	assert.Contains(t, err.Error(), "pre-EIP-155")
}

// --- GetFromAddress FrontierSigner fallback (high-s value) ---

func TestGetFromAddress_FrontierSigner_HighS(t *testing.T) {
	t.Parallel()
	// Craft a pre-EIP-155 tx where HomesteadSigner rejects (s > secp256k1HalfN)
	// but FrontierSigner accepts. We sign normally, then flip s to s' = N - s
	// and adjust v, producing a valid but "non-canonical" signature.
	key, expectedAddr := defaultTestKey()

	inner := &ethtypes.LegacyTx{
		Nonce:    42,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       new(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}

	// Sign with FrontierSigner to get a valid pre-EIP-155 signature
	signer := ethtypes.FrontierSigner{}
	signedTx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	// Extract V, R, S
	v, r, s := signedTx.RawSignatureValues()

	// secp256k1 curve order N
	secp256k1N, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16)
	secp256k1HalfN := new(big.Int).Div(secp256k1N, big.NewInt(2))

	// If s is already > halfN, the original signature works. Otherwise flip it.
	newS := new(big.Int).Set(s)
	newV := new(big.Int).Set(v)
	if s.Cmp(secp256k1HalfN) <= 0 {
		// Flip s: s' = N - s
		newS.Sub(secp256k1N, s)
		// Flip recovery bit: 27 ↔ 28
		if v.Int64() == 27 {
			newV.SetInt64(28)
		} else {
			newV.SetInt64(27)
		}
	}

	// Create a new tx with the high-s signature
	highSTx := ethtypes.NewTx(&ethtypes.LegacyTx{
		Nonce:    42,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       new(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
		V:        newV,
		R:        r,
		S:        newS,
	})

	// Verify: HomesteadSigner should reject (s > halfN)
	_, homesteadErr := ethtypes.Sender(ethtypes.HomesteadSigner{}, highSTx)
	require.Error(t, homesteadErr, "HomesteadSigner should reject high-s signature")

	// Verify: FrontierSigner should accept
	from, frontierErr := ethtypes.Sender(ethtypes.FrontierSigner{}, highSTx)
	require.NoError(t, frontierErr, "FrontierSigner should accept high-s signature")
	assert.Equal(t, expectedAddr, from)

	// Now test GetFromAddress — it should succeed via the FrontierSigner fallback path
	addr, err := GetFromAddress(highSTx)
	require.NoError(t, err)
	require.NotNil(t, addr)
	assert.Equal(t, expectedAddr, *addr)
}

// --- normalizeHeaderName Tests ---

func TestNormalizeHeaderName(t *testing.T) {
	tests := []struct {
		name           string
		apiKeyType     string
		expectedHeader string
	}{
		{"x-goog-api-key lowercase", "x-goog-api-key", "x-goog-api-key"},
		{"X-Goog-Api-Key mixed case", "X-Goog-Api-Key", "x-goog-api-key"},
		{"x-api-key lowercase", "x-api-key", "x-api-key"},
		{"X-Api-Key mixed case", "X-Api-Key", "x-api-key"},
		{"custom header", "X-Custom-Header", "x-custom-header"},
		{"trims whitespace", "  X-Api-Key  ", "x-api-key"},
		{"empty string stays empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeHeaderName(tt.apiKeyType)
			assert.Equal(t, tt.expectedHeader, result)
		})
	}
}

// --- isGCPProvider Tests ---

func TestIsGCPProvider(t *testing.T) {
	tests := []struct {
		name       string
		headerName string
		expected   bool
	}{
		{"x-goog-api-key is GCP", "x-goog-api-key", true},
		{"contains goog anywhere", "my-goog-header", true},
		{"x-api-key is not GCP", "x-api-key", false},
		{"empty is not GCP", "", false},
		{"unknown is not GCP", "unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isGCPProvider(tt.headerName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// --- NewEthereumClient context-driven dial behaviour ---

func TestNewEthereumClient_CancelledContext_FailsFast(t *testing.T) {
	t.Parallel()
	server := simpleRPCServer()
	defer server.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	client, err := NewEthereumClient(ctx, server.URL, "", "", "X-Api-Key", 0)
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestNewEthereumClient_WSBlackHole_DeadlineAbortsDial(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		apiKey string
		header string // "x-goog-api-key" exercises the GCP query-param dial path
	}{
		{name: "NoAPIKey", apiKey: "", header: "X-Api-Key"},
		{name: "WithAPIKeyHeader", apiKey: "test-api-key-12345", header: "X-Api-Key"},
		{name: "WithGCPQueryParam", apiKey: "test-api-key-12345", header: "x-goog-api-key"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wsServer := newHangingWSServer()
			defer wsServer.Close()
			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()

			start := time.Now()
			client, err := NewEthereumClient(ctx, "", wsURL, tc.apiKey, tc.header, 0)
			elapsed := time.Since(start)

			assert.Error(t, err)
			assert.Nil(t, client)
			assertDeadlineError(t, err)
			assert.ErrorIs(t, err, errWSDialAborted)
			assert.Less(t, elapsed, 5*time.Second, "dial must abort on the context deadline, not on OS-level timeouts")
		})
	}
}

func TestNewEthereumClient_DialTimeoutBoundsBlackHole(t *testing.T) {
	t.Parallel()
	wsServer := newHangingWSServer()
	defer wsServer.Close()
	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	// No caller deadline: the dialTimeout parameter alone must bound the dial.
	start := time.Now()
	client, err := NewEthereumClient(t.Context(), "", wsURL, "", "X-Api-Key", 300*time.Millisecond)
	elapsed := time.Since(start)

	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Less(t, elapsed, 5*time.Second, "dialTimeout must bound the dial without a caller deadline")
}

func TestNewEthereumClient_NonPositiveDialTimeout_IsUnbounded(t *testing.T) {
	t.Parallel()
	server := simpleRPCServer()
	defer server.Close()

	for _, dialTimeout := range []time.Duration{0, -1} {
		client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", dialTimeout)
		require.NoError(t, err)
		require.NotNil(t, client)
		assert.NotNil(t, client.httpClient)
		require.NoError(t, client.Close())
	}
}

func TestNewEthereumClient_WSTimeout_DegradesToHTTPWhenConnected(t *testing.T) {
	t.Parallel()

	// Regression: the WS dial used to share one timeout budget with the
	// whole dial sequence, and a WS-phase timeout failed the constructor
	// regardless of the connected HTTP client. The WS phase now owns a
	// freshly derived budget, and its expiry with the caller's context
	// alive must degrade to HTTP-only startup instead of failing.

	cases := []struct {
		name   string
		apiKey string
		header string
	}{
		{name: "NoAPIKey", apiKey: "", header: "X-Api-Key"},
		{name: "WithAPIKeyHeader", apiKey: "test-api-key-12345", header: "X-Api-Key"},
		{name: "WithGCPQueryParam", apiKey: "test-api-key-12345", header: "x-goog-api-key"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			httpServer := simpleRPCServer()
			defer httpServer.Close()
			wsServer := newHangingWSServer()
			defer wsServer.Close()
			wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

			// No caller deadline: dialTimeout bounds only the WS phase.
			start := time.Now()
			client, err := NewEthereumClient(t.Context(), httpServer.URL, wsURL, tc.apiKey, tc.header, 300*time.Millisecond)
			elapsed := time.Since(start)

			require.NoError(t, err)
			require.NotNil(t, client)
			require.NoError(t, client.Close())
			assert.NotNil(t, client.httpClient)
			assert.Nil(t, client.wsClient, "WS dial timed out, so the client must start HTTP-only")
			assert.GreaterOrEqual(t, elapsed, 250*time.Millisecond,
				"the WS-phase budget, not the instant HTTP dial, must bound the hanging handshake")
			assert.Less(t, elapsed, 5*time.Second, "WS timeout must degrade, not hang or fail the constructor")
		})
	}
}

func TestNewEthereumClient_ParentDeadlineMidWSDial_FailsFast(t *testing.T) {
	t.Parallel()
	httpServer := simpleRPCServer()
	defer httpServer.Close()
	wsServer := newHangingWSServer()
	defer wsServer.Close()
	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	// The caller's deadline dies mid-WS-dial while the WS-phase budget
	// (10s) would still be running: caller-context death must always fail
	// the constructor, never degrade to a half-started HTTP-only client.
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	client, err := NewEthereumClient(ctx, httpServer.URL, wsURL, "", "X-Api-Key", 10*time.Second)
	elapsed := time.Since(start)

	assert.Error(t, err)
	assert.Nil(t, client)
	assertDeadlineError(t, err)
	assert.ErrorIs(t, err, errWSDialAborted)
	assert.Less(t, elapsed, 5*time.Second, "the caller deadline must abort startup even with HTTP connected")
}

func TestEthereumClient_NilClientGuard_ReturnsSentinel(t *testing.T) {
	t.Parallel()
	// Both transports unset: every getter's nil-client guard classifies via
	// the errNoClientAvailable sentinel.
	c := &EthereumClient{}

	_, err := c.GetLatestBlock(t.Context())
	assert.ErrorIs(t, err, errNoClientAvailable)

	_, err = c.GetLatestBlockNumber(t.Context())
	assert.ErrorIs(t, err, errNoClientAvailable)
}
