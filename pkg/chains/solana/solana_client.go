package solana

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"

	solana "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/gagliardetto/solana-go/rpc/jsonrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// Solana JSON-RPC server error codes that classify a missing-block outcome.
// A missing slot surfaces both as a null result and as dedicated error codes
// depending on where the node looked, so every variant must be mapped.
const (
	// rpcCodeBlockCleanedUp: the block existed but was pruned below the
	// node's ledger floor (full nodes keep only recent history).
	rpcCodeBlockCleanedUp = -32001
	// rpcCodeBlockNotAvailable: the slot is not in the node's blockstore,
	// typical for slots that are not yet processed or were skipped.
	rpcCodeBlockNotAvailable = -32004
	// rpcCodeSlotSkipped: the slot was skipped by its leader or lost in a
	// ledger jump to a recent snapshot.
	rpcCodeSlotSkipped = -32007
	// rpcCodeLongTermStorageSlotSkipped: the slot is absent from the node's
	// long-term storage (e.g. BigTable).
	rpcCodeLongTermStorageSlotSkipped = -32009
	// rpcCodeUnsupportedTransactionVersion: the block contains versioned
	// transactions but maxSupportedTransactionVersion was absent or too low.
	// Unreachable in production because the client always sends the parameter.
	rpcCodeUnsupportedTransactionVersion = -32015
	// jsonRPCMethodNotFound is the JSON-RPC standard error code pair with
	// method_missing diagnostics: a node without a method (e.g. an archive
	// node proxy without getFirstAvailableBlock) is diagnosable as such.
	jsonRPCMethodNotFound = -32601
)

// Sentinel errors returned by Client so the fetcher can apply policy
// (tip comparison, archive routing) without parsing library error strings.
// None of these messages contain "not found": that substring belongs to the
// transient block-pending condition and drives the processor's infinite
// retry loop, which permanently-missing blocks must never trigger.
var (
	errNotConfirmed         = stderrors.New("block not confirmed at the requested commitment")
	errSlotSkipped          = stderrors.New("slot skipped or missing from node storage")
	errBlockNotAvailable    = stderrors.New("block not available on node")
	errBlockCleanedUp       = stderrors.New("block cleaned up from node ledger")
	errArchiveNotConfigured = stderrors.New("archive RPC endpoint not configured")
)

// httpStatusSampleLimit bounds the body sample carried by httpStatusError:
// a provider error page must not dump megabytes into the log line.
const httpStatusSampleLimit = 200

// tcpKeepAliveInterval is the interval between TCP keep-alive probes on
// pooled connections to the RPC endpoints: long-lived idle connections must
// survive NAT timeouts and notice a half-open peer without per-request
// dialing.
const tcpKeepAliveInterval = 30 * time.Second

// ClientOptions carries the dial-time parameters for Client.
type ClientOptions struct {
	// RPCURL is the main JSON-RPC endpoint. Required.
	RPCURL string
	// ArchiveRPCURL optionally points at an archive node for slots below the
	// main endpoint's ledger floor. Empty disables archive routing.
	ArchiveRPCURL string
	// Commitment bounds block visibility; only "confirmed" and "finalized"
	// are valid (validated by the config layer).
	Commitment string
	// MaxSupportedTransactionVersion is mandatory on getBlock calls: blocks
	// containing versioned transactions error out without it.
	MaxSupportedTransactionVersion uint64
	// Rewards controls whether the block rewards array is fetched.
	Rewards bool
	// APIKey / APIKeyType add a static header to every request for
	// header-authenticated RPC providers. Empty key = no header.
	APIKey     string
	APIKeyType string
	// DialTimeoutSeconds bounds the per-phase connection setup (TCP dial,
	// TLS handshake, time to the first response byte). 0 or negative falls
	// back to config.DefaultSolanaDialTimeoutSeconds. There is deliberately
	// no whole-request timeout: a full-fidelity hot-slot body streams for
	// unbounded time after the first byte, so only the connection phases
	// are bounded.
	DialTimeoutSeconds int
}

