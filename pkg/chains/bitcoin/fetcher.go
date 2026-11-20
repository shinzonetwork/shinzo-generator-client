package bitcoin

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcjson"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// rawClient is the package-private seam between the fetcher and the RPC
// client. *Client satisfies it directly; tests inject fakes instead of wiring
// the HTTP server the way the client-level tests do, which keeps fetcher
// semantics (routing, wrapping, retry absence) testable in isolation.
type rawClient interface {
	GetBlockCount(ctx context.Context) (int64, error)
	GetBlockHash(ctx context.Context, height int64) (string, error)
	GetBlockVerbosity3(ctx context.Context, hash string) (*Block, error)
	GetBlockVerbosity3FromArchive(ctx context.Context, hash string) (*Block, error)
	GetBlockHeaderByHash(ctx context.Context, hash string) (*VerboseBlockHeader, error)
	GetChainInfo(ctx context.Context) (*ChainInfo, error)
	Close() error
}

var _ rawClient = (*Client)(nil)

// Fetcher implements chains.Fetcher for Bitcoin Core over its JSON-RPC
// surface. FetchBlock always returns the main-chain block at the given
// height (hash lookup first, then the verbosity-3 body), FetchHighestBlockNumber
// rides getblockcount, and FetchFinalizedBlockNumber applies the configured
// confirmation depth because Bitcoin Core publishes no finalized or safe tag.
type Fetcher struct {
	client            rawClient
	confirmationDepth int64
	cfgBitcoin        config.BitcoinConfig
	dialTimeout       time.Duration
}

// Compile-time guarantee that Fetcher implements chains.Fetcher.
var _ chains.Fetcher = (*Fetcher)(nil)

// NewFetcher wraps an already-built client. It exists for tests and
// higher-layer harnesses that construct the seam themselves; Connect is then
// a no-op regardless of the config fields left zero.
func NewFetcher(client rawClient, confirmationDepth int64) *Fetcher {
	return &Fetcher{
		client:            client,
		confirmationDepth: confirmationDepth,
	}
}

// NewFetcherFromConfig builds the fetcher the factory hands to the indexer.
// Connection stays deferred: construction is network-free, so failure to
// reach the node surfaces from Connect, which the indexer calls once, under
// its own context.
func NewFetcherFromConfig(cfg *config.Config) (*Fetcher, error) {
	if cfg == nil {
		return nil, errors.NewConfigurationError("chain", "NewFetcherFromConfig", "config is nil", "", nil)
	}
	depth := int64(cfg.Bitcoin.ConfirmationDepth)
	if depth <= 0 {
		depth = config.DefaultBitcoinConfirmationDepth
	}
	return &Fetcher{
		confirmationDepth: depth,
		cfgBitcoin:        cfg.Bitcoin,
		dialTimeout:       resolveDialTimeout(cfg.Bitcoin.DialTimeoutSeconds),
	}, nil
}

// Connect implements chains.Fetcher. A config-built fetcher constructs its
// client here (network-free) and runs one getblockcount health check, so the
// indexer learns about an unreachable node at startup and not on the first
// fetch. The probe is bounded by the resolved dial timeout; btcd has no
// per-call context yet, so the bound is enforced by selecting on the probe's
// completion against the deadline, and the under-probe call may linger until
// the library's own HTTP timeout releases it.
func (f *Fetcher) Connect(ctx context.Context) error {
	if f.client != nil {
		return nil
	}
	client, err := NewClient(f.cfgBitcoin)
	if err != nil {
		return err
	}
	if err := f.probeHealth(ctx, client); err != nil {
		// Close failure cannot make the already-reported probe failure any
		// worse, and the lazy HTTP client holds nothing worth reporting.
		_ = client.Close()
		return err
	}
	f.client = client
	return nil
}

// probeHealth runs the getblockcount reachability check under the dial
// timeout and records the node's archive floor as a one-time startup log. A
// missing floor log never fails Connect: it is documentation for the
// operator, not a health signal.
func (f *Fetcher) probeHealth(ctx context.Context, client *Client) error {
	probeCtx, cancel := context.WithTimeout(ctx, f.dialTimeout)
	defer cancel()
	// Buffered so the goroutine cannot leak: the fetcher may select
	// timeout before the probe finishes, after which the probe's send still
	// succeeds and the goroutine exits.
	probeResult := make(chan error, 1)
	go func() {
		_, err := client.GetBlockCount(probeCtx)
		probeResult <- err
	}()

	select {
	case err := <-probeResult:
		if err != nil {
			return fmt.Errorf("bitcoin connect probe failed: %w", err)
		}
		f.logArchiveFloor(ctx, client)
		return nil
	case <-probeCtx.Done():
		return fmt.Errorf("bitcoin connect probe timed out after %s: %w", f.dialTimeout, probeCtx.Err())
	}
}

// logArchiveFloor documents the node's prune state in one startup line. The
// getblockchaininfo call makes up to three round trips when the library's
// backend-version cache is cold, which is acceptable at startup and never
// repeated.
func (f *Fetcher) logArchiveFloor(ctx context.Context, client *Client) {
	info, err := client.GetChainInfo(ctx)
	if err != nil {
		logger.Sugar.Warnf("bitcoin node archive-floor info unavailable: %v", err)
		return
	}
	if info.Pruned {
		logger.Sugar.Infof("bitcoin node %s: pruned, undo data starts at height %d", info.Chain, info.PruneHeight)
	} else {
		logger.Sugar.Infof("bitcoin node %s: not pruned, all heights carry undo data", info.Chain)
	}
}

