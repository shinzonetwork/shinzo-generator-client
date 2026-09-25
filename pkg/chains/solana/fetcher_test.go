package solana

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
)

// ---------------------------------------------------------------------------
// Fake rpcClient (test seam).
// ---------------------------------------------------------------------------

type fakeSlotClient struct {
	mu sync.Mutex

	blockFn   func(ctx context.Context, slot uint64) (*Block, error)
	archiveFn func(ctx context.Context, slot uint64) (*Block, error)
	tip       uint64
	tipErr    error

	blockCalls, archiveCalls, tipCalls, closeCalls int
}

func (f *fakeSlotClient) GetBlock(ctx context.Context, slot uint64) (*Block, error) {
	f.mu.Lock()
	f.blockCalls++
	f.mu.Unlock()
	return f.blockFn(ctx, slot)
}

func (f *fakeSlotClient) GetBlockFromArchive(ctx context.Context, slot uint64) (*Block, error) {
	f.mu.Lock()
	f.archiveCalls++
	f.mu.Unlock()
	if f.archiveFn == nil {
		return nil, errArchiveNotConfigured
	}
	return f.archiveFn(ctx, slot)
}

func (f *fakeSlotClient) GetSlot(context.Context) (uint64, error) {
	f.mu.Lock()
	f.tipCalls++
	f.mu.Unlock()
	if f.tipErr != nil {
		return 0, f.tipErr
	}
	return f.tip, nil
}

func (f *fakeSlotClient) Close() error {
	f.mu.Lock()
	f.closeCalls++
	f.mu.Unlock()
	return nil
}

func (f *fakeSlotClient) calls() (block, archive, tipCalls, closeCalls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blockCalls, f.archiveCalls, f.tipCalls, f.closeCalls
}

func (f *fakeSlotClient) blockCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blockCalls
}

func (f *fakeSlotClient) closeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCalls
}

// testFetcher wires a fetcher with a pre-connected fake client.
func testFetcher(client *fakeSlotClient) *Fetcher {
	return NewFetcher(client)
}

// ---------------------------------------------------------------------------
// NewFetcherFromConfig.
// ---------------------------------------------------------------------------

func TestNewFetcherFromConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Solana: config.SolanaConfig{
			RPCURL:                         "https://api.mainnet-beta.solana.com",
			ArchiveRPCURL:                  "https://archive.example.com",
			APIKey:                         "secret",
			APIKeyType:                     "X-Api-Key",
			Commitment:                     "finalized",
			MaxSupportedTransactionVersion: 1,
			Rewards:                        new(false),
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
		Solana: config.SolanaConfig{RPCURL: "https://r", Commitment: "confirmed"},
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

	assert.NoError(t, f.Close())
}

// NewFetcherFromConfigForTest mirrors NewFetcherFromConfig with a minimal
// config, used where only the disconnected state matters.
func NewFetcherFromConfigForTest() *Fetcher {
	f, _ := NewFetcherFromConfig(&config.Config{
		Solana: config.SolanaConfig{RPCURL: "https://r", Commitment: "confirmed"},
	})
	return f
}

// ---------------------------------------------------------------------------
// FetchBlock classification.
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlock_Success(t *testing.T) {
	t.Parallel()

	want := &Block{Slot: 42}
	client := &fakeSlotClient{
		tip:     100,
		blockFn: func(_ context.Context, _ uint64) (*Block, error) { return want, nil },
	}
	f := testFetcher(client)

	raw, err := f.FetchBlock(context.Background(), 42)
	require.NoError(t, err)
	assert.Same(t, want, raw.(*Block))

	block, archive, tip, _ := client.calls()
	assert.Equal(t, 1, block)
	assert.Equal(t, 0, archive)
	assert.Equal(t, 0, tip, "successful fetches never query the tip")
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
			assert.ErrorIs(t, err, chains.ErrHeightSkipped, "strictly below tip means permanently skipped: %v", err)
			assert.False(t, errors.IsErrNotFound(err),
				"skipped heights must not enter the not-found retry loop")

			block, _, tip, _ := client.calls()
			assert.Equal(t, 1, block)
			assert.Equal(t, 1, tip, "classification needs one tip query")
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
			assert.NotErrorIs(t, err, chains.ErrHeightSkipped)
		})
	}
}

func TestFetcher_FetchBlock_TipQueryFails(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{
		tipErr: fmt.Errorf("tip connection refused"),
		blockFn: func(_ context.Context, _ uint64) (*Block, error) {
			return nil, errSlotSkipped
		},
	}
	f := testFetcher(client)

	_, err := f.FetchBlock(context.Background(), 999)
	require.Error(t, err)
	assert.True(t, errors.IsErrNotFound(err),
		"unclassifiable misses stay transient and get retried, never declared skipped: %v", err)
	assert.NotErrorIs(t, err, chains.ErrHeightSkipped)
}

// ---------------------------------------------------------------------------
// FetchBlock — pruned below the node's ledger floor.
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

	block, archive, _, _ := client.calls()
	assert.Equal(t, 1, block)
	assert.Equal(t, 1, archive)
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
	assert.ErrorIs(t, err, chains.ErrHeightSkipped)
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
	assert.NotErrorIs(t, err, chains.ErrHeightSkipped,
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
// FetchHighestBlockNumber and Close.
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

func TestFetcher_Close(t *testing.T) {
	t.Parallel()

	client := &fakeSlotClient{}
	f := testFetcher(client)
	require.NoError(t, f.Close())

	client.mu.Lock()
	closeCalls := client.closeCalls
	client.mu.Unlock()
	assert.Equal(t, 1, closeCalls)

	// Never-connected fetcher: Close is a no-op.
	require.NoError(t, NewFetcherFromConfigForTest().Close())
}

// Wrap-error contracts exercised through the public surface.
func TestFetcher_ErrorContracts(t *testing.T) {
	t.Parallel()

	// The sentinel is disjoint from the transient not-found substring so no
	// consumer can accidentally retry on a skipped height.
	assert.False(t, errors.IsErrNotFound(chains.ErrHeightSkipped))

	skipped := fmt.Errorf("slot %d below confirmed tip %d: %w", 5, 6, chains.ErrHeightSkipped)
	assert.ErrorIs(t, skipped, chains.ErrHeightSkipped)
	assert.False(t, errors.IsErrNotFound(skipped))

	transient := fmt.Errorf("slot %d not found: %w", 6, errNotConfirmed)
	assert.True(t, errors.IsErrNotFound(transient))
	assert.NotErrorIs(t, transient, chains.ErrHeightSkipped)
}
