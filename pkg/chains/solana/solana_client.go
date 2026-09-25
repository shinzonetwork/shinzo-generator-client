package solana

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"

	solana "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/gagliardetto/solana-go/rpc/jsonrpc"
)

// Solana JSON-RPC server error codes that classify a missing-block outcome.
// A missing slot surfaces both as a null result and as dedicated error codes
// depending on where the node looked, so every variant must be mapped.
const (
	// rpcCodeBlockCleanedUp: the block existed but was pruned below the
	// node's ledger floor (full nodes keep only recent history).
	rpcCodeBlockCleanedUp = -32001
	// rpcCodeBlockNotAvailable: the slot is not in the node's blockstore —
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

const (
	// retryTransportMaxAttempts bounds rate-limit retries inside the HTTP
	// transport so a persistently throttled provider still surfaces errors
	// to the fetcher/processor retry layers instead of blocking forever.
	retryTransportMaxAttempts = 5
	// retryTransportMaxWait caps a single Retry-After sleep: a hostile or
	// buggy Retry-After value must not park a worker for minutes.
	retryTransportMaxWait = 30 * time.Second
	// retryTransportBackoff is the exponential base applied when the
	// throttling response carries no Retry-After header.
	retryTransportBackoff = 500 * time.Millisecond
)

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
}

// Client wraps the JSON-RPC transport for Solana nodes and converts
// responses into the package's local types, keeping solana-go SDK types at
// the call boundary. It is a read-only client: every RPC it issues is safe
// to re-issue after a transport-level rate-limit retry, and it is safe for
// concurrent use across slots.
type Client struct {
	client        *rpc.Client
	archiveClient *rpc.Client

	commitment   rpc.CommitmentType
	maxTxVersion *uint64
	rewards      bool
}

// NewClient builds the HTTP transport (rate-limit retry, optional
// API-key header) and the block client. No network I/O happens here — the
// HTTP layer dials lazily — so callers perform one cheap RPC (the fetcher's
// Connect health check) to fail fast on a bad endpoint or key.
func NewClient(ctx context.Context, opts ClientOptions) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.NewRPCConnectionFailed("rpc", "NewClient", "all endpoints",
			fmt.Errorf("construction context cancelled before connecting: %w", err))
	}
	if opts.RPCURL == "" {
		return nil, errors.NewConfigurationError("solana", "NewClient",
			"solana rpc_url is empty; set SOLANA_RPC_URL or the config rpc_url", "", nil)
	}

	c := &Client{
		commitment:   rpc.CommitmentType(opts.Commitment),
		maxTxVersion: &opts.MaxSupportedTransactionVersion,
		rewards:      opts.Rewards,
	}

	c.client = rpc.NewWithCustomRPCClient(jsonrpc.NewClientWithOpts(opts.RPCURL, rpcClientOpts(opts)))

	if opts.ArchiveRPCURL != "" {
		c.archiveClient = rpc.NewWithCustomRPCClient(jsonrpc.NewClientWithOpts(opts.ArchiveRPCURL, rpcClientOpts(opts)))
	}

	return c, nil
}

// rpcClientOpts builds the jsonrpc options for one endpoint: a fresh
// rate-limit-retrying HTTP transport plus the optional API-key header. A
// separate transport per endpoint keeps idle-connection pools isolated.
func rpcClientOpts(opts ClientOptions) *jsonrpc.RPCClientOpts {
	header := http.Header{}
	if opts.APIKey != "" {
		name := strings.ToLower(strings.TrimSpace(opts.APIKeyType))
		if name == "" {
			name = "x-api-key"
		}
		header.Set(name, opts.APIKey)
	}
	return &jsonrpc.RPCClientOpts{
		HTTPClient:   &http.Client{Transport: newRetryTransport(http.DefaultTransport)},
		CustomHeader: header,
	}
}

// GetBlock fetches a slot with full fidelity: complete transactions with
// metadata (including inner instructions and token balances), and rewards
// per configuration. Transport-level outcomes are mapped to package
// sentinels so the fetcher can classify skip-vs-transient without touching
// library error strings. Each call records its network round-trip and local
// conversion time into the rpcStats collector carried by ctx (no-op when
// absent).
func (c *Client) GetBlock(ctx context.Context, slot uint64) (*Block, error) {
	stats := rpcStatsFrom(ctx)
	netStart := time.Now()
	out, err := c.getBlock(ctx, slot, c.client)
	netDur := time.Since(netStart)
	if err != nil {
		stats.record("GetBlock", netDur, 0, true)
		return nil, err
	}

	localStart := time.Now()
	block, convErr := c.convertBlock(out, slot)
	if convErr != nil {
		stats.record("GetBlock", netDur, 0, true)
		return nil, convErr
	}
	stats.record("GetBlock", netDur, time.Since(localStart), false)
	return block, nil
}