// Client wraps the JSON-RPC transport for Solana nodes and converts
// responses into the package's local types, keeping solana-go SDK types at
// the call boundary. It is a read-only client, so every RPC it issues is
// safe to re-issue by the processor's retry policy, and it is safe for
// concurrent use across slots. There is no retry or backoff inside the
// client: those live in the shared block processor.
type Client struct {
	client        *rpc.Client
	archiveClient *rpc.Client

	commitment   rpc.CommitmentType
	maxTxVersion *uint64
	rewards      bool
	rpcErrors    metric.Int64Counter
}

// NewClient builds the HTTP transport (per-phase dial timeouts, optional
// API-key header) and the block client. No network I/O happens here: the
// HTTP layer dials lazily, so callers perform one cheap RPC (the fetcher's
// connect health check) to fail fast on a bad endpoint or key.
func NewClient(ctx context.Context, opts ClientOptions) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.NewRPCConnectionFailed("solana", "NewClient", "all endpoints",
			fmt.Errorf("construction context cancelled before connecting: %w", err))
	}
	if opts.RPCURL == "" {
		return nil, errors.NewConfigurationError("solana", "NewClient",
			"solana rpc_url is empty; set SOLANA_RPC_URL or the config rpc_url", "", nil)
	}

	rpcErrors, meterErr := otel.Meter("github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana").Int64Counter("shinzo.generator.rpc.errors",
		metric.WithDescription("Failed Solana JSON-RPC calls, by rpc.method and error.type. Not-yet-produced and skipped slots are not counted."),
		metric.WithUnit("{error}"),
	)
	if meterErr != nil {
		otel.Handle(meterErr)
	}

	c := &Client{
		commitment:   rpc.CommitmentType(opts.Commitment),
		maxTxVersion: &opts.MaxSupportedTransactionVersion,
		rewards:      opts.Rewards,
		rpcErrors:    rpcErrors,
	}

	c.client = rpc.NewWithCustomRPCClient(jsonrpc.NewClientWithOpts(opts.RPCURL, rpcClientOpts(opts)))

	if opts.ArchiveRPCURL != "" {
		c.archiveClient = rpc.NewWithCustomRPCClient(jsonrpc.NewClientWithOpts(opts.ArchiveRPCURL, rpcClientOpts(opts)))
	}

	return c, nil
}

// rpcClientOpts builds the jsonrpc options for one endpoint: a dial-timeout
// HTTP transport plus the optional API-key header. A separate transport per
// endpoint keeps idle-connection pools isolated.
func rpcClientOpts(opts ClientOptions) *jsonrpc.RPCClientOpts {
	return &jsonrpc.RPCClientOpts{
		HTTPClient:   &http.Client{Transport: newHTTPTransport(opts.DialTimeoutSeconds)},
		CustomHeader: authHeader(opts.APIKey, opts.APIKeyType),
	}
}

// authHeader builds the static header for header-authenticated providers.
// An empty key yields no header; an empty type falls back to the customary
// x-api-key (matching the EVM adapter's api_key/api_key_type handling).
func authHeader(apiKey, apiKeyType string) http.Header {
	if apiKey == "" {
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(apiKeyType))
	if name == "" {
		name = "x-api-key"
	}
	return http.Header{name: []string{apiKey}}
}

// newHTTPTransport wraps the default-like transport with per-phase dial
// budgets: TCP dial, TLS handshake, and time to the first response byte
// share the configured dial timeout. The body itself streams without a
// budget: a full-fidelity hot-slot response can legitimately take longer to
// transfer than its time-to-first-byte, and a whole-request timeout would
// kill the read mid-body.
func newHTTPTransport(dialTimeoutSeconds int) http.RoundTripper {
	if dialTimeoutSeconds <= 0 {
		dialTimeoutSeconds = config.DefaultSolanaDialTimeoutSeconds
	}
	timeout := time.Duration(dialTimeoutSeconds) * time.Second
	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: tcpKeepAliveInterval,
	}
	return &httpStatusTransport{
		base: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
		},
	}
}

