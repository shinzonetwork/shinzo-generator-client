package bitcoin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
)

// Verbosity is the second argument to Bitcoin Core `getblock`. We always use 3
// to receive prevout undo data alongside transactions.
const Verbosity = 3

const defaultTimeout = 30 * time.Second

// Client is a Bitcoin Core JSON-RPC client over HTTP. It supports both static
// rpcuser/rpcpassword auth and Bitcoin Core's cookie-file auth (the default
// when no rpcuser is configured).
type Client struct {
	url  string
	http *http.Client

	authMu     sync.RWMutex
	user, pass string
	cookiePath string
}

// Config configures a Bitcoin Core RPC client. Provide either User+Pass for
// static auth, or CookiePath to read credentials from Bitcoin Core's
// `.cookie` file (typically "<datadir>/.cookie", or "<datadir>/testnet3/.cookie"
// for testnet). When both are set, User+Pass wins.
type Config struct {
	URL        string
	User       string
	Pass       string
	CookiePath string
	Timeout    time.Duration
}

// NewClient establishes the HTTP transport and validates that auth credentials
// can be obtained. It does not yet make an RPC call against the server.
func NewClient(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.NewConfigurationError("rpc", "NewClient", "missing URL", "", nil)
	}
	if cfg.User == "" && cfg.CookiePath == "" {
		return nil, errors.NewConfigurationError("rpc", "NewClient",
			"no auth configured (set User+Pass or CookiePath)", "", nil)
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	c := &Client{
		url:        cfg.URL,
		http:       &http.Client{Timeout: timeout},
		user:       cfg.User,
		pass:       cfg.Pass,
		cookiePath: cfg.CookiePath,
	}
	if c.user == "" {
		if err := c.refreshCookie(); err != nil {
			return nil, errors.NewRPCConnectionFailed("rpc", "NewClient", cfg.CookiePath, err)
		}
	}
	logger.Sugar.Infof("Bitcoin RPC client configured for %s", cfg.URL)
	return c, nil
}

// Close is a no-op kept for symmetry with stateful clients.
func (c *Client) Close() {}

// refreshCookie re-reads the .cookie file. Bitcoin Core rotates it on every
// daemon restart, so we read it on first use and on any 401 response.
func (c *Client) refreshCookie() error {
	data, err := os.ReadFile(c.cookiePath)
	if err != nil {
		return fmt.Errorf("read cookie file %s: %w", c.cookiePath, err)
	}
	user, pass, ok := strings.Cut(strings.TrimSpace(string(data)), ":")
	if !ok {
		return fmt.Errorf("cookie file %s: expected user:pass format", c.cookiePath)
	}
	c.authMu.Lock()
	c.user = user
	c.pass = pass
	c.authMu.Unlock()
	return nil
}

func (c *Client) basicAuth() (string, string) {
	c.authMu.RLock()
	defer c.authMu.RUnlock()
	return c.user, c.pass
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// call posts a JSON-RPC request and returns the raw `result` field.
// On 401 with cookie auth we re-read the cookie and retry once.
func (c *Client) call(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "1.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", method, err)
	}

	respBody, status, err := c.do(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if status == http.StatusUnauthorized && c.cookiePath != "" {
		if rerr := c.refreshCookie(); rerr != nil {
			return nil, fmt.Errorf("%s: 401 and cookie refresh failed: %w", method, rerr)
		}
		respBody, status, err = c.do(ctx, body)
		if err != nil {
			return nil, fmt.Errorf("%s after cookie refresh: %w", method, err)
		}
	}
	// Bitcoin Core returns 500 with a JSON-RPC error body for application errors;
	// only treat other non-200 statuses as transport failures.
	if status != http.StatusOK && status != http.StatusInternalServerError {
		return nil, fmt.Errorf("%s: HTTP %d: %s", method, status, string(respBody))
	}
	var resp rpcResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("%s decode envelope: %w", method, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("%s: %w", method, resp.Error)
	}
	return resp.Result, nil
}

func (c *Client) do(ctx context.Context, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	user, pass := c.basicAuth()
	req.SetBasicAuth(user, pass)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

// FetchLatestBlockHeight returns the chain tip height (`getblockcount`).
func (c *Client) FetchLatestBlockHeight(ctx context.Context) (int64, error) {
	raw, err := c.call(ctx, "getblockcount", nil)
	if err != nil {
		return 0, err
	}
	var h int64
	if err := json.Unmarshal(raw, &h); err != nil {
		return 0, fmt.Errorf("getblockcount decode: %w", err)
	}
	return h, nil
}

// FetchBlockHashByHeight resolves a height to its canonical block hash.
func (c *Client) FetchBlockHashByHeight(ctx context.Context, height int64) (string, error) {
	raw, err := c.call(ctx, "getblockhash", []any{height})
	if err != nil {
		return "", err
	}
	var h string
	if err := json.Unmarshal(raw, &h); err != nil {
		return "", fmt.Errorf("getblockhash decode: %w", err)
	}
	return h, nil
}

// FetchBlockByHash fetches a block at verbosity 3 (full transactions with
// prevout undo data).
func (c *Client) FetchBlockByHash(ctx context.Context, hash string) (*Block, error) {
	if !isHexHash(hash) {
		return nil, fmt.Errorf("invalid block hash %q: expected 64 hex chars", hash)
	}
	raw, err := c.call(ctx, "getblock", []any{hash, Verbosity})
	if err != nil {
		return nil, err
	}
	var b Block
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("getblock decode: %w", err)
	}
	return &b, nil
}

// FetchBlockByHeight fetches a block by height. Costs two RPC round trips.
func (c *Client) FetchBlockByHeight(ctx context.Context, height int64) (*Block, error) {
	hash, err := c.FetchBlockHashByHeight(ctx, height)
	if err != nil {
		return nil, err
	}
	return c.FetchBlockByHash(ctx, hash)
}

func isHexHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
