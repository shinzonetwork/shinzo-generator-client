// Command fetch_blocks captures raw block data from any EVM-compatible
// JSON-RPC node into a replay fixture for the benchmarking acceptance suites.
// The chain and network are plain parameters (--chain/--network): they only
// label the fixture and derive its filename — the capture itself is
// transport-only JSON-RPC over HTTP, so it works unchanged across
// EVM-compatible chains.
//
// It stores the node's result bytes verbatim, so the replay mock server can
// serve exactly what the node served. Endpoint URLs and API keys are never
// written into the fixture.
//
// The GETH_* env var names are historical — the Generator is chain-agnostic,
// so this capture CLI reuses them like the rest of the tooling.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

const (
	// defaultBlockCount is how many blocks end at the tip when neither
	// --from nor --to is given.
	defaultBlockCount = 100

	// defaultDelay paces consecutive RPC requests to be polite to public
	// endpoints.
	defaultDelay = 250 * time.Millisecond

	// Retry bounds for transport errors, HTTP 429 and 5xx responses.
	maxRetries     = 5
	retryBaseDelay = 500 * time.Millisecond
	retryMaxDelay  = 8 * time.Second

	// httpTimeout bounds a single RPC round trip.
	httpTimeout = 30 * time.Second

	// Fixture output layout: fixtures for every chain share one directory
	// and the filename prefix identifies the chain
	// (e.g. bsc_blocks_<from>_<to>.json).
	defaultOutDir = "benchmarking/testdata"
	fixtureExt    = ".json"

	// Receipt capture modes recorded per block.
	receiptModeBatch = "batch"
	receiptModePerTx = "per-tx"

	// defaultChain/defaultNetwork prefill the chain-agnostic --chain and
	// --network flags; they only label the fixture, never the transport.
	defaultChain   = "BSC"
	defaultNetwork = "Mainnet"

	// maxErrorBodyBytes bounds how much of an error response body is
	// echoed into error messages.
	maxErrorBodyBytes = 1024

	// fixtureFilePerm is the fixture file's permission bits (owner-only:
	// the fixture contains chain data, but nothing should grow around it).
	fixtureFilePerm = 0o600
)

// rpcEnv bundles the endpoint connection parameters shared by every JSON-RPC
// request.
type rpcEnv struct {
	client     *http.Client
	url        string
	apiKey     string
	apiKeyType string
}

// captureOptions holds the parsed CLI parameters.
type captureOptions struct {
	chain   string
	network string
	from    uint64
	to      uint64
	out     string
	delay   time.Duration
}

// retryableError marks an RPC failure that a retry may fix. retryAfter
// carries the server's Retry-After hint when one was sent.
type retryableError struct {
	retryAfter time.Duration
	err        error
}

func (e *retryableError) Error() string {
	if e.retryAfter > 0 {
		return fmt.Sprintf("%v (Retry-After: %s)", e.err, e.retryAfter)
	}
	return e.err.Error()
}

func (e *retryableError) Unwrap() error { return e.err }

// retryBackoffFn is a test seam so unit tests can shrink the backoff waits.
var retryBackoffFn = retryBackoff //nolint:gochecknoglobals // test seam for retry timing

// retryBackoff grows exponentially from retryBaseDelay, capped at
// retryMaxDelay, so repeated failures never stall capture for minutes. The
// cap fires inside the loop: repeated unchecked doubling overflows the
// int64 duration to zero, which the final clamp would accept as-is.
func retryBackoff(attempt int) time.Duration {
	d := retryBaseDelay
	for range attempt {
		if d >= retryMaxDelay {
			return retryMaxDelay
		}
		d *= 2
	}
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	return d
}

// fixturePrefix builds the fixture filename prefix from the chain name, so
// fixtures for different chains coexist in the shared testdata directory.
func fixturePrefix(chain string) string {
	return strings.ToLower(chain) + "_blocks_"
}

// fixtureBlock holds one block's verbatim node responses. Receipts is always
// a JSON array of receipt objects regardless of capture mode, so replay
// consumers treat both modes uniformly.
type fixtureBlock struct {
	Number      string          `json:"number"`       // hex block number
	Block       json.RawMessage `json:"block"`        // eth_getBlockByNumber(number, full=true) result, verbatim
	Receipts    json.RawMessage `json:"receipts"`     // receipt array, verbatim
	ReceiptMode string          `json:"receipt_mode"` // "batch" or "per-tx"
}

// fixtureMeta describes the capture. It intentionally carries no endpoint or
// key material.
type fixtureMeta struct {
	Chain          string `json:"chain"`
	Network        string `json:"network"`
	From           uint64 `json:"from"`
	To             uint64 `json:"to"`
	BlockCount     int    `json:"block_count"`
	CapturedAt     string `json:"captured_at"`
	CapturePartial bool   `json:"capture_partial,omitempty"`
}