// httpStatusTransport turns non-2xx HTTP responses into typed errors. Solana
// providers return rate-limit (429) and auth failures at the HTTP layer,
// while JSON-RPC-level errors ride 200 envelopes, so the two layers can be
// classified distinctly (the same shape Tron adopted).
type httpStatusTransport struct {
	base http.RoundTripper
}

func (t *httpStatusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode > 299 {
		sample := readBodySample(resp)
		return nil, &httpStatusError{
			status: resp.StatusCode,
			msg:    sample,
		}
	}
	return resp, nil
}

// httpStatusError is a non-2xx provider status with a capped body sample.
// Its message carries no URL, so it is safe to log verbatim.
type httpStatusError struct {
	status int
	msg    string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("http status %d: %s", e.status, e.msg)
}

// readBodySample reads at most httpStatusSampleLimit bytes and closes the
// body, so a failed response never keeps a pooled connection.
func readBodySample(resp *http.Response) string {
	if resp.Body == nil {
		return ""
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, httpStatusSampleLimit))
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return string(raw)
}

// GetBlock fetches a slot with full fidelity: complete transactions with
// metadata (including inner instructions and token balances), and rewards
// per configuration. Transport-level outcomes are mapped to package
// sentinels so the fetcher can classify skip vs transient without touching
// library error strings.
func (c *Client) GetBlock(ctx context.Context, slot uint64) (*Block, error) {
	out, err := c.getBlock(ctx, slot, c.client)
	c.countRPCError(ctx, "getBlock", err)
	if err != nil {
		return nil, err
	}
	return c.convertBlock(out, slot)
}

// GetBlockFromArchive fetches a slot from the archive endpoint, used when
// the main endpoint reports the block pruned below its ledger floor. Fails
// with errArchiveNotConfigured when no archive endpoint is configured (no
// RPC is issued).
func (c *Client) GetBlockFromArchive(ctx context.Context, slot uint64) (*Block, error) {
	if c.archiveClient == nil {
		return nil, errArchiveNotConfigured
	}
	out, err := c.getBlock(ctx, slot, c.archiveClient)
	c.countRPCError(ctx, "getBlockFromArchive", err)
	if err != nil {
		return nil, err
	}
	return c.convertBlock(out, slot)
}

// getBlock issues the shared getBlock call shape against either endpoint.
// Encoding is base64: the SDK's battle-tested decode path, yielding the same
// fully-parsed transaction structs as the json encoding.
func (c *Client) getBlock(ctx context.Context, slot uint64, client *rpc.Client) (*rpc.GetBlockResult, error) {
	out, err := client.GetBlockWithOpts(ctx, slot, &rpc.GetBlockOpts{
		Encoding:                       solana.EncodingBase64,
		TransactionDetails:             rpc.TransactionDetailsFull,
		Commitment:                     c.commitment,
		Rewards:                        &c.rewards,
		MaxSupportedTransactionVersion: c.maxTxVersion,
	})
	if err != nil {
		return nil, classifyBlockError(fmt.Errorf("getBlock %d failed: %w",
			slot, utils.RedactHTTPError(err)))
	}
	if out == nil {
		// Defensive: the library converts a null result into its own
		// sentinel before returning, so this path should be unreachable.
		return nil, errNotConfirmed
	}
	return out, nil
}

// classifyBlockError maps library-reported transport outcomes to package
// sentinels. Library sentinel comparison precedes error-code inspection
// because a null getBlock result arrives as a plain error value, not as an
// RPCError object.
func classifyBlockError(err error) error {
	if stderrors.Is(err, rpc.ErrNotConfirmed) {
		return errNotConfirmed
	}

	var rpcErr *jsonrpc.RPCError
	if !stderrors.As(err, &rpcErr) {
		return err
	}

	switch rpcErr.Code {
	case rpcCodeSlotSkipped, rpcCodeLongTermStorageSlotSkipped:
		return fmt.Errorf("%w: %s", errSlotSkipped, rpcErr.Message)
	case rpcCodeBlockNotAvailable:
		return fmt.Errorf("%w: %s", errBlockNotAvailable, rpcErr.Message)
	case rpcCodeBlockCleanedUp:
		return fmt.Errorf("%w: %s", errBlockCleanedUp, rpcErr.Message)
	case rpcCodeUnsupportedTransactionVersion:
		return fmt.Errorf("getBlock rejected by node: %s", rpcErr.Message)
	default:
		return fmt.Errorf("getBlock RPC error %d: %s", rpcErr.Code, rpcErr.Message)
	}
}

