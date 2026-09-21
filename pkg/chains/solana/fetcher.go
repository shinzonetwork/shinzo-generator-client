package solana

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
)

// rpcClient abstracts the subset of *Client methods used by the
// fetcher. It exists so tests can inject a lightweight fake without dialing
// a real RPC endpoint. *Client satisfies this interface.
type rpcClient interface {
	GetBlock(ctx context.Context, slot uint64) (*Block, error)
	GetBlockFromArchive(ctx context.Context, slot uint64) (*Block, error)
	GetSlot(ctx context.Context) (uint64, error)
	Close() error
}

// Compile-time guarantees: the concrete client satisfies the test seam, and
// Fetcher satisfies the chain-agnostic fetcher contract.
var (
	_ rpcClient      = (*Client)(nil)
	_ chains.Fetcher = (*Fetcher)(nil)
)

// Fetcher is the RPC I/O layer for Solana. It fetches full-fidelity block
// data (block + transactions + metadata + rewards) from the chain node and
// queries the on-chain tip, classifying permanently-skipped slots into
// chains.ErrHeightSkipped so the indexer can advance past them.
//
// Fetcher is safe for concurrent use across different slots; the
// orchestration layer is responsible for parallel fan-out.
type Fetcher struct {
	client rpcClient

	// Connection-config fields populated by NewFetcherFromConfig. When
	// non-empty, Connect(ctx) builds the RPC client using these values.
	// dialTimeout bounds the Connect health check when positive; non-positive
	// values leave connectivity unbounded (the caller's context governs).
	// The low-level NewFetcher constructor sets client directly and leaves
	// these blank, making Connect a no-op.
	nodeURL      string
	archiveURL   string
	apiKey       string
	apiKeyType   string
	wsURL        string
	commitment   string
	maxTxVersion uint64
	rewards      bool
	dialTimeout  time.Duration

	// notifier drives the WS slotSubscribe hint gate. nil unless Connect
	// launched one (wsURL configured); all methods are nil-safe.
	notifier *slotNotifier
}

// NewFetcher creates a Fetcher wrapping the given RPC client. Intended for
// tests; production wiring uses NewFetcherFromConfig + Connect.
func NewFetcher(client rpcClient) *Fetcher {
	return &Fetcher{client: client}
}

// NewFetcherFromConfig creates a Fetcher from the given config without
// dialing the RPC endpoint. Call Connect(ctx) to establish the connection
// before using FetchBlock/FetchHighestBlockNumber.
func NewFetcherFromConfig(cfg *config.Config) (*Fetcher, error) {
	if cfg == nil {
		return nil, errors.NewConfigurationError("solana", "NewFetcherFromConfig", "config is nil", "", nil)
	}
	maxTxVersion := uint64(cfg.Solana.MaxSupportedTransactionVersion) //nolint:gosec // config validation normalizes this to a positive default
	return &Fetcher{
		nodeURL:      cfg.Solana.RPCURL,
		archiveURL:   cfg.Solana.ArchiveRPCURL,
		apiKey:       cfg.Solana.APIKey,
		apiKeyType:   cfg.Solana.APIKeyType,
		wsURL:        cfg.Solana.WsURL,
		commitment:   cfg.Solana.Commitment,
		maxTxVersion: maxTxVersion,
		rewards:      cfg.Solana.RewardsEnabled(),
		dialTimeout:  time.Duration(cfg.Solana.DialTimeoutSeconds) * time.Second,
	}, nil
}

// Connect builds the RPC client and issues one getSlot health check so a bad
// endpoint or API key fails at startup rather than at the first fetch. HTTP
// dialing is lazy, so without the check a misconfigured client could sit
// idle until index time. No-op when the fetcher was built via NewFetcher.
func (f *Fetcher) Connect(ctx context.Context) error {
	if f.client != nil {
		return nil
	}
	if f.dialTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.dialTimeout)
		defer cancel()
	}
	client, err := NewClient(ctx, ClientOptions{
		RPCURL:                         f.nodeURL,
		ArchiveRPCURL:                  f.archiveURL,
		Commitment:                     f.commitment,
		MaxSupportedTransactionVersion: f.maxTxVersion,
		Rewards:                        f.rewards,
		APIKey:                         f.apiKey,
		APIKeyType:                     f.apiKeyType,
	})
	if err != nil {
		return fmt.Errorf("create solana client: %w", err)
	}
	// Cheap round-trip through the real transport: validates URL, auth, and
	// TLS reachability in one proactive call.
	if _, err := client.GetSlot(ctx); err != nil {
		_ = client.Close()
		return errors.NewRPCConnectionFailed("solana", "Fetcher.Connect", f.nodeURL, err)
	}
	f.client = client

	// Soft-fail WS hint gate: the connection loop owns dialing and retries
	// forever with capped backoff, so a websocket failure here never blocks
	// startup — FetchBlock simply fetches blind until hints flow.
	if f.wsURL != "" {
		f.notifier = newSlotNotifier(f.wsURL, f.apiKey, f.apiKeyType)
		// WithoutCancel detaches the notifier from this ctx, which may be a
		// dial-timeout ctx that is cancelled as soon as Connect returns; the
		// notifier must outlive it and shut down only via Fetcher.Close.
		f.notifier.start(context.WithoutCancel(ctx))
	}
	return nil
}

