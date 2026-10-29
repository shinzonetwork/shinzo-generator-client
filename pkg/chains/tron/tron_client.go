package tron

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Client speaks the java-tron wallet API: plain HTTP POST with JSON
// bodies, no SDK and no WebSocket (the indexer polls the chain head on its
// own loop).
//
// Business failures arrive as HTTP 200: an exception produces a body with
// only an Error field, and an unknown block produces the empty object. Both
// are mapped to errors whose text keeps the "not found" convention of
// pkg/errors.IsErrNotFound, so the processor's retry logic treats a
// not-yet-produced height exactly like the EVM adapter does.
type Client struct {
	httpClient  *http.Client
	nodeURL     string
	solidityURL string
	apiKey      string
	rpcErrors   metric.Int64Counter
}

// NewClient builds a client against the configured endpoints. The
// wallet endpoints are served lazily, so no dial happens here: the caller's
// context is checked at Connect time in the fetcher instead. solidityURL
// empty means the same host as nodeURL (TronGrid serves /walletsolidity/*
// there); timeout bounds the whole HTTP round trip when positive.
func NewClient(nodeURL, solidityURL, apiKey string, timeout time.Duration) *Client {
	rpcErrors, meterErr := otel.Meter(
		"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/tron",
	).Int64Counter("shinzo.generator.rpc.errors",
		metric.WithDescription("Failed Tron wallet-API calls, by method and error.type. Not-yet-produced blocks are not counted."),
		metric.WithUnit("{error}"),
	)
	if meterErr != nil {
		otel.Handle(meterErr)
	}

	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	return &Client{
		httpClient:  httpClient,
		nodeURL:     trimBaseURL(nodeURL),
		solidityURL: resolveSolidityURL(nodeURL, solidityURL),
		apiKey:      apiKey,
		rpcErrors:   rpcErrors,
	}
}

// trimBaseURL normalizes a wallet-API base URL: surrounding whitespace and
// trailing slashes off, so endpoint paths concatenate cleanly.
func trimBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// resolveSolidityURL resolves the SolidityNode base URL, falling back to
// the FullNode host when no dedicated host is configured.
func resolveSolidityURL(nodeURL, solidityURL string) string {
	if trimmed := trimBaseURL(solidityURL); trimmed != "" {
		return trimmed
	}
	return trimBaseURL(nodeURL)
}

// post issues one wallet-API request and returns the raw response body.
// The endpoint decides the base URL: /walletsolidity paths go to the
// Solidity host, everything else to FullNode. Redacted debug logging
// mirrors the EVM transport. Non-2xx responses surface as an
// *httpStatusError; interpretation of the 200 body stays with the callers.
func (c *Client) post(ctx context.Context, method, endpoint string, body any) ([]byte, error) {
	base := c.nodeURL
	if strings.HasPrefix(endpoint, "/walletsolidity") {
		base = c.solidityURL
	}

	payload := []byte("{}")
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("tron %s: marshal request: %w", endpoint, err) //nolint:err113
		}
		payload = encoded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("tron %s: build request: %w", endpoint, utils.RedactHTTPError(err)) //nolint:err113
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set(apiKeyHeader, c.apiKey)
	}
	logDebugf("tron %s %s", req.Method, utils.RedactEndpoint(req.URL.String()))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		err = fmt.Errorf("tron %s: %w", endpoint, utils.RedactHTTPError(err)) //nolint:err113
		c.countRPCError(ctx, method, err)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("tron %s: read response: %w", endpoint, utils.RedactHTTPError(err)) //nolint:err113
		c.countRPCError(ctx, method, err)
		return nil, err
	}
	logDebugf("tron %s: HTTP %d, %d bytes", endpoint, resp.StatusCode, len(data))

	if resp.StatusCode < http.StatusOK || resp.StatusCode > httpSuccessMax {
		err := &httpStatusError{
			status: resp.StatusCode,
			msg:    fmt.Sprintf("tron %s: HTTP %d: %s", endpoint, resp.StatusCode, bodySample(data)), //nolint:err113
		}
		c.countRPCError(ctx, method, err)
		return nil, err
	}
	return data, nil
}

// decodeResponse unmarshals the JSON body into dst after rejecting an
// {"Error": "..."} business failure (HTTP 200, server exception). The
// server's message stays verbatim: whenever the node reports not found or
// does not exist, pkg/errors.IsErrNotFound classifies it for the shared
// retry logic.
func (c *Client) decodeResponse(ctx context.Context, method, endpoint string, data []byte, dst any) error {
	var business struct {
		Error string `json:"Error"`
	}
	if err := json.Unmarshal(data, &business); err == nil && business.Error != "" {
		err = fmt.Errorf("tron %s: %s", endpoint, business.Error) //nolint:err113
		c.countRPCError(ctx, method, err)
		return err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		err = fmt.Errorf("tron %s: decode response: %w", endpoint, err) //nolint:err113
		c.countRPCError(ctx, method, err)
		return err
	}
	return nil
}

// GetBlockByNum fetches a block by height. A height the node does not know
// (a not-yet-produced future height in head-lag indexing) arrives as HTTP
// 200 with body {} and maps to a "not found" error so the processor retries
// every 3s instead of failing.
func (c *Client) GetBlockByNum(ctx context.Context, num int64) (*Block, error) {
	return c.blockBy(ctx, methodGetBlockByNum, endpointGetBlockByNum,
		map[string]int64{"num": num}, fmt.Sprintf("block %d not found", num))
}

