package main

// The tron capture backend speaks the java-tron wallet API: plain HTTP POST
// with a JSON body and bare JSON responses without a JSON-RPC envelope. It
// mirrors the production adapter's transport semantics
// (pkg/chains/tron/tron_client.go) so the captured fixture bytes are exactly
// what the replay mock must serve.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
)

// Wallet-API endpoint paths and the header the TronGrid key rides in by
// default. Duplicated from pkg/chains/tron/constants.go (unexported there)
// so this capture CLI stays a dependency-light main package.
const (
	tronEndpointGetNowBlock         = "/wallet/getnowblock"
	tronEndpointGetBlockByNum       = "/wallet/getblockbynum"
	tronEndpointGetTxInfoByBlockNum = "/wallet/gettransactioninfobyblocknum"

	tronAPIKeyHeader = "Tron-Pro-Api-Key" //nolint:gosec // public HTTP header name, not a credential

	// receiptModeBlock labels the Tron receipt capture: the wallet API has a
	// single per-block receipts endpoint and no batch/per-tx duality.
	receiptModeBlock = "block"
)

// tronEnv bundles the wallet-API connection parameters.
type tronEnv struct {
	client       *http.Client
	url          string
	apiKey       string
	apiKeyHeader string
}

// tronBackend captures replay fixtures over the Tron wallet API.
type tronBackend struct {
	env *tronEnv
}

// newTronBackend reads the Tron endpoint env. The API key header follows the
// production client's resolution: a configured TRON_API_KEY_TYPE wins
// (lowercased, header names match case-insensitively on the wire), an empty
// one keeps the TronGrid standard header.
func newTronBackend(opts *captureOptions) (*tronBackend, error) {
	rpcURL := os.Getenv("TRON_RPC_URL")
	if rpcURL == "" {
		return nil, fmt.Errorf("TRON_RPC_URL is required: point it at a wallet-API endpoint for %s %s", opts.chain, opts.network)
	}
	apiKeyHeader := strings.ToLower(strings.TrimSpace(os.Getenv("TRON_API_KEY_TYPE")))
	if apiKeyHeader == "" {
		apiKeyHeader = tronAPIKeyHeader
	}
	return &tronBackend{env: &tronEnv{
		client:       &http.Client{Timeout: httpTimeout},
		url:          rpcURL,
		apiKey:       os.Getenv("TRON_API_KEY"),
		apiKeyHeader: apiKeyHeader,
	}}, nil
}

func (b *tronBackend) redactedEndpoint() string {
	return utils.RedactEndpoint(b.env.url)
}

