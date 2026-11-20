package solana

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
)

// ---------------------------------------------------------------------------
// NewFetcherFromConfig.
// ---------------------------------------------------------------------------

func TestNewFetcherFromConfig(t *testing.T) {
	t.Parallel()

	rewardsOff := false
	cfg := &config.Config{
		Solana: config.SolanaConfig{
			NodeURL:                        "https://api.mainnet-beta.solana.com",
			ArchiveRPCURL:                  "https://archive.example.com",
			APIKey:                         "secret",
			APIKeyType:                     "X-Api-Key",
			Commitment:                     "finalized",
			MaxSupportedTransactionVersion: 1,
			Rewards:                        &rewardsOff,
			DialTimeoutSeconds:             7,
		},
	}
	f, err := NewFetcherFromConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, "https://api.mainnet-beta.solana.com", f.nodeURL)
	assert.Equal(t, "https://archive.example.com", f.archiveURL)
	assert.Equal(t, "secret", f.apiKey)
	assert.Equal(t, "X-Api-Key", f.apiKeyType)
	assert.Equal(t, "finalized", f.commitment)
	assert.Equal(t, uint64(1), f.maxTxVersion)
	assert.False(t, f.rewards, "explicitly disabled rewards must stick")
	assert.Equal(t, 7*time.Second, f.dialTimeout)
	assert.Nil(t, f.client, "must not be connected before Connect")

	// Nil Rewards pointer means enabled.
	f, err = NewFetcherFromConfig(&config.Config{
		Solana: config.SolanaConfig{NodeURL: "https://r", Commitment: "confirmed"},
	})
	require.NoError(t, err)
	assert.True(t, f.rewards)
	assert.Equal(t, "confirmed", f.commitment)

	cfgNil := (*config.Config)(nil)
	_, err = NewFetcherFromConfig(cfgNil)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Not-connected guards.
// ---------------------------------------------------------------------------

func TestFetcher_NotConnected(t *testing.T) {
	t.Parallel()

	f := NewFetcherFromConfigForTest()

	_, err := f.FetchBlock(context.Background(), 1)
	require.Error(t, err)
	assert.ErrorContains(t, err, "fetcher not connected")

	_, err = f.FetchHighestBlockNumber(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "fetcher not connected")

	_, err = f.FetchFinalizedBlockNumber(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "fetcher not connected")

	_, err = f.FetchBlockHeaderByHash(context.Background(), "abc")
	require.Error(t, err)
	assert.ErrorContains(t, err, "fetcher not connected")

	assert.NoError(t, f.Close())
}

// ---------------------------------------------------------------------------
// FetchBlock classification.
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlock_Success(t *testing.T) {
	t.Parallel()

	want := fakeBlock(42)
	client := &fakeSlotClient{
		tip:     100,
		blockFn: func(_ context.Context, _ uint64) (*Block, error) { return want, nil },
	}
	f := testFetcher(client)

	raw, err := f.FetchBlock(context.Background(), 42)
	require.NoError(t, err)
	assert.Same(t, want, raw.(*Block))

	assert.Equal(t, 1, client.blockCallCount())
	assert.Equal(t, 0, client.archiveCallCount())
	assert.Equal(t, 0, client.tipCallCount(), "successful fetches never query the tip")

	// Every success remembers its header for the walk-back.
	got, err := f.FetchBlockHeaderByHash(context.Background(), want.Blockhash)
	require.NoError(t, err)
	assert.Equal(t, int64(42), got.Number)
	assert.Equal(t, want.Blockhash, got.Hash)
	assert.Equal(t, want.PreviousBlockhash, got.ParentHash)
}

func TestFetcher_FetchBlock_SkippedBelowTip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
	}{
		{name: "SlotSkippedCode", err: fmt.Errorf("slot skipped: %w", errSlotSkipped)},
		{name: "BlockNotAvailableCode", err: fmt.Errorf("not available: %w", errBlockNotAvailable)},
		{name: "NullResult", err: errNotConfirmed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeSlotClient{
				tip: 1000,
				blockFn: func(_ context.Context, _ uint64) (*Block, error) {
					return nil, tc.err
				},
			}
			f := testFetcher(client)

			_, err := f.FetchBlock(context.Background(), 999)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrHeightSkipped, "strictly below tip means permanently skipped: %v", err)
			assert.False(t, errors.IsErrNotFound(err),
				"skipped heights must not enter the not-found retry loop")

			assert.Equal(t, 1, client.blockCallCount())
			assert.Equal(t, 1, client.tipCallCount(), "classification needs one tip query")
		})
	}
}