// GetBlockByID fetches a block by its content hash (the 64-hex blockID). An
// unknown hash returns {} and maps to a "not found" error, matching
// by-height fetch semantics for the reorg walk-back.
func (c *Client) GetBlockByID(ctx context.Context, blockID string) (*Block, error) {
	if blockID == "" {
		return nil, fmt.Errorf("tron: empty block id") //nolint:err113
	}
	return c.blockBy(ctx, methodGetBlockByID, endpointGetBlockByID,
		map[string]string{"value": blockID}, fmt.Sprintf("block %s not found", blockID))
}

// blockBy fetches one block over the given endpoint and rejects the empty
// object as not found with the given message.
func (c *Client) blockBy(ctx context.Context, method, endpoint string, body any, emptyMsg string) (*Block, error) {
	data, err := c.post(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	var block Block
	if err := c.decodeResponse(ctx, method, endpoint, data, &block); err != nil {
		return nil, err
	}
	if block.BlockID == "" {
		return nil, fmt.Errorf("tron: %s", emptyMsg) //nolint:err113
	}
	return &block, nil
}

// GetNowBlock returns the FullNode's latest block, which may be
// non-solidified.
func (c *Client) GetNowBlock(ctx context.Context) (*Block, error) {
	return c.blockBy(ctx, methodGetNowBlock, endpointGetNowBlock, nil, "head block unavailable")
}

// GetSolidifiedNowBlock returns the SolidityNode's latest block, the
// solidified tip the reorg walk-back treats as its floor.
func (c *Client) GetSolidifiedNowBlock(ctx context.Context) (*Block, error) {
	return c.blockBy(ctx, methodSolidityGetNowBlock, endpointSolidityGetNowBlock, nil, "solidified head block unavailable")
}

// GetTransactionInfoByBlockNum fetches the receipts (TransactionInfo list)
// of a block. A block without transactions returns [], an empty list: a
// normal result, not a not-found.
func (c *Client) GetTransactionInfoByBlockNum(ctx context.Context, num int64) ([]*TransactionInfo, error) {
	data, err := c.post(ctx, methodGetTxInfoByBlockNum, endpointGetTxInfoByBlockNum, map[string]int64{"num": num})
	if err != nil {
		return nil, err
	}
	if trimmed := strings.TrimSpace(string(data)); trimmed == "" || trimmed == "{}" {
		// Some nodes answer an unknown height with the empty object where
		// others answer []; both resolve to "no receipts yet" and the
		// fetcher pins whatever the block fetch returned.
		return nil, nil
	}
	var infos []*TransactionInfo
	if err := c.decodeResponse(ctx, methodGetTxInfoByBlockNum, endpointGetTxInfoByBlockNum, data, &infos); err != nil {
		return nil, err
	}
	return infos, nil
}

// Close releases client resources. The plain HTTP client needs no teardown.
func (c *Client) Close() error {
	return nil
}

// httpStatusError is a non-2xx response from a wallet endpoint.
type httpStatusError struct {
	status int
	msg    string
}

func (e *httpStatusError) Error() string {
	return e.msg
}

// countRPCError counts a failed wallet-API call. Not-yet-produced blocks
// (the "not found" convention), context cancellation, and deadline expiry
// are expected outcomes of head indexing, not failures.
func (c *Client) countRPCError(ctx context.Context, method string, err error) {
	if c.rpcErrors == nil || err == nil ||
		isContextError(err) ||
		strings.Contains(err.Error(), errors.ErrStrNotFound) {
		return
	}
	c.rpcErrors.Add(ctx, 1, metric.WithAttributes(
		attribute.String("rpc.method", method),
		attribute.String("error.type", rpcErrorType(err)),
	))
}

// rpcErrorType maps err to the error.type telemetry value, matching the
// dashboard vocabulary of the EVM adapter.
func rpcErrorType(err error) string {
	if statusErr, ok := stderrors.AsType[*httpStatusError](err); ok {
		if statusErr.status == http.StatusTooManyRequests {
			return "rate_limited"
		}
		return "http_error"
	}
	if isContextError(err) {
		return "timeout"
	}
	return "_OTHER"
}

// isContextError reports whether err was caused by context cancellation or
// deadline expiry, accepting the socket-deadline sentinel the transport can
// surface instead of the context's own one.
func isContextError(err error) bool {
	return stderrors.Is(err, context.Canceled) ||
		stderrors.Is(err, context.DeadlineExceeded) ||
		stderrors.Is(err, os.ErrDeadlineExceeded)
}

// bodySample trims a response body for error text: enough to identify the
// failure, nowhere near enough to flood the logs.
func bodySample(data []byte) string {
	const maxLen = 200
	text := strings.TrimSpace(string(data))
	if len(text) > maxLen {
		return text[:maxLen] + "..."
	}
	return text
}

// logDebugf emits a debug line only when a logger is wired: unit tests can
// run before any logger init.
func logDebugf(format string, args ...any) {
	if logger.Sugar != nil {
		logger.Sugar.Debugf(format, args...)
	}
}
