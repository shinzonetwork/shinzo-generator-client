package bitcoin

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	chainhash "github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/rpcclient"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
)

// RPC method names, reused wherever a call's metric label is needed.
const (
	rpcMethodGetBlockCount     = "getblockcount"
	rpcMethodGetBlockHash      = "getblockhash"
	rpcMethodGetBlock          = "getblock"
	rpcMethodGetBlockHeader    = "getblockheader"
	rpcMethodGetBlockchainInfo = "getblockchaininfo"
)

const (
	// defaultAPIKeyHeader is the header name used when api_key is set and
	// api_key_type is empty.
	defaultAPIKeyHeader = "x-api-key"

	// blockVerbosityUndoData is the getblock verbosity that returns parsed
	// transactions with per-input prevout undo data and per-transaction
	// fees. No rpcclient method issues it, so it travels through RawRequest.
	blockVerbosityUndoData = 3

	// undoDataMarker is the stable fragment of Bitcoin Core's -5 variant
	// reporting unavailable undo data ("Block not available (undo data is
	// not available)"). The other -5 variant ("Block not found") reports an
	// unknown hash, and the two failures route differently: unknown-hash
	// keeps its not-found text for walk-back matching, undo-data absence is
	// the archive-floor signal.
	undoDataMarker = "undo data"
)

var (
	// errUndoDataUnavailable tags the pruned-node outcome: this node's undo
	// data no longer covers the requested block. Its message deliberately
	// avoids the "not found" and "does not exist" fragments
	// pkg/errors.IsErrNotFound keys on, so the fetcher routes the outcome to
	// the archive fallback without a mistaken not-found classification.
	errUndoDataUnavailable = stderrors.New("block undo data is unavailable on this node")

	// errArchiveNotConfigured reports that the archive fallback endpoint has
	// never been configured, surfacing the env var instead of a confusing
	// client-unavailable plumbing error.
	errArchiveNotConfigured = stderrors.New("archive fallback endpoint is not configured; set BITCOIN_ARCHIVE_RPC_URL to serve pruned heights")
)

// Client is the Bitcoin Core JSON-RPC client. rpcclient runs in HTTP
// POST mode because Bitcoin Core has no websocket RPC surface; a second
// client serves the optional archive fallback endpoint for heights whose
// undo data the primary node has pruned away.
type Client struct {
	client        *rpcclient.Client
	archiveClient *rpcclient.Client
	rpcErrors     metric.Int64Counter
	dialTimeout   time.Duration
}

// NewClient creates the Bitcoin Core JSON-RPC client. rpc_url is
// accepted with or without an http/https scheme; TLS keys off the scheme
// because Core itself serves plain http while hosted endpoints sit behind
// https terminators. Basic auth (rpc_user and rpc_password) is preferred,
// and when only cookie_path is configured the credentials come from
// rpcclient's cookie handler, which re-reads the file on mtime change with a
// 30-second cache and keeps up with the daemon rotating it. api_key rides on
// every request as an extra header named after the lowercased api_key_type,
// defaulting to x-api-key. A second client is built for archive_rpc_url when
// configured. No request is issued here: HTTP POST connections are lazy, so
// construction is network-free, and the fetcher's Connect phase runs the
// reachability probe under the resolved dial timeout.
func NewClient(cfg config.BitcoinConfig) (*Client, error) {
	if cfg.RPCURL == "" {
		return nil, errors.NewConfigurationError("bitcoin", "NewClient", "rpc_url is empty", "", nil)
	}
	if !cfg.AuthConfigured() {
		return nil, errors.NewConfigurationError("bitcoin", "NewClient",
			"bitcoin core accepts only basic auth or a cookie file; set rpc_user and rpc_password, or cookie_path", "auth", nil)
	}

	rpcErrors, meterErr := otel.Meter("github.com/shinzonetwork/shinzo-generator-client/pkg/chains/bitcoin").Int64Counter("shinzo.generator.rpc.errors",
		metric.WithDescription("Failed Bitcoin JSON-RPC calls, by rpc.method and error.type. Heights past the tip are not counted."),
		metric.WithUnit("{error}"),
	)
	if meterErr != nil {
		otel.Handle(meterErr)
	}

	client, err := newRPCClient(cfg.RPCURL, &cfg)
	if err != nil {
		return nil, errors.NewRPCConnectionFailed("rpc", "NewClient", cfg.RPCURL, err)
	}
	var archiveClient *rpcclient.Client
	if cfg.ArchiveRPCURL != "" {
		archiveClient, err = newRPCClient(cfg.ArchiveRPCURL, &cfg)
		if err != nil {
			return nil, errors.NewRPCConnectionFailed("rpc", "NewClient", cfg.ArchiveRPCURL, err)
		}
	}

	logger.Sugar.Infof("Bitcoin Core client created for %s", utils.RedactEndpoint(cfg.RPCURL))
	return &Client{
		client:        client,
		archiveClient: archiveClient,
		rpcErrors:     rpcErrors,
		dialTimeout:   resolveDialTimeout(cfg.DialTimeoutSeconds),
	}, nil
}