// GetSlot returns the current tip at the configured commitment. On Solana
// the slot is the fetch height, so this both bounds backfill and serves the
// chains.Fetcher tip query.
func (c *Client) GetSlot(ctx context.Context) (uint64, error) {
	slot, err := c.client.GetSlot(ctx, c.commitment)
	c.countRPCError(ctx, "getSlot", err)
	if err != nil {
		return 0, fmt.Errorf("getSlot failed: %w", utils.RedactHTTPError(err))
	}
	return slot, nil
}

// GetFirstAvailableBlock returns the slot of the lowest confirmed block the
// endpoint has not purged. Full nodes hold only days of ledger, so this
// documents the archive floor behind the archive_rpc_url routing: once
// backfill reaches it, older slots only come from an archive endpoint.
func (c *Client) GetFirstAvailableBlock(ctx context.Context) (uint64, error) {
	slot, err := c.client.GetFirstAvailableBlock(ctx)
	c.countRPCError(ctx, "getFirstAvailableBlock", err)
	if err != nil {
		return 0, fmt.Errorf("getFirstAvailableBlock failed: %w", utils.RedactHTTPError(err))
	}
	return slot, nil
}

// Close releases idle pooled connections on both endpoints.
func (c *Client) Close() error {
	if c.client != nil {
		_ = c.client.Close()
	}
	if c.archiveClient != nil {
		_ = c.archiveClient.Close()
	}
	return nil
}

// convertBlock maps the SDK block response into the package's local types.
// Null-safe: blockTime/commission/computeUnits may be null, and empty
// collections stay nil rather than being invented.
func (c *Client) convertBlock(result *rpc.GetBlockResult, slot uint64) (*Block, error) {
	transactions := make([]Transaction, 0, len(result.Transactions))
	for i := range result.Transactions {
		tx, err := convertTransactionWithMeta(&result.Transactions[i], slot, i)
		if err != nil {
			return nil, err
		}
		transactions = append(transactions, *tx)
	}

	block := &Block{
		Slot:              slot,
		Blockhash:         result.Blockhash.String(),
		PreviousBlockhash: result.PreviousBlockhash.String(),
		Transactions:      transactions,
		Rewards:           convertRewards(result.Rewards),
	}
	if result.BlockTime != nil {
		unix := int64(*result.BlockTime)
		block.BlockTime = &unix
	}
	return block, nil
}

