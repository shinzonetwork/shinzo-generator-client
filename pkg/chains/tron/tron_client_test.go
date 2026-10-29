package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeResponse bundles what one wallet-API fake serves.
type fakeResponse struct {
	status int
	data   string
}

// capture records the last request a fake served. Assertions must read the
// capture after the synchronous client call completes, never inside the
// handler goroutine.
type capture struct {
	mu     sync.Mutex
	path   string
	apiKey string
	body   string
}

// handler builds a walletFakeHandler serving resp while recording the
// request.
func (c *capture) handler(resp fakeResponse) walletFakeHandler {
	return func(path, apiKey string, body []byte) (int, []byte) {
		c.mu.Lock()
		c.path, c.apiKey, c.body = path, apiKey, string(body)
		c.mu.Unlock()
		return resp.status, []byte(resp.data)
	}
}

func (c *capture) served() (path, apiKey, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path, c.apiKey, c.body
}

// blockJSON builds a minimal popular block body for the fakes.
func blockJSON(blockID string, num, ts int64, parent string) string {
	return fmt.Sprintf(
		`{"blockID": %q, "block_header": {"raw_data": {"number": %d, "timestamp": %d, "parentHash": %q}}}`,
		blockID, num, ts, parent)
}

// TestTronClient_GetBlockByNum pins the request shape (JSON POST body with
// num), the parsing of a populated block, and that no API-key header leaves
// when none is configured.
func TestTronClient_GetBlockByNum(t *testing.T) {
	t.Parallel()
	captured := &capture{}
	server := newWalletFake(t, captured.handler(fakeResponse{
		status: http.StatusOK,
		data:   blockJSON("0000000003fe262d", 66987565, 1777445121000, "0000000003fe262c"),
	}))

	client := NewClient(server.URL, "", "", 0)
	block, err := client.GetBlockByNum(context.Background(), 66987565)
	require.NoError(t, err)
	assert.Equal(t, "0000000003fe262d", block.BlockID)
	assert.Equal(t, int64(66987565), block.BlockHeader.RawData.Number)
	assert.Equal(t, int64(1777445121000), block.BlockHeader.RawData.Timestamp)

	path, apiKey, body := captured.served()
	assert.Equal(t, "/wallet/getblockbynum", path)
	assert.Empty(t, apiKey, "no API key configured, header must not be sent")

	var sent map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &sent))
	assert.Equal(t, map[string]any{"num": float64(66987565)}, sent)
}

// TestTronClient_APIKeyHeaderInjection pins the fixed TronGrid
// authentication header on every request when an API key is configured.
func TestTronClient_APIKeyHeaderInjection(t *testing.T) {
	t.Parallel()
	captured := &capture{}
	server := newWalletFake(t, captured.handler(fakeResponse{
		status: http.StatusOK,
		data:   blockJSON("b", 1, 2, "p"),
	}))

	client := NewClient(server.URL, "", "test-key", 0)
	_, err := client.GetBlockByNum(context.Background(), 1)
	require.NoError(t, err)

	_, apiKey, _ := captured.served()
	assert.Equal(t, "test-key", apiKey)
}

// TestTronClient_GetBlockByNum_EmptyObject pins the not-found mapping: a
// not-yet-produced height arrives as HTTP 200 with {} and must surface as
// a pkg/errors.IsErrNotFound error, not a transport failure.
func TestTronClient_GetBlockByNum_EmptyObject(t *testing.T) {
	t.Parallel()
	server := newWalletFake(t, func(_ string, _ string, _ []byte) (int, []byte) {
		return http.StatusOK, []byte(`{}`)
	})

	client := NewClient(server.URL, "", "", 0)
	_, err := client.GetBlockByNum(context.Background(), 123456)
	require.Error(t, err)
	assert.True(t, errors.IsErrNotFound(err), "empty block body must classify as not found: %v", err)
	assert.Contains(t, err.Error(), "123456")
}

// TestTronClient_ErrorBody pins the business-failure mapping: HTTP 200 with
// an Error field carries the server's message verbatim.
func TestTronClient_ErrorBody(t *testing.T) {
	t.Parallel()
	server := newWalletFake(t, func(_ string, _ string, _ []byte) (int, []byte) {
		return http.StatusOK, []byte(`{"Error": "class java.lang.NumberFormatException : bad number"}`)
	})

	client := NewClient(server.URL, "", "", 0)
	_, err := client.GetNowBlock(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "class java.lang.NumberFormatException : bad number")
}

// TestTronClient_HTTPErrorStatus covers a transport-level rejection: a
// non-2xx status is not a not-found and carries the status code.
func TestTronClient_HTTPErrorStatus(t *testing.T) {
	t.Parallel()
	server := newWalletFake(t, func(_ string, _ string, _ []byte) (int, []byte) {
		return http.StatusInternalServerError, []byte(`{"Error": "boom"}`)
	})

	client := NewClient(server.URL, "", "", 0)
	_, err := client.GetBlockByID(context.Background(), "deadbeef")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
	assert.False(t, errors.IsErrNotFound(err), "a 500 must not classify as not found")
}