// GetBlockCount returns the height of the best chain.
//
// Every method on this client carries a context for signature stability,
// but rpcclient cannot yet forward one per call (btcsuite btcd issue 2499
// and PR 2506 remain open), so the context is checked before each call only
// and the wire call stays bounded by the library's internal HTTP timeout.
// The upstream tracker gets re-checked before per-call cancellation is ever
// relied on.
func (c *Client) GetBlockCount(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	blockCount, err := c.client.GetBlockCount()
	err = wrapRPCError(err)
	c.countRPCError(ctx, rpcMethodGetBlockCount, err)
	if err != nil {
		return 0, fmt.Errorf("failed to get block count: %w", err)
	}
	return blockCount, nil
}

// GetBlockHash returns the content hash of the block at the given height.
// Heights past the tip surface as Core's -8 "Block height out of range" and
// pass through untouched; the fetcher owns wrapping them into not-found so
// the processor's tip-wait retry keeps matching, while the error counter
// skips the routine wait-for-tip outcome.
func (c *Client) GetBlockHash(ctx context.Context, height int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	blockHash, err := c.client.GetBlockHash(height)
	err = wrapRPCError(err)
	c.countRPCError(ctx, rpcMethodGetBlockHash, err)
	if err != nil {
		return "", fmt.Errorf("failed to get hash for height %d: %w", height, err)
	}
	return blockHash.String(), nil
}

// GetBlockVerbosity3 fetches the block with the given content hash at
// verbosity 3: parsed transactions whose inputs carry denormalized prevout
// undo data and whose decoded bodies carry the per-transaction fee. No
// rpcclient method exposes this verbosity (btcsuite btcd issue 2092 tracks
// the gap), so the call goes through the RawRequest escape hatch and decodes
// into this package's wire types; the escape hatch gets reconsidered after
// any future upstream verbosity-3 method lands.
func (c *Client) GetBlockVerbosity3(ctx context.Context, hash string) (*Block, error) {
	return c.getBlockVerbosity3(ctx, c.client, hash)
}

// GetBlockVerbosity3FromArchive issues the same verbosity-3 fetch against
// the archive fallback endpoint, for heights the primary node has pruned.
func (c *Client) GetBlockVerbosity3FromArchive(ctx context.Context, hash string) (*Block, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.archiveClient == nil {
		return nil, errArchiveNotConfigured
	}
	return c.getBlockVerbosity3(ctx, c.archiveClient, hash)
}

// ArchiveConfigured reports whether the archive fallback endpoint exists.
// The fetcher routes pruned-height failures to the archive under this flag.
func (c *Client) ArchiveConfigured() bool {
	return c.archiveClient != nil
}