func TestFetcher_FetchBlock_MissingAtOrAboveTip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		slot uint64
	}{
		{name: "AtTip", err: errNotConfirmed, slot: 1000},
		{name: "AboveTip", err: errNotConfirmed, slot: 1001},
		{name: "NotAvailableAboveTip", err: errBlockNotAvailable, slot: 1001},
		{name: "SkippedCodeAboveTip", err: errSlotSkipped, slot: 1002},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeSlotClient{
				tip: 1000,
				blockFn: func(_ context.Context, _ uint64) (*Block, error) {
					return nil, tc.err
				},
			}
			f := testFetcher(client)

			_, err := f.FetchBlock(context.Background(), int64(tc.slot))
			require.Error(t, err)
			assert.True(t, errors.IsErrNotFound(err),
				"at/above-tip misses are transient and must drive the processor's not-found wait: %v", err)
			assert.NotErrorIs(t, err, ErrHeightSkipped)
		})
	}
}

func TestFetcher_FetchBlock_TipQueryFails(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{
		tipErr:  fmt.Errorf("tip connection refused"),
		blockFn: func(_ context.Context, _ uint64) (*Block, error) { return nil, errSlotSkipped },
	}
	f := testFetcher(client)

	_, err := f.FetchBlock(context.Background(), 999)
	require.Error(t, err)
	assert.True(t, errors.IsErrNotFound(err),
		"unclassifiable misses stay transient and get retried, never declared skipped: %v", err)
	assert.NotErrorIs(t, err, ErrHeightSkipped)
}

func TestFetcher_FetchBlock_NegativeHeight(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{}
	f := testFetcher(client)

	_, err := f.FetchBlock(context.Background(), -1)
	require.Error(t, err)
	assert.ErrorContains(t, err, "non-negative")

	assert.Zero(t, client.blockCallCount(), "no RPC may be issued for an invalid height")
}

// ---------------------------------------------------------------------------
// FetchBlock: pruned below the node's ledger floor.
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlock_CleanedUp_WithArchive(t *testing.T) {
	t.Parallel()

	want := &Block{Slot: 42}
	client := &fakeSlotClient{
		tip: 1000,
		blockFn: func(_ context.Context, _ uint64) (*Block, error) {
			return nil, fmt.Errorf("cleaned: %w", errBlockCleanedUp)
		},
		archiveFn: func(_ context.Context, _ uint64) (*Block, error) { return want, nil },
	}
	f := testFetcher(client)

	raw, err := f.FetchBlock(context.Background(), 42)
	require.NoError(t, err)
	assert.Same(t, want, raw.(*Block))

	assert.Equal(t, 1, client.blockCallCount())
	assert.Equal(t, 1, client.archiveCallCount())
}

func TestFetcher_FetchBlock_CleanedUp_ArchiveSkipClassified(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{
		tip: 1000,
		blockFn: func(_ context.Context, _ uint64) (*Block, error) {
			return nil, fmt.Errorf("cleaned: %w", errBlockCleanedUp)
		},
		archiveFn: func(_ context.Context, _ uint64) (*Block, error) {
			return nil, fmt.Errorf("archive: %w", errSlotSkipped)
		},
	}
	f := testFetcher(client)

	// Below tip on the archive path too: permanently skipped.
	_, err := f.FetchBlock(context.Background(), 999)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrHeightSkipped)
}

func TestFetcher_FetchBlock_CleanedUp_NoArchive(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{
		tip: 1000,
		blockFn: func(_ context.Context, _ uint64) (*Block, error) {
			return nil, fmt.Errorf("cleaned: %w", errBlockCleanedUp)
		},
	}
	f := testFetcher(client)

	_, err := f.FetchBlock(context.Background(), 42)
	require.Error(t, err)
	assert.ErrorContains(t, err, "SOLANA_ARCHIVE_RPC_URL",
		"pruned blocks are live data: the hard error directs the operator to configure the archive endpoint")
	assert.False(t, errors.IsErrNotFound(err),
		"must not retry forever on a pruned slot")
	assert.NotErrorIs(t, err, ErrHeightSkipped,
		"pruned-but-existing blocks must not be declared skipped: that would silently drop them")
}

func TestFetcher_FetchBlock_CleanedUp_ArchiveOtherError(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{
		tip: 1000,
		blockFn: func(_ context.Context, _ uint64) (*Block, error) {
			return nil, fmt.Errorf("cleaned: %w", errBlockCleanedUp)
		},
		archiveFn: func(_ context.Context, _ uint64) (*Block, error) {
			return nil, fmt.Errorf("archive connection refused")
		},
	}
	f := testFetcher(client)

	_, err := f.FetchBlock(context.Background(), 42)
	require.Error(t, err)
	assert.ErrorContains(t, err, "connection refused")
}

// ---------------------------------------------------------------------------
// FetchHighestBlockNumber / FetchFinalizedBlockNumber.
// ---------------------------------------------------------------------------

func TestFetcher_FetchHighestBlockNumber(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{tip: 424242}
	f := testFetcher(client)

	height, err := f.FetchHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(424242), height)
}

func TestFetcher_FetchHighestBlockNumber_Error(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{tipErr: fmt.Errorf("getSlot failed: connection refused")}
	f := testFetcher(client)

	_, err := f.FetchHighestBlockNumber(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "highest slot")
}