// GetBlockFromArchive fetches a slot from the archive endpoint, used when
// the main endpoint reports the block pruned below its ledger floor. Fails
// with errArchiveNotConfigured when no archive endpoint is configured (no
// RPC is issued, so nothing is recorded for that case).
func (c *Client) GetBlockFromArchive(ctx context.Context, slot uint64) (*Block, error) {
	if c.archiveClient == nil {
		return nil, errArchiveNotConfigured
	}
	stats := rpcStatsFrom(ctx)
	netStart := time.Now()
	out, err := c.getBlock(ctx, slot, c.archiveClient)
	netDur := time.Since(netStart)
	if err != nil {
		stats.record("GetBlockFromArchive", netDur, 0, true)
		return nil, err
	}

	localStart := time.Now()
	block, convErr := c.convertBlock(out, slot)
	if convErr != nil {
		stats.record("GetBlockFromArchive", netDur, 0, true)
		return nil, convErr
	}
	stats.record("GetBlockFromArchive", netDur, time.Since(localStart), false)
	return block, nil
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
		return nil, classifyBlockError(fmt.Errorf("getBlock %d failed: %w", slot, err))
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
// chains.Fetcher tip query. There is no local conversion, so only the
// network round-trip is recorded.
func (c *Client) GetSlot(ctx context.Context) (uint64, error) {
	stats := rpcStatsFrom(ctx)
	netStart := time.Now()
	slot, err := c.client.GetSlot(ctx, c.commitment)
	if err != nil {
		stats.record("GetSlot", time.Since(netStart), 0, true)
		return 0, fmt.Errorf("getSlot failed: %w", err)
	}
	stats.record("GetSlot", time.Since(netStart), 0, false)
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
// Null-safe: blockTime/blockHeight/commission/computeUnits may be null, and
// empty collections stay nil rather than being invented.
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
		ParentSlot:        result.ParentSlot,
		Transactions:      transactions,
		Rewards:           convertRewards(result.Rewards),
	}
	if result.BlockHeight != nil {
		height := *result.BlockHeight
		block.BlockHeight = &height
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
		Signature:               firstSignature(tx),
		Slot:                    slot,
		TransactionIndex:        txIndex,
		RecentBlockhash:         tx.Message.RecentBlockhash.String(),
		AccountKeys:             staticKeys,
		LoadedAddressesWritable: loadedWritable,
		LoadedAddressesReadonly: loadedReadonly,
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
		out.Failed = true
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
	out.Instructions = convertOuterInstructions(tx.Message.Instructions, committed)
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
// only reports stack depth for CPI instructions) and no inner index.
func convertOuterInstructions(instrs []solana.CompiledInstruction, keys []string) []Instruction {
	if len(instrs) == 0 {
		return nil
	}
	out := make([]Instruction, 0, len(instrs))
	for i, ci := range instrs {
		out = append(out, newInstruction(
			uint64(ci.ProgramIDIndex), ci.Accounts, ci.Data.String(),
			nil, i, 0, keys,
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
		for j, ci := range group.Instructions {
			stackHeight := ci.StackHeight
			inner = append(inner, newInstruction(
				uint64(ci.ProgramIDIndex), ci.Accounts, ci.Data.String(),
				&stackHeight, int(group.Index), j, keys,
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
func newInstruction(programIDIndex uint64, accountIdx []uint16, data string, stackHeight *uint16, outerIdx, innerIdx int, keys []string) Instruction {
	return Instruction{
		ProgramID:        resolveKey(keys, programIDIndex),
		Accounts:         resolveAccounts(keys, accountIdx),
		Data:             data,
		InstructionIndex: outerIdx,
		InnerIndex:       innerIdx,
		StackHeight:      stackHeight,
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

// retryTransport wraps the base round tripper with read-safe rate-limit
// retries. Solana providers throttle with HTTP 429 (CU limits) or 503 and
// supply a Retry-After header; only this layer can see it, so honoring the
// header has to happen here rather than in the fetcher or processor.
type retryTransport struct {
	base http.RoundTripper
}

// newRetryTransport wraps base (or the default transport) with
// rate-limit-aware retry behaviour.
func newRetryTransport(base http.RoundTripper) http.RoundTripper {
	return &retryTransport{base: base}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(req)
	for attempt := 1; attempt < retryTransportMaxAttempts; attempt++ {
		if err != nil || !isThrottledStatus(resp.StatusCode) {
			return resp, err
		}
		if err := req.Context().Err(); err != nil {
			return nil, err
		}

		wait := retryAfter(resp.Header.Get("Retry-After"), attempt)
		drainAndClose(resp)

		select {
		case <-req.Context().Done():
			// Surface the cancellation rather than issuing another request
			// the caller no longer wants.
			return nil, req.Context().Err()
		case <-time.After(wait):
		}

		resp, err = base.RoundTrip(withFreshBody(req))
	}

	return resp, err
}

// isThrottledStatus reports HTTP statuses the transport should retry.
func isThrottledStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable
}

// retryAfter parses a Retry-After value (seconds or HTTP-date), clamped to
// the transport's wait bounds: at least the base backoff (a zero value means
// "retry soon", not "give up"), at most the max wait. A missing header falls
// back to exponential backoff on the attempt number.
func retryAfter(value string, attempt int) time.Duration {
	wait := retryTransportBackoff << (attempt - 1)
	if secs, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && secs >= 0 {
		wait = time.Duration(secs) * time.Second
	} else if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			wait = delay
		}
	}
	if wait < retryTransportBackoff {
		wait = retryTransportBackoff
	}
	if wait > retryTransportMaxWait {
		wait = retryTransportMaxWait
	}
	return wait
}

// drainAndClose reads out the response body so the pooled connection can be
// reused by the next attempt.
func drainAndClose(resp *http.Response) {
	if resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// withFreshBody re-buffers a request body for a retry attempt: the previous
// attempt consumed the stream, and JSON-RPC POSTs carry a GetBody-able
// payload (bytes reader), which makes a re-issue possible at all.
func withFreshBody(req *http.Request) *http.Request {
	clone := req.Clone(req.Context())
	if req.GetBody != nil {
		if body, err := req.GetBody(); err == nil {
			clone.Body = body
		}
	}
	return clone
}
