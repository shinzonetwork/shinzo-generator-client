package main

// The bitcoin capture backend speaks Bitcoin Core's JSON-RPC 1.0 dialect:
// HTTP POST with basic auth (or a hosted-provider key header) and an envelope
// response. It mirrors the production adapter's transport semantics
// (pkg/chains/bitcoin/bitcoin_client.go) so the captured fixture bytes are
// exactly what the replay mock must serve.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
)

const (
	// Core method names the capture issues, paired the way the production
	// fetcher pairs them: getblockhash pins the height to the main-chain
	// hash, then the verbosity-3 body is fetched by that hash.
	bitcoinMethodGetBlockCount = "getblockcount"
	bitcoinMethodGetBlockHash  = "getblockhash"
	bitcoinMethodGetBlock      = "getblock"

	// rpcCodeHeightOutOfRange is Core's -8, the answer getblockhash gives for
	// a height the answering node has not indexed. Within a capture range
	// resolved against the tip this can only be load-balancer lag, which is
	// why it is the one RPC error the capture retries one level up.
	rpcCodeHeightOutOfRange = -8

	// bitcoinCaptureVerbosity is the getblock verbosity the production
	// adapter indexes: parsed transactions with per-input prevout undo data
	// and per-transaction fees.
	bitcoinCaptureVerbosity = 3

	// bitcoinDefaultAPIKeyHeader is the header name used when BITCOIN_API_KEY
	// is set and BITCOIN_API_KEY_TYPE is empty, matching the production
	// client's default. Duplicated here so the capture CLI stays a
	// dependency-light main package.
	bitcoinDefaultAPIKeyHeader = "x-api-key" //nolint:gosec // public HTTP header name, not a credential

	// receiptModeNone labels the Bitcoin receipt capture: Bitcoin has no
	// receipt pass, so fixtures record empty receipts and the replay never
	// times receipt work for this chain.
	receiptModeNone = "none"

	// bitcoinCaptureAttempts bounds one block's capture: 1 try plus 3
	// retries. The symptom being retried is load-balancer lag, a race of
	// seconds, not the 10-minute block interval, so the bounded backoff
	// outlasts the lag while a genuinely absent height still fails fast.
	bitcoinCaptureAttempts = 4
)

// bitcoinEnv bundles the endpoint connection parameters shared by every
// Bitcoin JSON-RPC request.
type bitcoinEnv struct {
	client       *http.Client
	url          string
	user         string
	password     string
	apiKey       string
	apiKeyHeader string
}

// bitcoinBackend captures replay fixtures over the Bitcoin Core JSON-RPC
// surface.
type bitcoinBackend struct {
	env *bitcoinEnv
}

// newBitcoinBackend reads the Bitcoin endpoint env. Bitcoin Core accepts only
// basic auth or cookie auth; the capture CLI covers the basic-auth pair, and
// hosted providers (own endpoints that add a key header on top of Core's
// surface) take BITCOIN_API_KEY. One mechanism is required, and the error
// names both so an unauthenticated run fails before any request is sent.
// Cookie auth stays out of scope for the capture CLI: the .cookie file is a
// local-daemon artifact, not an endpoint credential.
func newBitcoinBackend(opts *captureOptions) (*bitcoinBackend, error) {
	rpcURL := os.Getenv("BITCOIN_RPC_URL")
	if rpcURL == "" {
		return nil, fmt.Errorf("BITCOIN_RPC_URL is required: point it at a Bitcoin Core JSON-RPC endpoint for %s %s", opts.chain, opts.network)
	}
	user := os.Getenv("BITCOIN_RPC_USER")
	password := os.Getenv("BITCOIN_RPC_PASSWORD")
	apiKey := os.Getenv("BITCOIN_API_KEY")
	if (user == "" || password == "") && apiKey == "" {
		return nil, fmt.Errorf("bitcoin capture needs auth: set BITCOIN_RPC_USER and BITCOIN_RPC_PASSWORD for basic auth, or BITCOIN_API_KEY for a hosted provider")
	}
	apiKeyHeader := strings.ToLower(strings.TrimSpace(os.Getenv("BITCOIN_API_KEY_TYPE")))
	if apiKeyHeader == "" {
		apiKeyHeader = bitcoinDefaultAPIKeyHeader
	}
	return &bitcoinBackend{env: &bitcoinEnv{
		client:       &http.Client{Timeout: httpTimeout},
		url:          rpcURL,
		user:         user,
		password:     password,
		apiKey:       apiKey,
		apiKeyHeader: apiKeyHeader,
	}}, nil
}

func (b *bitcoinBackend) redactedEndpoint() string {
	return utils.RedactEndpoint(b.env.url)
}

// fetchTip resolves the chain tip via getblockcount.
func (b *bitcoinBackend) fetchTip(ctx context.Context) (uint64, error) {
	raw, err := b.bitcoinCall(ctx, bitcoinMethodGetBlockCount, []any{})
	if err != nil {
		return 0, err
	}
	var count int64
	if err := json.Unmarshal(raw, &count); err != nil {
		return 0, fmt.Errorf("parse getblockcount result: %w", err)
	}
	if count < 0 {
		return 0, fmt.Errorf("getblockcount answered %d", count)
	}
	return uint64(count), nil
}