// GetBlockHeaderByHash returns the header fields of the block with the given
// content hash through getblockheader verbose, a real header-by-hash RPC:
// the reason the generic reorg walk-back is fully feasible here. The
// requested hash is echoed back unchanged, so a locally recomputed hash can
// never mismatch Core's canonical answer. Unknown hashes keep Core's
// "-5: Block not found" text so pkg/errors.IsErrNotFound continues to
// classify them for the walk-back; a malformed hash is a caller bug and
// deliberately does not read as not-found.
func (c *Client) GetBlockHeaderByHash(ctx context.Context, hash string) (*VerboseBlockHeader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	blockHash, hashErr := chainhash.NewHashFromStr(hash)
	if hashErr != nil {
		return nil, errors.NewParsingFailed("bitcoin", "GetBlockHeaderByHash", "block hash", hashErr)
	}
	header, err := c.client.GetBlockHeaderVerbose(blockHash)
	err = wrapRPCError(err)
	c.countRPCError(ctx, rpcMethodGetBlockHeader, err)
	if err != nil {
		return nil, fmt.Errorf("failed to get header by hash %s: %w", hash, err)
	}
	return mapVerboseBlockHeader(header), nil
}

// GetChainInfo returns the node state that documents the archive floor:
// Pruned and PruneHeight carry whether the node runs pruned mode and the
// lowest block it still stores. The library's typed call supplies the
// fields, and its first invocation additionally probes the backend version
// (getinfo fails over to getnetworkinfo on Bitcoin Core) before caching it,
// so a cold client can issue three round trips for one logical call.
func (c *Client) GetChainInfo(ctx context.Context) (*ChainInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := c.client.GetBlockChainInfo()
	err = wrapRPCError(err)
	c.countRPCError(ctx, rpcMethodGetBlockchainInfo, err)
	if err != nil {
		return nil, fmt.Errorf("failed to get blockchain info: %w", err)
	}
	return mapChainInfo(info), nil
}

// Close shuts both clients down. rpcclient exposes Shutdown rather than an
// io.Closer-style method; afterwards every call fails fast with the
// library's shutdown sentinel instead of hanging on the wire.
func (c *Client) Close() error {
	c.client.Shutdown()
	if c.archiveClient != nil {
		c.archiveClient.Shutdown()
	}
	return nil
}

// getBlockVerbosity3 fetches and decodes the verbosity-3 payload against one
// endpoint.
func (c *Client) getBlockVerbosity3(ctx context.Context, client *rpcclient.Client, hash string) (*Block, error) {
	raw, err := c.rawGetBlockVerbosity3(ctx, client, hash)
	if err != nil {
		return nil, err
	}
	var block Block
	if decodeErr := json.Unmarshal(raw, &block); decodeErr != nil {
		return nil, fmt.Errorf("failed to decode getblock response for %s: %w", hash, decodeErr)
	}
	return &block, nil
}

// rawGetBlockVerbosity3 is the wire half of the verbosity-3 fetch, left
// package-private so tests can pin the raw payload pass-through while the
// public method decodes. No hash shape check happens here: Core answers
// unknown and malformed hashes alike with -5, message text aside, and the
// client classifies that one error rather than pre-flighting.
func (c *Client) rawGetBlockVerbosity3(ctx context.Context, client *rpcclient.Client, hash string) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hashParam, marshalErr := json.Marshal(hash)
	if marshalErr != nil {
		return nil, fmt.Errorf("failed to marshal getblock hash param: %w", marshalErr)
	}
	// Marshaling an int constant cannot fail.
	verbosityParam, _ := json.Marshal(blockVerbosityUndoData)
	raw, err := client.RawRequest(rpcMethodGetBlock, []json.RawMessage{hashParam, verbosityParam})
	err = wrapRPCError(err)
	c.countRPCError(ctx, rpcMethodGetBlock, err)
	if err != nil {
		return nil, fmt.Errorf("failed to get block %s: %w", hash, err)
	}
	return raw, nil
}