func TestFetcher_FetchFinalizedBlockNumber(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{finalized: 424000}
	f := testFetcher(client)

	height, err := f.FetchFinalizedBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(424000), height)
	assert.Equal(t, 1, client.finalizedCallCount(), "exactly one finalized getSlot per tip query")
}

func TestFetcher_FetchFinalizedBlockNumber_Error(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{finalErr: fmt.Errorf("finalized getSlot failed")}
	f := testFetcher(client)

	_, err := f.FetchFinalizedBlockNumber(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "finalized slot")
}

// ---------------------------------------------------------------------------
// FetchBlockHeaderByHash: the walk-back cache.
// ---------------------------------------------------------------------------

func TestFetcher_HeaderByHash_FromCache(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{tip: 1000}
	client.blockFn = func(_ context.Context, slot uint64) (*Block, error) {
		return fakeBlockWithTxs(slot, fakeTransaction("hdr")), nil
	}
	f := testFetcher(client)

	_, err := f.FetchBlock(context.Background(), 500)
	require.NoError(t, err)

	header, err := f.FetchBlockHeaderByHash(context.Background(), testHeader(500).Hash)
	require.NoError(t, err)
	requireSlotHeader(t, header, testHeader(500))
}

func TestFetcher_HeaderByHash_UnknownHashNotFound(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{}
	f := testFetcher(client)

	_, err := f.FetchBlockHeaderByHash(context.Background(), testHeader(1).Hash)
	require.Error(t, err)
	assert.True(t, errors.IsErrNotFound(err),
		"an unknown blockhash must classify like other not-found fetch outcomes: %v", err)
}

func TestFetcher_HeaderByHash_CacheBound(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{}
	client.blockFn = func(_ context.Context, slot uint64) (*Block, error) {
		return fakeBlock(slot), nil
	}
	f := testFetcher(client)

	// Fetch 2 more slots than the bound: the cache is dropped wholesale at
	// capacity, so only the last two slots (fetched after the reset) remain.
	first := uint64(2)
	last := uint64(maxHeaderCacheEntries + 3)
	for slot := first; slot <= last; slot++ {
		_, err := f.FetchBlock(context.Background(), int64(slot))
		require.NoError(t, err)
	}

	_, err := f.FetchBlockHeaderByHash(context.Background(), testHeader(first).Hash)
	require.True(t, errors.IsErrNotFound(err), "the pre-reset entries must be gone")
	_, err = f.FetchBlockHeaderByHash(context.Background(), testHeader(last-5).Hash)
	require.True(t, errors.IsErrNotFound(err))

	for slot := last - 1; slot <= last; slot++ {
		header, err := f.FetchBlockHeaderByHash(context.Background(), testHeader(slot).Hash)
		require.NoError(t, err)
		requireSlotHeader(t, header, testHeader(slot))
	}
}

func TestFetcher_HeaderByHash_NilBlockNotCached(t *testing.T) {
	t.Parallel()

	// A nil block with no error must not poison the cache with an empty
	// hash. The typed nil keeps the fake's return honest without an
	// untyped (nil, nil) that the nilnil linter rejects.
	client := &fakeSlotClient{}
	client.blockFn = func(_ context.Context, _ uint64) (*Block, error) { return (*Block)(nil), nil }
	f := testFetcher(client)

	raw, err := f.FetchBlock(context.Background(), 7)
	require.NoError(t, err)
	assert.Nil(t, raw)

	_, err = f.FetchBlockHeaderByHash(context.Background(), "anything")
	assert.True(t, errors.IsErrNotFound(err))
}

func TestFetcher_FetchBlock_CtxErrorPassthrough(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{}
	client.blockFn = func(_ context.Context, _ uint64) (*Block, error) {
		return nil, context.Canceled
	}
	f := testFetcher(client)

	_, err := f.FetchBlock(context.Background(), 5)
	require.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// Close.
// ---------------------------------------------------------------------------

func TestFetcher_Close(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{}
	f := testFetcher(client)
	require.NoError(t, f.Close())

	assert.Equal(t, 1, client.closeCallCount())

	// Never-connected fetcher: Close is a no-op.
	require.NoError(t, NewFetcherFromConfigForTest().Close())
}

// Wrap-error contracts exercised through the public surface.
func TestFetcher_ErrorContracts(t *testing.T) {
	t.Parallel()

	// The sentinel is disjoint from the transient not-found substring so no
	// consumer can accidentally retry on a skipped height.
	assert.False(t, errors.IsErrNotFound(ErrHeightSkipped))

	skipped := fmt.Errorf("slot %d below confirmed tip %d: %w", 5, 6, ErrHeightSkipped)
	assert.ErrorIs(t, skipped, ErrHeightSkipped)
	assert.False(t, errors.IsErrNotFound(skipped))

	transient := fmt.Errorf("slot %d not found: %w", 6, errNotConfirmed)
	assert.True(t, errors.IsErrNotFound(transient))
	assert.NotErrorIs(t, transient, ErrHeightSkipped)
}