// captureBlock fetches one block, keeping the node's result bytes verbatim.
// Bitcoin has no receipt pass, so the fixture records empty receipts in the
// none mode and the on-disk envelope stays identical across adapters.
func (b *bitcoinBackend) captureBlock(ctx context.Context, num uint64) (*fixtureBlock, error) {
	var lastErr error
	for attempt := range bitcoinCaptureAttempts {
		if attempt > 0 {
			if err := sleepCtx(ctx, retryBackoffFn(attempt)); err != nil {
				return nil, err
			}
		}
		fb, err := b.captureBlockOnce(ctx, num)
		if err == nil {
			return fb, nil
		}
		lastErr = err
		if _, ok := errors.AsType[*heightLagError](err); !ok {
			return nil, err
		}
		log.Printf("block %d: %v (attempt %d/%d, a load balancer may have served a node that has not indexed the height yet)", num, err, attempt+1, bitcoinCaptureAttempts)
	}
	return nil, fmt.Errorf("block %d: %w (still not indexed after %d attempts)", num, lastErr, bitcoinCaptureAttempts)
}

// heightLagError marks getblockhash's -8 answer for a height the resolved tip
// guarantees exists. It is not a transport failure (withRetries retries only
// transport errors and 429/5xx), which is why the bitcoin backend retries it
// one level up.
type heightLagError struct {
	err error
}

func (e *heightLagError) Error() string { return e.err.Error() }

func (e *heightLagError) Unwrap() error { return e.err }

// captureBlockOnce performs one hash-plus-body fetch attempt, keeping the
// node's result bytes verbatim. The body's hash and height must echo the
// requested pair: the height-to-hash lookup and the by-hash body fetch pin
// the block twice, so a mismatch is a capture bug rather than node lag and
// stays fatal across the retries.
func (b *bitcoinBackend) captureBlockOnce(ctx context.Context, num uint64) (*fixtureBlock, error) {
	hashRaw, err := b.bitcoinCall(ctx, bitcoinMethodGetBlockHash, []any{num})
	if err != nil {
		return nil, err
	}
	var hash string
	if err := json.Unmarshal(hashRaw, &hash); err != nil {
		return nil, fmt.Errorf("parse getblockhash result: %w", err)
	}
	if hash == "" {
		return nil, fmt.Errorf("getblockhash answered an empty hash for block %d", num)
	}

	blockRaw, err := b.bitcoinCall(ctx, bitcoinMethodGetBlock, []any{hash, bitcoinCaptureVerbosity})
	if err != nil {
		return nil, err
	}
	var body struct {
		Hash   string `json:"hash"`
		Height int64  `json:"height"`
	}
	if err := json.Unmarshal(blockRaw, &body); err != nil {
		return nil, fmt.Errorf("parse getblock result: %w", err)
	}
	if body.Hash != hash {
		return nil, fmt.Errorf("getblock answered hash %s for request %s", body.Hash, hash)
	}
	if body.Height < 0 || uint64(body.Height) != num { //nolint:gosec // G115: a negative height is rejected above, so the conversion cannot wrap
		return nil, fmt.Errorf("getblock answered height %d for request %d", body.Height, num)
	}

	return &fixtureBlock{
		Number:      strconv.FormatUint(num, 10),
		Block:       blockRaw,
		Receipts:    json.RawMessage("[]"),
		ReceiptMode: receiptModeNone,
	}, nil
}

// bitcoinCall issues one JSON-RPC request through the shared retry loop,
// reusing the EVM transport's backoff, Retry-After handling, and interrupt
// awareness unchanged.
func (b *bitcoinBackend) bitcoinCall(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return withRetries(ctx, method, func() (json.RawMessage, error) {
		return bitcoinOnce(ctx, b.env, method, params)
	})
}

// bitcoinOnce issues a single JSON-RPC request and returns the raw result
// bytes. Core reports RPC failures as HTTP 500 carrying a JSON-RPC error
// object, so the envelope is decoded before the status checks: an
// application answer reaches the caller unretried, while proxy failures
// (HTML or empty bodies on 429/5xx) soak up the transport retries.
func bitcoinOnce(ctx context.Context, env *bitcoinEnv, method string, params any) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "1.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, fmt.Errorf("bitcoin %s: marshal request: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env.url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("bitcoin %s: build request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Core serves basic auth natively; a hosted provider's key rides as an
	// extra header named after the lowercased api_key_type, like the
	// production client does.
	if env.user != "" && env.password != "" {
		req.SetBasicAuth(env.user, env.password)
	}
	if env.apiKey != "" && env.apiKeyHeader != "" {
		req.Header.Set(env.apiKeyHeader, env.apiKey)
	}

	resp, err := env.client.Do(req)
	if err != nil {
		return nil, &retryableError{err: fmt.Errorf("bitcoin %s: %w", method, err)}
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &retryableError{err: fmt.Errorf("bitcoin %s: read response: %w", method, err)}
	}

	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		// Not a JSON-RPC envelope: classify by status the way the proxy
		// answers deserve.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, &retryableError{
				retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
				err:        fmt.Errorf("bitcoin %s: http status %d from %s", method, resp.StatusCode, utils.RedactEndpoint(env.url)),
			}
		}
		if len(body) > maxErrorBodyBytes {
			body = body[:maxErrorBodyBytes]
		}
		return nil, fmt.Errorf("bitcoin %s: http status %d from %s: %s", method, resp.StatusCode, utils.RedactEndpoint(env.url), string(body))
	}
	if envelope.Error != nil {
		if method == bitcoinMethodGetBlockHash && envelope.Error.Code == rpcCodeHeightOutOfRange {
			return nil, &heightLagError{err: fmt.Errorf("rpc error %d: %s", envelope.Error.Code, envelope.Error.Message)}
		}
		return nil, fmt.Errorf("rpc error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bitcoin %s: http status %d from %s", method, resp.StatusCode, utils.RedactEndpoint(env.url))
	}
	return envelope.Result, nil
}