// fetchTip resolves the chain tip via getnowblock: the head block's
// raw_data.number is the highest height the node knows. The empty object is
// the wallet API's not-found shape and reads as an unavailable head block.
func (b *tronBackend) fetchTip(ctx context.Context) (uint64, error) {
	raw, err := b.walletCall(ctx, tronEndpointGetNowBlock, nil)
	if err != nil {
		return 0, err
	}
	var block struct {
		BlockID     string `json:"blockID"`
		BlockHeader struct {
			RawData struct {
				Number int64 `json:"number"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return 0, fmt.Errorf("parse getnowblock result: %w", err)
	}
	if block.BlockID == "" {
		return 0, fmt.Errorf("head block unavailable (empty object)")
	}
	if block.BlockHeader.RawData.Number < 0 {
		return 0, fmt.Errorf("head block carries negative number %d", block.BlockHeader.RawData.Number)
	}
	return uint64(block.BlockHeader.RawData.Number), nil
}

// captureBlock fetches one block plus its per-block receipts, keeping the
// node's body bytes verbatim. The wallet API serves receipts per block with
// no fallback path, so a receipt count that disagrees with the block's
// transaction count is a capture failure, not a fallback trigger: serving
// partial receipts to the replay would time a block without its receipt
// work and flatter the benchmark.
func (b *tronBackend) captureBlock(ctx context.Context, num uint64) (*fixtureBlock, error) {
	blockRaw, err := b.walletCall(ctx, tronEndpointGetBlockByNum, map[string]int64{"num": int64(num)}) //nolint:gosec // G115: num <= tip, which came from an int64 height
	if err != nil {
		return nil, err
	}
	if isEmptyJSONObject(blockRaw) {
		return nil, fmt.Errorf("block %d not available on the node (empty object)", num)
	}
	var block struct {
		BlockID      string            `json:"blockID"`
		Transactions []json.RawMessage `json:"transactions"`
	}
	if err := json.Unmarshal(blockRaw, &block); err != nil {
		return nil, fmt.Errorf("parse getblockbynum result: %w", err)
	}
	if block.BlockID == "" {
		return nil, fmt.Errorf("block %d not available on the node (missing blockID)", num)
	}

	receiptsRaw, err := b.walletCall(ctx, tronEndpointGetTxInfoByBlockNum, map[string]int64{"num": int64(num)}) //nolint:gosec // G115: num <= tip, which came from an int64 height
	if err != nil {
		return nil, err
	}
	receipts := normalizeTronReceipts(receiptsRaw)
	var infos []json.RawMessage
	if err := json.Unmarshal(receipts, &infos); err != nil {
		return nil, fmt.Errorf("parse gettransactioninfobyblocknum result: %w", err)
	}
	if len(infos) != len(block.Transactions) {
		return nil, fmt.Errorf("block %d: node returned %d receipts for %d transactions", num, len(infos), len(block.Transactions))
	}

	return &fixtureBlock{
		Number:      strconv.FormatUint(num, 10),
		Block:       blockRaw,
		Receipts:    receipts,
		ReceiptMode: receiptModeBlock,
	}, nil
}

// normalizeTronReceipts rewrites the two "no receipts yet" spellings (the
// empty object and an empty body, which some nodes answer for an unknown
// height) into an empty JSON array, so the fixture always carries an array
// like the EVM receipt modes do and replay consumers stay uniform.
func normalizeTronReceipts(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" {
		return json.RawMessage("[]")
	}
	return raw
}

// isEmptyJSONObject reports whether raw is the bare empty object, the
// wallet API's not-found answer.
func isEmptyJSONObject(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "{}"
}

// walletCall issues one wallet-API request through the shared retry loop,
// reusing the EVM transport's backoff, Retry-After handling, and interrupt
// awareness unchanged.
func (b *tronBackend) walletCall(ctx context.Context, endpoint string, body any) (json.RawMessage, error) {
	return withRetries(ctx, endpoint, func() (json.RawMessage, error) {
		return tronOnce(ctx, b.env, endpoint, body)
	})
}

// tronOnce issues a single wallet-API request and returns the raw body
// bytes. Business failures arrive as HTTP 200 with an {"Error": "..."}
// body: that is the node's answer, not a transport failure, so it returns
// unretried. HTTP 429/5xx and transport errors surface as retryable.
func tronOnce(ctx context.Context, env *tronEnv, endpoint string, body any) (json.RawMessage, error) {
	payload := []byte("{}")
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("tron %s: marshal request: %w", endpoint, err)
		}
		payload = encoded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env.url+endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("tron %s: build request: %w", endpoint, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if env.apiKey != "" {
		req.Header.Set(env.apiKeyHeader, env.apiKey)
	}

	resp, err := env.client.Do(req)
	if err != nil {
		return nil, &retryableError{err: fmt.Errorf("tron %s: %w", endpoint, err)}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, &retryableError{
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			err:        fmt.Errorf("tron %s: http status %d from %s", endpoint, resp.StatusCode, utils.RedactEndpoint(env.url)),
		}
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("tron %s: http status %d from %s: %s", endpoint, resp.StatusCode, utils.RedactEndpoint(env.url), string(data))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tron %s: read response: %w", endpoint, err)
	}
	var business struct {
		Error string `json:"Error"`
	}
	if err := json.Unmarshal(data, &business); err == nil && business.Error != "" {
		return nil, fmt.Errorf("tron %s: %s", endpoint, business.Error)
	}
	return data, nil
}