// replayFixture is the on-disk format the benchmarking acceptance suites
// replay through their mock JSON-RPC servers.
type replayFixture struct {
	Meta   fixtureMeta    `json:"meta"`
	Blocks []fixtureBlock `json:"blocks"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatalf("fetch_blocks: %v", err)
	}
}

func run(args []string) error {
	// Load .env if present so the bare binary works outside a sourced
	// shell, matching config.LoadConfig's behavior.
	_ = godotenv.Load()

	opts, err := parseFlags(args)
	if err != nil {
		return err
	}

	rpcURL := os.Getenv("GETH_RPC_URL")
	if rpcURL == "" {
		return fmt.Errorf("GETH_RPC_URL is required: point it at a JSON-RPC endpoint for %s %s", opts.chain, opts.network)
	}
	// The endpoint must be an ARCHIVAL node: fetching an arbitrary historical
	// block range needs full state/receipt history, which non-archival
	// (pruned/full) nodes serve only near the tip — deeper ranges return null
	// blocks or missing receipts and the capture fails partway through.
	env := &rpcEnv{
		client:     &http.Client{Timeout: httpTimeout},
		url:        rpcURL,
		apiKey:     os.Getenv("GETH_API_KEY"),
		apiKeyType: os.Getenv("GETH_API_KEY_TYPE"),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tip, err := fetchTip(ctx, env)
	if err != nil {
		return fmt.Errorf("resolve current tip: %w", err)
	}

	resolvedFrom, resolvedTo, err := resolveRange(opts.from, opts.to, tip)
	if err != nil {
		return err
	}
	if opts.out == "" {
		opts.out = filepath.Join(defaultOutDir,
			fmt.Sprintf("%s%d_%d%s", fixturePrefix(opts.chain), resolvedFrom, resolvedTo, fixtureExt))
	}
	//nolint:gosec // G706 false positive: the tainted labels are CR/LF-stripped
	// by sanitizeLogLabel, and the endpoint is reduced to scheme://host, but
	// this gosec build does not apply the rule's documented sanitizers
	// (strings.ReplaceAll, strconv.Quote) interprocedurally.
	log.Printf("capturing %s %s blocks %d..%d (tip %d) from %s",
		sanitizeLogLabel(opts.chain), sanitizeLogLabel(opts.network), resolvedFrom, resolvedTo, tip, redactEndpoint(rpcURL))

	return captureRange(ctx, env, opts, resolvedFrom, resolvedTo)
}

// sanitizeLogLabel strips CR/LF from values that originate outside the
// program — flags and environment variables — so a hostile value cannot
// forge or spoof log lines.
func sanitizeLogLabel(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	return strings.ReplaceAll(s, "\r", "")
}

// redactEndpoint reduces an endpoint URL to scheme://host so API keys that
// providers embed in the path (.../v2/<key>) never reach terminal or CI logs.
// url.Parse rejects control characters, so a CR/LF-forged value falls into the
// unparseable branch rather than being echoed.
func redactEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "(endpoint hidden)"
	}
	return u.Scheme + "://" + u.Host
}

// parseFlags parses and validates the CLI flags.
func parseFlags(args []string) (*captureOptions, error) {
	opts := &captureOptions{}
	fs := flag.NewFlagSet("fetch_blocks", flag.ContinueOnError)
	fs.StringVar(&opts.chain, "chain", defaultChain, "chain name labelling the fixture (e.g. BSC)")
	fs.StringVar(&opts.network, "network", defaultNetwork, "network name labelling the fixture (e.g. Mainnet)")
	fs.Uint64Var(&opts.from, "from", 0, "first block to capture (0 = defaultBlockCount blocks ending at --to or the tip)")
	fs.Uint64Var(&opts.to, "to", 0, "last block to capture (0 = the current tip)")
	fs.StringVar(&opts.out, "out", "", fmt.Sprintf("output fixture path (default %s/<chain>_<from>_<to>%s)", defaultOutDir, fixtureExt))
	fs.DurationVar(&opts.delay, "delay", defaultDelay, "pause between RPC requests")
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("parse flags: %w", err)
	}

	// A blank chain or network would corrupt the fixture identity and its
	// filename, so reject it up front rather than mid-capture.
	if strings.TrimSpace(opts.chain) == "" {
		return nil, fmt.Errorf("--chain must not be empty")
	}
	if strings.TrimSpace(opts.network) == "" {
		return nil, fmt.Errorf("--network must not be empty")
	}
	return opts, nil
}

// resolveRange turns the raw --from/--to values into a concrete capture
// range. Zero values mean "unset": --to falls back to the current tip and
// --from then defaults to a defaultBlockCount window ending at --to.
func resolveRange(from, to, tip uint64) (uint64, uint64, error) {
	if to == 0 {
		to = tip
	}
	if to > tip {
		return 0, 0, fmt.Errorf("--to %d is beyond the current tip %d", to, tip)
	}
	if from == 0 {
		// uint64 subtraction wraps below genesis, so the window start is
		// derived from the count of blocks that actually exist: to+1 blocks
		// sit at or under --to, so a tipside defaultBlockCount window only
		// reaches back that far.
		from = to + 1 - min(to+1, defaultBlockCount)
	}
	if from > to {
		return 0, 0, fmt.Errorf("--from %d is greater than --to %d", from, to)
	}
	return from, to, nil
}

// captureRange walks the block range, fills the fixture and saves it. An
// interrupt (SIGINT/SIGTERM) stops capture and saves whatever was collected
// so far as a partial fixture instead of losing the work.
func captureRange(ctx context.Context, env *rpcEnv, opts *captureOptions, from, to uint64) error {
	fx := &replayFixture{
		Meta: fixtureMeta{
			Chain:      opts.chain,
			Network:    opts.network,
			From:       from,
			To:         to,
			CapturedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}

	interrupted := false
	for num := from; num <= to && !interrupted; num++ {
		fb, err := captureBlock(ctx, env, num)
		if err != nil {
			if ctx.Err() != nil {
				interrupted = true
				break
			}
			return fmt.Errorf("block %d: %w", num, err)
		}
		fx.Blocks = append(fx.Blocks, *fb)
		log.Printf("captured block %d (%d/%d, %s receipts)", num, num-from+1, to-from+1, fb.ReceiptMode)

		if err := sleepCtx(ctx, opts.delay); err != nil {
			interrupted = true
		}
	}

	if interrupted {
		last := fx.Meta.From
		if len(fx.Blocks) > 0 {
			last, _ = parseHexUint(fx.Blocks[len(fx.Blocks)-1].Number)
		}
		fx.Meta.To = last
		fx.Meta.CapturePartial = true
		log.Printf("interrupted - saving partial fixture (%d blocks)", len(fx.Blocks))
	}
	fx.Meta.BlockCount = len(fx.Blocks)

	if err := saveFixture(fx, opts.out); err != nil {
		return fmt.Errorf("save fixture: %w", err)
	}
	log.Printf("saved %d blocks to %s", len(fx.Blocks), opts.out)
	return nil
}

// captureBlock fetches one block plus its receipts, keeping the node's result
// bytes verbatim.
func captureBlock(ctx context.Context, env *rpcEnv, num uint64) (*fixtureBlock, error) {
	hexNum := hexNum(num)

	blockRaw, err := rpcCall(ctx, env, "eth_getBlockByNumber", []any{hexNum, true})
	if err != nil {
		return nil, fmt.Errorf("eth_getBlockByNumber: %w", err)
	}
	if isJSONNull(blockRaw) {
		return nil, fmt.Errorf("block %s not available on the node (null result)", hexNum)
	}

	receipts, mode, err := captureReceipts(ctx, env, hexNum, blockRaw)
	if err != nil {
		return nil, err
	}

	return &fixtureBlock{Number: hexNum, Block: blockRaw, Receipts: receipts, ReceiptMode: mode}, nil
}

// captureReceipts prefers the batch eth_getBlockReceipts call and falls back
// to per-transaction eth_getTransactionReceipt when the batch answer is
// unavailable — a transport/envelope error, a null result (pruned or
// non-archival nodes), or a receipt count that disagrees with the block's
// transaction count — mirroring the production fetcher's fallback. Serving
// null or partial receipts to the replay would time a block without its
// receipt work and flatter the benchmark.
func captureReceipts(ctx context.Context, env *rpcEnv, hexNum string, blockRaw json.RawMessage) (json.RawMessage, string, error) {
	var block struct {
		Transactions []struct {
			Hash string `json:"hash"`
		} `json:"transactions"`
	}
	if err := json.Unmarshal(blockRaw, &block); err != nil {
		return nil, "", fmt.Errorf("parse block transactions: %w", err)
	}

	receipts, err := rpcCall(ctx, env, "eth_getBlockReceipts", []any{hexNum})
	switch {
	case err == nil && !isJSONNull(receipts):
		var batch []json.RawMessage
		if jerr := json.Unmarshal(receipts, &batch); jerr == nil && len(batch) == len(block.Transactions) {
			return receipts, receiptModeBatch, nil
		}
		log.Printf("block %s: eth_getBlockReceipts returned %d receipts for %d transactions, falling back to per-tx receipts", hexNum, len(batch), len(block.Transactions))
	case err != nil:
		log.Printf("block %s: eth_getBlockReceipts unavailable, falling back to per-tx receipts: %v", hexNum, err)
	default:
		log.Printf("block %s: eth_getBlockReceipts returned null (pruned or non-archival node), falling back to per-tx receipts", hexNum)
	}

	perTx := make([]json.RawMessage, 0, len(block.Transactions))
	for _, tx := range block.Transactions {
		raw, err := rpcCall(ctx, env, "eth_getTransactionReceipt", []any{tx.Hash})
		if err != nil {
			return nil, "", fmt.Errorf("eth_getTransactionReceipt %s: %w", tx.Hash, err)
		}
		if isJSONNull(raw) {
			return nil, "", fmt.Errorf("receipt for tx %s not found", tx.Hash)
		}
		perTx = append(perTx, raw)
	}
	joined, err := json.Marshal(perTx)
	if err != nil {
		return nil, "", fmt.Errorf("join per-tx receipts: %w", err)
	}
	return joined, receiptModePerTx, nil
}

// fetchTip resolves the node's current tip via eth_blockNumber.
func fetchTip(ctx context.Context, env *rpcEnv) (uint64, error) {
	raw, err := rpcCall(ctx, env, "eth_blockNumber", []any{})
	if err != nil {
		return 0, err
	}
	var hexStr string
	if err := json.Unmarshal(raw, &hexStr); err != nil {
		return 0, fmt.Errorf("parse eth_blockNumber result: %w", err)
	}
	tip, err := parseHexUint(hexStr)
	if err != nil {
		return 0, fmt.Errorf("parse eth_blockNumber result %q: %w", hexStr, err)
	}
	return tip, nil
}

// rpcCall performs one JSON-RPC request, retrying transport errors and HTTP
// 429/5xx responses with Retry-After or exponential backoff. JSON-RPC
// envelope errors are not retried: they are the node's answer (e.g. a missing
// eth_getBlockReceipts method) and the caller decides what to do with them.
func rpcCall(ctx context.Context, env *rpcEnv, method string, params any) (json.RawMessage, error) {
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			wait := retryBackoffFn(attempt)
			var rerr *retryableError
			if errors.As(lastErr, &rerr) && rerr.retryAfter > 0 {
				wait = rerr.retryAfter
			}
			if err := sleepCtx(ctx, wait); err != nil {
				return nil, err
			}
		}

		result, err := rpcOnce(ctx, env, method, params)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%s: giving up after %d attempts: %w", method, maxRetries, lastErr)
}

// rpcOnce issues a single JSON-RPC request and returns the raw result bytes.
func rpcOnce(ctx context.Context, env *rpcEnv, method string, params any) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env.url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Both key and header name are required: setting a header with an empty
	// name is a protocol error, and a key without a name is unusable anyway.
	if env.apiKey != "" && env.apiKeyType != "" {
		req.Header.Set(strings.ToLower(env.apiKeyType), env.apiKey)
	}

	resp, err := env.client.Do(req)
	if err != nil {
		return nil, &retryableError{err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, &retryableError{
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			err:        fmt.Errorf("http status %d from %s", resp.StatusCode, redactEndpoint(env.url)),
		}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("http status %d from %s: %s", resp.StatusCode, redactEndpoint(env.url), string(body))
	}

	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode rpc response: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Result, nil
}

// isRetryable reports whether the error is worth another attempt.
func isRetryable(err error) bool {
	var rerr *retryableError
	return errors.As(err, &rerr)
}

// parseRetryAfter parses a numeric Retry-After header, clamping negative or
// absurd values to a sane window.
func parseRetryAfter(raw string) time.Duration {
	if raw == "" {
		return 0
	}
	secs, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || secs <= 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > retryMaxDelay {
		return retryMaxDelay
	}
	return d
}

// saveFixture writes the fixture atomically: full bytes to a temp file, then
// a rename, so a crash never leaves a truncated fixture behind.
func saveFixture(fx *replayFixture, out string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil { //nolint:mnd
		return fmt.Errorf("create output dir: %w", err)
	}
	data, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal fixture: %w", err)
	}
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, data, fixtureFilePerm); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	return os.Rename(tmp, out)
}

// sleepCtx sleeps for d, aborting early when the context is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// hexNum formats a block number the way nodes expect it in params.
func hexNum(num uint64) string {
	return "0x" + strconv.FormatUint(num, 16)
}

// parseHexUint parses a hex-quantity string ("0x...") into a uint64.
func parseHexUint(s string) (uint64, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if trimmed == "" {
		return 0, fmt.Errorf("empty hex quantity")
	}
	n, err := strconv.ParseUint(trimmed, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("parse hex quantity %q: %w", s, err)
	}
	return n, nil
}

// isJSONNull reports whether raw is a JSON null (e.g. an unknown block).
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