// convertTransactionWithMeta decodes one versioned transaction and its
// metadata. Program IDs and account lists are resolved from compiled
// instruction indices against the static keys plus the ALT-loaded
// writable/readonly keys, in the order the runtime concatenates them.
func convertTransactionWithMeta(twm *rpc.TransactionWithMeta, slot uint64, txIndex int) (*Transaction, error) {
	tx, err := twm.GetTransaction()
	if err != nil {
		return nil, fmt.Errorf("failed to decode transaction %d of slot %d: %w", txIndex, slot, err)
	}

	staticKeys := pubkeysToStrings(tx.Message.AccountKeys)
	var loadedWritable, loadedReadonly []string
	if twm.Meta != nil {
		loadedWritable = pubkeysToStrings(twm.Meta.LoadedAddresses.Writable)
		loadedReadonly = pubkeysToStrings(twm.Meta.LoadedAddresses.ReadOnly)
	}

	committed := make([]string, 0, len(staticKeys)+len(loadedWritable)+len(loadedReadonly))
	committed = append(committed, staticKeys...)
	committed = append(committed, loadedWritable...)
	committed = append(committed, loadedReadonly...)

	out := &Transaction{
		Signature:       firstSignature(tx),
		RecentBlockhash: tx.Message.RecentBlockhash.String(),
		AccountKeys:     staticKeys,
		LoadedAddresses: append(loadedWritable, loadedReadonly...),
		Instructions:    convertOuterInstructions(tx.Message.Instructions, committed),
	}

	if twm.Version < 0 {
		out.Version = "legacy"
	} else {
		out.Version = strconv.Itoa(int(twm.Version))
	}

	if twm.Meta == nil {
		return out, nil
	}

	if twm.Meta.Err != nil {
		if raw, err := json.Marshal(twm.Meta.Err); err == nil {
			out.Err = string(raw)
		} else {
			out.Err = fmt.Sprintf("%v", twm.Meta.Err)
		}
	}
	out.Fee = twm.Meta.Fee
	out.ComputeUnitsConsumed = twm.Meta.ComputeUnitsConsumed
	out.LogMessages = twm.Meta.LogMessages
	out.PreBalances = twm.Meta.PreBalances
	out.PostBalances = twm.Meta.PostBalances
	out.PreTokenBalances = convertTokenBalances(twm.Meta.PreTokenBalances)
	out.PostTokenBalances = convertTokenBalances(twm.Meta.PostTokenBalances)
	out.InnerInstructions = convertInnerInstructions(twm.Meta.InnerInstructions, committed)

	return out, nil
}

// firstSignature extracts the transaction's primary signature: the first
// entry of the decoded signature list. An empty transaction yields "".
func firstSignature(tx *solana.Transaction) string {
	if len(tx.Signatures) == 0 {
		return ""
	}
	return tx.Signatures[0].String()
}

// pubkeysToStrings maps a slice of SDK pubkeys to base58 strings.
func pubkeysToStrings(keys solana.PublicKeySlice) []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, len(keys))
	for i, key := range keys {
		out[i] = key.String()
	}
	return out
}

// convertOuterInstructions resolves compiled outer instructions against the
// full account-key list. Outer instructions have a nil StackHeight (the RPC
// only reports stack depth for CPI instructions).
func convertOuterInstructions(instrs []solana.CompiledInstruction, keys []string) []Instruction {
	if len(instrs) == 0 {
		return nil
	}
	out := make([]Instruction, 0, len(instrs))
	for _, ci := range instrs {
		out = append(out, newInstruction(
			uint64(ci.ProgramIDIndex), ci.Accounts, ci.Data.String(),
			nil, keys,
		))
	}
	return out
}

// convertInnerInstructions resolves one transaction's CPI instruction groups,
// each tied to its parent outer instruction's index.
func convertInnerInstructions(groups []rpc.InnerInstruction, keys []string) []InnerInstructionGroup {
	if len(groups) == 0 {
		return nil
	}
	out := make([]InnerInstructionGroup, 0, len(groups))
	for _, group := range groups {
		inner := make([]Instruction, 0, len(group.Instructions))
		for _, ci := range group.Instructions {
			stackHeight := ci.StackHeight
			inner = append(inner, newInstruction(
				uint64(ci.ProgramIDIndex), ci.Accounts, ci.Data.String(),
				&stackHeight, keys,
			))
		}
		out = append(out, InnerInstructionGroup{Index: group.Index, Instructions: inner})
	}
	return out
}

// newInstruction assembles a resolved instruction. Account-index resolution
// degrades to an empty string on out-of-range indices instead of failing the
// whole block: node data is expected well-formed, and a single malformed
// entry must not cost an otherwise complete document set (the EVM adapter
// makes the same trade with its zero-address fallback).
func newInstruction(programIDIndex uint64, accountIdx []uint16, data string, stackHeight *uint16, keys []string) Instruction {
	return Instruction{
		ProgramID:   resolveKey(keys, programIDIndex),
		Accounts:    resolveAccounts(keys, accountIdx),
		Data:        data,
		StackHeight: stackHeight,
	}
}

// resolveKey returns the base58 pubkey at the given index, or "" when
// out of bounds.
func resolveKey(keys []string, idx uint64) string {
	if idx < uint64(len(keys)) {
		return keys[idx]
	}
	return ""
}