// wrapRPCError normalizes one failed RPC call before it leaves the client:
// transport failures get their endpoint URL redacted, rpcclient's untyped
// "status code: %d" transport string is promoted to a typed error for
// classification, and the pruned-node flavor of Core's -5 answer gets the
// undo-data sentinel wrapped underneath it. RPC errors are never retried by
// the client: rpcclient's HTTP POST transport retries only connection
// establishment, so one outcome reaches the caller on the first attempt.
func wrapRPCError(err error) error {
	if err == nil {
		return nil
	}
	if statusErr, ok := parseHTTPStatusError(err); ok {
		return statusErr
	}
	var rpcErr *btcjson.RPCError
	if stderrors.As(err, &rpcErr) &&
		rpcErr.Code == btcjson.ErrRPCBlockNotFound &&
		strings.Contains(rpcErr.Message, undoDataMarker) {
		return &undoDataRPCError{rpcErr: rpcErr}
	}
	return utils.RedactHTTPError(err)
}

// undoDataRPCError joins the pruned-node sentinel with the originating -5
// error. Custom Unwrap keeps both reachable the way errors.Is and errors.As
// expect, without the fmt verb the vet printf check rejects for a value
// receiver type. The Error text leads with the sentinel, so not-found
// matching on the composed message cannot read this outcome as not-found.
type undoDataRPCError struct {
	rpcErr *btcjson.RPCError
}

func (e *undoDataRPCError) Error() string {
	return errUndoDataUnavailable.Error() + ": " + e.rpcErr.Error()
}

func (e *undoDataRPCError) Unwrap() []error {
	return []error{errUndoDataUnavailable, e.rpcErr}
}

// httpStatusError carries the status code of a non-JSON-RPC transport
// response, so dashboards and callers classify by type instead of message
// text.
type httpStatusError struct {
	statusCode int
	cause      error
}

func (e *httpStatusError) Error() string {
	return e.cause.Error()
}

func (e *httpStatusError) Unwrap() error {
	return e.cause
}

// parseHTTPStatusError promotes the status-code error rpcclient's HTTP POST
// path builds for any response whose body is not a JSON-RPC envelope. That
// format crosses the library boundary as an untyped fmt error, so it is
// parsed once into the typed form; response bodies that already parse as
// JSON-RPC (Bitcoin Core reports RPC failures as HTTP 500 with an error
// object) are extracted by the library and never reach this path. The
// original error stays wrapped, so its full transport detail survives.
func parseHTTPStatusError(err error) (*httpStatusError, bool) {
	const statusPrefix = "status code: "
	message := err.Error()
	if !strings.HasPrefix(message, statusPrefix) {
		return nil, false
	}
	rest := message[len(statusPrefix):]
	before, _, ok := strings.Cut(rest, ",")
	if !ok {
		return nil, false
	}
	// A code that does not parse as an integer, or parses to the
	// impossible HTTP status zero, marks a different transport spelling
	// that belongs to pass-through handling rather than typed promotion.
	statusCode, _ := strconv.Atoi(strings.TrimSpace(before))
	if statusCode == 0 {
		return nil, false
	}
	return &httpStatusError{statusCode: statusCode, cause: err}, true
}

// countRPCError counts one failed RPC call at the error.type grain.
// Caller-aborted calls and heights past the tip (Core's -8, the routine
// outcome of polling ahead while waiting for the next block) are expected,
// not failures; the pruned-or-missing-undo-data outcome is deliberately
// counted as the archive-floor signal, alongside every other failure.
func (c *Client) countRPCError(ctx context.Context, method string, err error) {
	if c.rpcErrors == nil || err == nil || isContextError(err) {
		return
	}
	var rpcErr *btcjson.RPCError
	if stderrors.As(err, &rpcErr) && rpcErr.Code == btcjson.ErrRPCInvalidParameter {
		return
	}
	c.rpcErrors.Add(ctx, 1, metric.WithAttributes(
		semconv.RPCMethod(method),
		attribute.String("error.type", rpcErrorType(err)),
	))
}

