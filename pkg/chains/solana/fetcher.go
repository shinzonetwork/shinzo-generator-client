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

// rpcClient abstracts the subset of *SolanaClient methods used by the
// fetcher. It exists so tests can inject a lightweight fake without dialing
// a real RPC endpoint. *SolanaClient satisfies this interface.
type rpcClient interface {
	GetBlock(ctx context.Context, slot uint64) (*Block, error)
	GetBlockFromArchive(ctx context.Context, slot uint64) (*Block, error)
	GetSlot(ctx context.Context) (uint64, error)
	Close() error
}

// Compile-time guarantees: the concrete client satisfies the test seam, and
// Fetcher satisfies the chain-agnostic fetcher contract.
var (
	_ rpcClient      = (*SolanaClient)(nil)
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
	nodeURL     string
	archiveURL  string
	apiKey      string
	apiKeyType  string
	commitment  string
	maxTxVersion uint64
	rewards     bool
	dialTimeout time.Duration
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
	return &Fetcher{
		nodeURL:       cfg.Solana.RPCURL,
		archiveURL:    cfg.Solana.ArchiveRPCURL,
		apiKey:        cfg.Solana.APIKey,
		apiKeyType:    cfg.Solana.APIKeyType,
		commitment:    cfg.Solana.Commitment,
		maxTxVersion:  uint64(cfg.Solana.MaxSupportedTransactionVersion),
		rewards:       cfg.Solana.RewardsEnabled(),
		dialTimeout:   time.Duration(cfg.Solana.DialTimeoutSeconds) * time.Second,
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
	client, err := NewSolanaClient(ctx, SolanaClientOptions{
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
func (f *Fetcher) FetchBlock(ctx context.Context, height int64) (any, error) {
	if f.client == nil {
		return nil, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchBlock")
	}
	slot := uint64(height)

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
	return int64(slot), nil
}

// Close implements chains.Fetcher. It closes the underlying RPC client.
func (f *Fetcher) Close() error {
	if f.client != nil {
		return f.client.Close()
	}
	return nil
}