// TestTronClient_GetBlockByID pins the by-id request body name and the
// not-found path used to stop the reorg walk-back, plus the empty-id
// rejection as a caller bug.
func TestTronClient_GetBlockByID(t *testing.T) {
	t.Parallel()
	captured := &capture{}
	server := newWalletFake(t, captured.handler(fakeResponse{
		status: http.StatusOK,
		data:   blockJSON("deadbeef", 7, 8, "p"),
	}))

	client := NewClient(server.URL, "", "", 0)
	block, err := client.GetBlockByID(context.Background(), "deadbeef")
	require.NoError(t, err)
	assert.Equal(t, int64(7), block.BlockHeader.RawData.Number)

	_, _, body := captured.served()
	var sent map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &sent))
	assert.Equal(t, map[string]any{"value": "deadbeef"}, sent)

	_, emptyErr := client.GetBlockByID(context.Background(), "")
	require.Error(t, emptyErr, "an empty hash is a caller bug")

	notFoundServer := newWalletFake(t, func(_ string, _ string, _ []byte) (int, []byte) {
		return http.StatusOK, []byte(`{}`)
	})
	notFoundClient := NewClient(notFoundServer.URL, "", "", 0)
	_, notFoundErr := notFoundClient.GetBlockByID(context.Background(), "0000000000000000")
	require.Error(t, notFoundErr)
	assert.True(t, errors.IsErrNotFound(notFoundErr))
}

// TestTronClient_SolidityRouting pins base URL selection: /wallet paths go
// to the FullNode host, /walletsolidity paths to the Solidity host, and an
// empty Solidity host falls back to the FullNode URL.
func TestTronClient_SolidityRouting(t *testing.T) {
	t.Parallel()
	nodeCapture := &capture{}
	solidityCapture := &capture{}
	nodeServer := newWalletFake(t, nodeCapture.handler(fakeResponse{status: http.StatusOK, data: blockJSON("n", 1, 2, "p")}))
	solidityServer := newWalletFake(t, solidityCapture.handler(fakeResponse{status: http.StatusOK, data: blockJSON("s", 3, 4, "p")}))

	client := NewClient(nodeServer.URL, solidityServer.URL, "", 0)
	_, errNode := client.GetNowBlock(context.Background())
	require.NoError(t, errNode)
	_, errSolidity := client.GetSolidifiedNowBlock(context.Background())
	require.NoError(t, errSolidity)
	nodePath, _, _ := nodeCapture.served()
	solidityPath, _, _ := solidityCapture.served()
	assert.Equal(t, "/wallet/getnowblock", nodePath)
	assert.Equal(t, "/walletsolidity/getnowblock", solidityPath)

	fallbackCapture := &capture{}
	fallbackServer := newWalletFake(t, fallbackCapture.handler(fakeResponse{status: http.StatusOK, data: blockJSON("n", 1, 2, "p")}))
	fallbackClient := NewClient(fallbackServer.URL, "", "", 0)
	_, errFallback := fallbackClient.GetSolidifiedNowBlock(context.Background())
	require.NoError(t, errFallback)
	fallbackPath, _, _ := fallbackCapture.served()
	assert.Equal(t, "/walletsolidity/getnowblock", fallbackPath, "empty solidity host must fall back to the FullNode host")
}

// TestTronClient_Receipts pins receipt parsing: a populated list decodes in
// order, an empty list is a normal result, and an empty object resolves to
// no receipts at all.
func TestTronClient_Receipts(t *testing.T) {
	t.Parallel()
	populated := newWalletFake(t, func(_ string, _ string, _ []byte) (int, []byte) {
		return http.StatusOK, []byte(`[
			{"id": "a", "blockNumber": 1000000, "blockTimeStamp": 1578594852000, "fee": 345000,
			 "receipt": {"result": "SUCCESS", "energy_usage": 64284}},
			{"id": "b", "blockNumber": 1000000, "blockTimeStamp": 1578594852000}
		]`)
	})
	populatedClient := NewClient(populated.URL, "", "", 0)
	infos, err := populatedClient.GetTransactionInfoByBlockNum(context.Background(), 1000000)
	require.NoError(t, err)
	require.Len(t, infos, 2)
	assert.Equal(t, "a", infos[0].ID)
	assert.Equal(t, int64(345000), infos[0].Fee)
	assert.Equal(t, int64(64284), infos[0].Receipt.EnergyUsage)

	emptyList := newWalletFake(t, func(_ string, _ string, _ []byte) (int, []byte) {
		return http.StatusOK, []byte(`[]`)
	})
	emptyClient := NewClient(emptyList.URL, "", "", 0)
	none, err := emptyClient.GetTransactionInfoByBlockNum(context.Background(), 1000000)
	require.NoError(t, err)
	assert.Empty(t, none)

	emptyObject := newWalletFake(t, func(_ string, _ string, _ []byte) (int, []byte) {
		return http.StatusOK, []byte(`{}`)
	})
	objectClient := NewClient(emptyObject.URL, "", "", 0)
	noneObject, err := objectClient.GetTransactionInfoByBlockNum(context.Background(), 1000000)
	require.NoError(t, err)
	assert.Empty(t, noneObject)
}

// TestTronClient_TimeoutIsServedByHTTPClient pins that the configured
// timeout reaches the HTTP client as a full round-trip bound.
func TestTronClient_TimeoutIsServedByHTTPClient(t *testing.T) {
	t.Parallel()
	server := newWalletFake(t, func(_ string, _ string, _ []byte) (int, []byte) {
		time.Sleep(50 * time.Millisecond)
		return http.StatusOK, []byte(blockJSON("b", 1, 2, "p"))
	})

	client := NewClient(server.URL, "", "", 10*time.Millisecond)
	_, err := client.GetBlockByNum(context.Background(), 1)
	require.Error(t, err, "a 10ms budget cannot cover a 50ms response")
}