// rpcErrorType buckets one failure for the error.type metric attribute, so
// dashboards separate rate limiting, HTTP transport failures, method
// availability, block unavailability, height range, generic RPC codes and
// context-driven timeouts.
func rpcErrorType(err error) string {
	if rpcErr, ok := stderrors.AsType[*btcjson.RPCError](err); ok {
		// The btcjson package defines the -32601 code as a *RPCError
		// variable while -5 and -8 are bare RPCErrorCode constants, so the
		// mixed spellings stick to the library's own definitions and keep
		// every comparison in RPCErrorCode.
		switch rpcErr.Code { //nolint:exhaustive // all remaining codes funnel into rpc_error through the default case
		case btcjson.ErrRPCMethodNotFound.Code:
			return "method_not_found"
		case btcjson.ErrRPCBlockNotFound:
			return "block_not_found"
		case btcjson.ErrRPCInvalidParameter:
			return "height_out_of_range"
		default:
			return "rpc_error"
		}
	}
	if httpErr, ok := stderrors.AsType[*httpStatusError](err); ok {
		if httpErr.statusCode == http.StatusTooManyRequests {
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
// deadline expiry, accepting the socket-deadline sentinel alongside the
// context ones the same way the evm adapter does.
func isContextError(err error) bool {
	return stderrors.Is(err, context.Canceled) ||
		stderrors.Is(err, context.DeadlineExceeded) ||
		stderrors.Is(err, os.ErrDeadlineExceeded)
}

// newRPCClient builds one HTTP POST client for a single endpoint, sharing
// the deployment's auth material and api-key header.
func newRPCClient(endpoint string, cfg *config.BitcoinConfig) (*rpcclient.Client, error) {
	conn, err := connConfigFor(endpoint, cfg)
	if err != nil {
		return nil, err
	}
	return rpcclient.New(conn, nil)
}

// connConfigFor maps one endpoint and the bitcoin config onto rpcclient's
// ConnConfig: HTTP POST mode always, TLS keyed off the URL scheme, auth
// from the basic-auth pair when both halves are set and from rpcclient's
// cookie handler otherwise, and the api-key header through ExtraHeaders.
func connConfigFor(endpoint string, cfg *config.BitcoinConfig) (*rpcclient.ConnConfig, error) {
	host, disableTLS, hostErr := rpcEndpointHost(endpoint)
	if hostErr != nil {
		return nil, hostErr
	}
	conn := &rpcclient.ConnConfig{
		Host:         host,
		HTTPPostMode: true,
		DisableTLS:   disableTLS,
		User:         cfg.RPCUser,
		Pass:         cfg.RPCPassword,
		ExtraHeaders: apiKeyHeader(cfg),
	}
	// rpcclient falls through to cookie auth whenever Pass is empty, so the
	// cookie path only needs wiring when basic auth is not complete.
	if cfg.RPCUser == "" || cfg.RPCPassword == "" {
		conn.CookiePath = cfg.CookiePath
	}
	return conn, nil
}

// rpcEndpointHost maps an endpoint onto rpcclient's Host value plus a TLS
// flag. Schemed http/https endpoints are rebuilt as hostname[:port] plus
// the escaped path and query, and an endpoint naming no port gains the
// scheme default (443 for https, 80 for http): btcd's HTTP POST transport
// derives its dial address from Host alone, where a missing port decays
// into port 0 and every connect attempt fails before reaching the network.
// Credentials embedded in the URL are dropped: basic auth is the configured
// rpc_user and rpc_password pair, not a userinfo spelling. Bare endpoints
// without a scheme keep their literal spelling and degrade to plain http,
// the way Core's own default deployment is written, but they must then
// carry a numeric port, since a portless bare host decays the same way.
// Unknown schemes are a configuration error.
func rpcEndpointHost(endpoint string) (host string, disableTLS bool, err error) {
	parsed, parseErr := url.Parse(endpoint)
	switch {
	case parseErr != nil || parsed.Host == "":
		// url.Parse rejects "host:port" input ("first path segment cannot
		// contain colon") and reads a bare hostname as a path, so both bare
		// spellings land here and are validated directly rather than being
		// handed to btcd as a portless Host.
		if !isBareHostPort(endpoint) {
			return "", true, fmt.Errorf("rpc_url %q is neither host:port nor an http:// or https:// URL", endpoint)
		}
		return endpoint, true, nil //nolint:nilerr // the parse outcome selects input spelling, not a call failure
	case parsed.Scheme == "https":
		return schemeAuthority(parsed, "443"), false, nil
	case parsed.Scheme == "http":
		return schemeAuthority(parsed, "80"), true, nil
	default:
		return "", true, fmt.Errorf("rpc_url scheme %q is not http or https", parsed.Scheme)
	}
}

// schemeAuthority rebuilds the authority rpcclient needs: the hostname with
// an explicit or scheme-default port, followed by the escaped path and
// query so hosted-provider endpoints keep their addressable suffix. The
// fragment is dropped: it is never sent over the wire and cannot address an
// RPC surface.
func schemeAuthority(u *url.URL, defaultPort string) string {
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), defaultPort)
	}
	if u.RawQuery == "" {
		return host + u.EscapedPath()
	}
	return host + u.EscapedPath() + "?" + u.RawQuery
}