// FetchBlock implements chains.Fetcher. Two round trips produce the bundle:
// getblockhash pins the height to the current main-chain hash, then the
// verbosity-3 body is fetched by that hash, so a concurrent reorg between the
// two calls can only surface as a not-found, never as a half-switched payload.
//
// Error classification is deliberately narrow: heights past the tip (Core's
// -8) are wrapped into not-found so the processor's tip-wait retry engages,
// Core's unknown-hash -5 keeps its own not-found text for the walk-back, and
// the pruned-node undo-data outcome routes to the archive endpoint, with its
// hard operator error when none is configured. Nothing here retries: the
// processor owns retries, and the client never retries RPC errors.
func (f *Fetcher) FetchBlock(ctx context.Context, height int64) (any, error) {
	if f.client == nil {
		return nil, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchBlock")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	blockHash, err := f.client.GetBlockHash(ctx, height)
	if err != nil {
		if isHeightOutOfRange(err) {
			return nil, fmt.Errorf("block at height %d not found: %w", height, err)
		}
		return nil, err
	}
	block, err := f.client.GetBlockVerbosity3(ctx, blockHash)
	if err != nil {
		if stderrors.Is(err, errUndoDataUnavailable) {
			return f.fetchFromArchive(ctx, height, blockHash)
		}
		return nil, err
	}
	return block, nil
}

// fetchFromArchive completes the pruned-height fetch against the archive
// endpoint. Each failure shape gets its own message so the operator can
// distinguish a missing configuration from an archive that is itself pruned;
// both stay free of the not-found substrings, keeping them out of the retry
// loops that not-found classification drives.
func (f *Fetcher) fetchFromArchive(ctx context.Context, height int64, hash string) (any, error) {
	block, err := f.client.GetBlockVerbosity3FromArchive(ctx, hash)
	if err == nil {
		return block, nil
	}
	if stderrors.Is(err, errArchiveNotConfigured) {
		return nil, fmt.Errorf("height %d needs undo data this node has pruned and no archive endpoint is configured: %w", height, err)
	}
	if stderrors.Is(err, errUndoDataUnavailable) {
		return nil, fmt.Errorf("archive fallback also lacks undo data for block %s: %w", hash, err)
	}
	return nil, err
}

// isHeightOutOfRange reports whether the error is Core's -8, the distance the
// fetcher turns into not-found for the processor's wait-for-tip loop. Only
// the exact code counts: other -8 users elsewhere in Core are never reachable
// from this seam, because the height lookup is the only -8 source.
func isHeightOutOfRange(err error) bool {
	rpcErr, ok := stderrors.AsType[*btcjson.RPCError](err)
	return ok && rpcErr.Code == btcjson.ErrRPCInvalidParameter
}

// FetchBlockHeaderByHash implements chains.Fetcher. The walk-back follows
// parent hashes header-only, so the returned chain hash is the one the caller
// asked for, echoed unchanged: a hash recomputed from payload fields could
// never be trusted the same way. Client errors pass through untouched so
// Core's not-found wording keeps its classification for the walk-back.
func (f *Fetcher) FetchBlockHeaderByHash(ctx context.Context, hash string) (chains.BlockHeader, error) {
	if f.client == nil {
		return chains.BlockHeader{}, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchBlockHeaderByHash")
	}
	header, err := f.client.GetBlockHeaderByHash(ctx, hash)
	if err != nil {
		return chains.BlockHeader{}, err
	}
	if header == nil {
		// Unreachable against the real client, which errors on an absent
		// header; kept defensive so a nil deref can never become the
		// walk-back's next parentHash.
		return chains.BlockHeader{}, fmt.Errorf("header for hash %s is unavailable", hash)
	}
	return chains.BlockHeader{
		Number:     header.Height,
		Hash:       hash,
		ParentHash: header.PreviousBlockHash,
	}, nil
}

// FetchHighestBlockNumber implements chains.Fetcher.
func (f *Fetcher) FetchHighestBlockNumber(ctx context.Context) (int64, error) {
	if f.client == nil {
		return 0, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchHighestBlockNumber")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	tip, err := f.client.GetBlockCount(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get block count: %w", err)
	}
	return tip, nil
}

// FetchFinalizedBlockNumber implements chains.Fetcher. Bitcoin Core exposes
// no finalized or safe tag, so the walk-back floor is the config-approved
// policy instead: the tip minus the confirmation depth. A tip still inside
// the first confirmation window clamps at genesis rather than reporting a
// negative height, because no walk-back descends below zero anyway.
func (f *Fetcher) FetchFinalizedBlockNumber(ctx context.Context) (int64, error) {
	if f.client == nil {
		return 0, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchFinalizedBlockNumber")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	tip, err := f.client.GetBlockCount(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get block count: %w", err)
	}
	floor := tip - f.confirmationDepth
	if floor < 0 {
		return 0, nil
	}
	return floor, nil
}

// Close implements chains.Fetcher. It is nil-safe: a fetcher that never
// connected has nothing to shut down.
func (f *Fetcher) Close() error {
	if f.client == nil {
		return nil
	}
	return f.client.Close()
}