// resolveAccounts maps compiled account indices to base58 pubkeys, keeping
// order and collapsing out-of-range entries to "".
func resolveAccounts(keys []string, idxs []uint16) []string {
	if len(idxs) == 0 {
		return nil
	}
	out := make([]string, 0, len(idxs))
	for _, idx := range idxs {
		out = append(out, resolveKey(keys, uint64(idx)))
	}
	return out
}

// convertTokenBalances maps pre/postTokenBalances entries; amount stays a
// raw string because token amounts can exceed int64.
func convertTokenBalances(balances []rpc.TokenBalance) []TokenBalance {
	if len(balances) == 0 {
		return nil
	}
	out := make([]TokenBalance, 0, len(balances))
	for _, tb := range balances {
		entry := TokenBalance{
			AccountIndex: tb.AccountIndex,
			Mint:         tb.Mint.String(),
		}
		if tb.Owner != nil {
			entry.Owner = tb.Owner.String()
		}
		if tb.ProgramId != nil {
			entry.ProgramID = tb.ProgramId.String()
		}
		if tb.UiTokenAmount != nil {
			entry.Amount = tb.UiTokenAmount.Amount
			entry.Decimals = tb.UiTokenAmount.Decimals
		}
		out = append(out, entry)
	}
	return out
}

// convertRewards maps per-block reward entries, keeping the signed lamports
// and the pointer-valued commission (only voting/staking rewards have one).
func convertRewards(rewards []rpc.BlockReward) []Reward {
	if len(rewards) == 0 {
		return nil
	}
	out := make([]Reward, 0, len(rewards))
	for _, r := range rewards {
		entry := Reward{
			Pubkey:      r.Pubkey.String(),
			Lamports:    r.Lamports,
			PostBalance: r.PostBalance,
			RewardType:  string(r.RewardType),
		}
		if r.Commission != nil {
			commission := *r.Commission
			entry.Commission = &commission
		}
		out = append(out, entry)
	}
	return out
}

// countRPCError counts a failed RPC call. Not-yet-produced slots (null
// result), not-available and skipped slots are routine near-tip outcomes on
// Solana, so they are not failures; a cleaned-up block is counted because it
// signals the archive floor being reached.
func (c *Client) countRPCError(ctx context.Context, method string, err error) {
	if c.rpcErrors == nil || err == nil || isContextError(err) || isExpectedOutcome(err) {
		return
	}
	c.rpcErrors.Add(ctx, 1, metric.WithAttributes(
		semconv.RPCMethod(method),
		attribute.String("error.type", rpcErrorType(err)),
	))
}

// isExpectedOutcome reports whether err classifies a routine missing-slot
// condition instead of a failure (see countRPCError).
func isExpectedOutcome(err error) bool {
	for _, sentinel := range []error{
		errNotConfirmed,
		errBlockNotAvailable,
		errSlotSkipped,
		errArchiveNotConfigured,
	} {
		if stderrors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

// rpcErrorType maps err to an error.type value, so dashboards can separate
// rate-limited provider responses (429) and method-missing endpoints from
// other failures, matching the EVM dashboard vocabulary.
func rpcErrorType(err error) string {
	if httpErr, ok := stderrors.AsType[*httpStatusError](err); ok {
		if httpErr.status == http.StatusTooManyRequests {
			return "rate_limited"
		}
		return "http_error"
	}
	if rpcErr, ok := stderrors.AsType[*jsonrpc.RPCError](err); ok {
		if rpcErr.Code == jsonRPCMethodNotFound {
			return "method_not_found"
		}
		return "rpc_error"
	}
	if isContextError(err) {
		return "timeout"
	}
	return "_OTHER"
}

// isContextError reports whether err was caused by context cancellation or
// deadline expiry.
func isContextError(err error) bool {
	return stderrors.Is(err, context.Canceled) ||
		stderrors.Is(err, context.DeadlineExceeded) ||
		stderrors.Is(err, os.ErrDeadlineExceeded)
}