// isBareHostPort reports whether endpoint is the bare host:port spelling
// with a numeric port. Anything else would hand btcd a Host without a
// usable port and decay into the port-0 dial, so it is rejected instead.
func isBareHostPort(endpoint string) bool {
	host, port, splitErr := net.SplitHostPort(endpoint)
	if splitErr != nil || host == "" || port == "" {
		return false
	}
	_, portErr := strconv.Atoi(port)
	return portErr == nil
}

// apiKeyHeader materializes the hosted-provider header when api_key is set:
// the lowercased api_key_type is the header name, falling back to
// x-api-key when the type is empty. A nil map otherwise, so rpcclient
// writes no extra headers.
func apiKeyHeader(cfg *config.BitcoinConfig) map[string]string {
	if cfg.APIKey == "" {
		return nil
	}
	headerName := strings.ToLower(strings.TrimSpace(cfg.APIKeyType))
	if headerName == "" {
		headerName = defaultAPIKeyHeader
	}
	return map[string]string{headerName: cfg.APIKey}
}

// resolveDialTimeout converts the configured seconds into the duration the
// fetcher bounds its startup health check with. Zero and negative values
// fall back on the deployment default rather than meaning unbounded, since
// a Bitcoin Core node still deep in initial sync is a common first-run
// state, not an outage.
func resolveDialTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return time.Duration(config.DefaultBitcoinDialTimeoutSeconds) * time.Second
	}
	return time.Duration(seconds) * time.Second
}

// mapVerboseBlockHeader copies the consumed getblockheader fields into the
// local mapping target, ending at this call the reach of btcjson types.
func mapVerboseBlockHeader(res *btcjson.GetBlockHeaderVerboseResult) *VerboseBlockHeader {
	if res == nil {
		return nil
	}
	return &VerboseBlockHeader{
		Hash:              res.Hash,
		Confirmations:     res.Confirmations,
		Height:            int64(res.Height),
		Version:           uint32(res.Version), //nolint:gosec // Core defines the block version as a 32-bit field, so a decoded int32 is never negative here
		MerkleRoot:        res.MerkleRoot,
		Time:              res.Time,
		Nonce:             res.Nonce,
		Bits:              res.Bits,
		Difficulty:        res.Difficulty,
		PreviousBlockHash: res.PreviousHash,
		NextBlockHash:     res.NextHash,
	}
}

// mapChainInfo copies the archive-floor fields of the getblockchaininfo
// result into the local mapping target, ending at this call the reach of
// btcjson types.
func mapChainInfo(res *btcjson.GetBlockChainInfoResult) *ChainInfo {
	if res == nil {
		return nil
	}
	return &ChainInfo{
		Chain:         res.Chain,
		Blocks:        int64(res.Blocks),
		Headers:       int64(res.Headers),
		BestBlockHash: res.BestBlockHash,
		Pruned:        res.Pruned,
		PruneHeight:   int64(res.PruneHeight),
	}
}