// FetchBlock implements chains.Fetcher. It fetches the block at the given
// height (the slot) and classifies missing-block outcomes:
//
//   - null / skipped / missing while strictly below the confirmed tip →
//     chains.ErrHeightSkipped (permanent: no block will ever exist there)
//   - same outcomes at or above the tip → transient not-found error (the
//     slot may simply not be visible yet; the processor's not-found loop
//     waits for it)
//   - pruned below the node's ledger floor → archive endpoint when
//     configured, otherwise a hard error so operators learn that deep
//     backfill needs SOLANA_ARCHIVE_RPC_URL
//
// When Connect launched the WS hint gate (ws_url configured), FetchBlock
// first waits for a slotSubscribe notification at or above the slot —
// advisory readiness at processed commitment, bounded by the stall timeout
// (silence flips to immediate blind fetches) — replacing the previous
// getBlock-poll-every-3s alignment with one fetch attempt right when the
// slot is likely fetchable. Without the gate, or once the gate is open, the
// behavior is byte-identical to the classification rules above. The tip
// query in classifyMissingSlot and the archive route are never gated.
func (f *Fetcher) FetchBlock(ctx context.Context, height int64) (any, error) {
	if f.client == nil {
		return nil, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchBlock")
	}
	if height < 0 {
		return nil, fmt.Errorf("invalid height %d: slot numbers are non-negative", height)
	}
	slot := uint64(height) //nolint:gosec // guarded non-negative above

	if err := f.waitSlotHinted(ctx, slot); err != nil {
		return nil, err
	}

	block, err := f.client.GetBlock(ctx, slot)
	if err == nil {
		return block, nil
	}

	if stderrors.Is(err, errBlockCleanedUp) {
		return f.fetchPrunedBlock(ctx, slot)
	}

	if isMissingSlotError(err) {
		return nil, f.classifyMissingSlot(ctx, slot, err)
	}

	return nil, err
}

// waitSlotHinted blocks until the slot was observed as processed by the WS
// hint gate, or fetching is otherwise safe to attempt: gate disabled, stall
// timeout expired (blind mode), or notifier stopped — each returns
// immediately. Only ctx cancellation produces an error.
func (f *Fetcher) waitSlotHinted(ctx context.Context, slot uint64) error {
	return f.notifier.AwaitHint(ctx, slot)
}

// fetchPrunedBlock routes a block below the main node's ledger floor to the
// archive endpoint. Without an archive endpoint the block cannot be
// recovered by waiting, so the failure propagates — the operator must
// configure SOLANA_ARCHIVE_RPC_URL for backfill; treating it as skipped
// would silently drop live data.
func (f *Fetcher) fetchPrunedBlock(ctx context.Context, slot uint64) (any, error) {
	block, err := f.client.GetBlockFromArchive(ctx, slot)
	if err == nil {
		return block, nil
	}

	if stderrors.Is(err, errArchiveNotConfigured) {
		return nil, fmt.Errorf(
			"slot %d pruned below the node's ledger floor and no archive endpoint configured: set SOLANA_ARCHIVE_RPC_URL for backfill: %w",
			slot, err)
	}

	if isMissingSlotError(err) {
		return nil, f.classifyMissingSlot(ctx, slot, err)
	}

	return nil, err
}

// classifyMissingSlot decides between permanent skip and transient
// not-found by comparing the requested slot with the confirmed tip. The
// boundary is strict: a slot at the tip may still be settling, so it stays
// in the transient retry path and only slots the tip has moved beyond can
// be declared permanent skips.
func (f *Fetcher) classifyMissingSlot(ctx context.Context, slot uint64, cause error) error {
	tip, tipErr := f.client.GetSlot(ctx)
	if tipErr != nil {
		// Without a usable tip the slot cannot be classified as a permanent
		// skip; a conservative transient error keeps the slot in the
		// processor's not-found waiting loop until the next attempt.
		return transientNotFound(slot, fmt.Errorf("tip query failed: %w", tipErr))
	}
	if slot < tip {
		return fmt.Errorf("slot %d below confirmed tip %d: %w", slot, tip, chains.ErrHeightSkipped)
	}
	return transientNotFound(slot, cause)
}

// isMissingSlotError reports which transport outcomes need tip-based
// classification: skip codes from the node, plus the null-result sentinel
// for blocks not yet visible at the configured commitment.
func isMissingSlotError(err error) bool {
	return stderrors.Is(err, errSlotSkipped) ||
		stderrors.Is(err, errBlockNotAvailable) ||
		stderrors.Is(err, errNotConfirmed)
}

// transientNotFound produces the "waiting for the chain" error: its message
// carries "not found" so the processor's existing not-found retry policy
// (infinite, throttled) handles it with zero processor special-casing.
func transientNotFound(slot uint64, cause error) error {
	return fmt.Errorf("slot %d not found: %w", slot, cause)
}

// FetchHighestBlockNumber implements chains.Fetcher. The current tip at the
// configured commitment — for Solana the slot is the height.
func (f *Fetcher) FetchHighestBlockNumber(ctx context.Context) (int64, error) {
	if f.client == nil {
		return 0, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchHighestBlockNumber")
	}
	slot, err := f.client.GetSlot(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get highest slot: %w", err)
	}
	return int64(slot), nil //nolint:gosec // confirmed slots stay far below int64 max
}

// Close implements chains.Fetcher. It stops the WS hint gate (releasing any
// FetchBlock waiters) and closes the underlying RPC client.
func (f *Fetcher) Close() error {
	f.notifier.stop()
	if f.client != nil {
		return f.client.Close()
	}
	return nil
}
