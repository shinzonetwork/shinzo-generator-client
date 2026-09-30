package solana

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
)

// ---------------------------------------------------------------------------
// FetchBlock × slot-notification gate. The gate is solana-internal: the
// generic processor is untouched and keeps its blind dispatch; every test
// here pins that the gate only delays the getBlock, never reclassifies.
// ---------------------------------------------------------------------------

// newGatedTestFetcher wires a pre-connected fetcher with a started notifier
// whose first dial is parked on the returned gate — no real network I/O.
func newGatedTestFetcher(t *testing.T, client *fakeSlotClient) (*Fetcher, *fakeSlotDialer) {
	t.Helper()
	// A non-nil gate is what parks Dial: without it the connection loop
	// would proceed immediately and connectGate's close would panic.
	dialer := &fakeSlotDialer{gate: make(chan struct{})}
	f := NewFetcher(client)
	f.notifier = newSlotNotifierWithDialer("ws://test", dialer)
	f.notifier.start(context.Background())
	t.Cleanup(f.notifier.stop)
	return f, dialer
}

func connectGate(t *testing.T, dialer *fakeSlotDialer) {
	t.Helper()
	close(dialer.gate)
	waitForCondition(t, 2*time.Second, func() bool { return dialer.dialCount() == 1 }, "first dial")
}

// TestFetcher_NotificationGate_NoRPCBeforeHint pins the core win: with the
// gate active and no hint yet, FetchBlock issues no getBlock at all — the
// old path would have polled the RPC every 3s.
func TestFetcher_NotificationGate_NoRPCBeforeHint(t *testing.T) {
	client := &fakeSlotClient{
		tip: 1000,
		blockFn: func(_ context.Context, slot uint64) (*Block, error) {
			return fakeBlock(slot), nil
		},
	}
	f, dialer := newGatedTestFetcher(t, client)
	connectGate(t, dialer)

	result := make(chan any, 1)
	errs := make(chan error, 1)
	go func() {
		raw, err := f.FetchBlock(context.Background(), 200)
		if err != nil {
			errs <- err
			return
		}
		result <- raw
	}()

	time.Sleep(150 * time.Millisecond)
	require.Zero(t, client.blockCallCount(), "no getBlock may be issued before a hint arrives")

	dialer.latest().sub.push(200)
	select {
	case raw := <-result:
		assert.Equal(t, uint64(200), raw.(*Block).Slot)
	case err := <-errs:
		t.Fatalf("FetchBlock failed after the hint: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("FetchBlock did not complete after the hint")
	}
	assert.Equal(t, 1, client.blockCallCount(), "exactly one fetch per hinted slot")
}

// TestFetcher_NotificationGate_LaterHintUnlocksSkipped pins the max-semantics
// unlock: a hint for a later slot opens the gate for an unhinted (skipped)
// slot, and classification still decides — hints never mark skips.
func TestFetcher_NotificationGate_LaterHintUnlocksSkipped(t *testing.T) {
	client := &fakeSlotClient{
		tip: 1000,
		blockFn: func(_ context.Context, _ uint64) (*Block, error) {
			return nil, fmt.Errorf("slot skipped: %w", errSlotSkipped)
		},
	}
	f, dialer := newGatedTestFetcher(t, client)
	connectGate(t, dialer)

	// Only slot 1005 is ever hinted; 999 gets no notification of its own.
	dialer.latest().sub.push(1005)

	_, err := f.FetchBlock(context.Background(), 999)
	require.Error(t, err)
	assert.ErrorIs(t, err, chains.ErrHeightSkipped,
		"the gate must not change skip classification: %v", err)
	assert.False(t, errors.IsErrNotFound(err))
}

// TestFetcher_NotificationGate_StallProceedsBlind pins the fallback ladder:
// a silent notifier degrades to an immediate blind fetch after one stall
// timeout instead of waiting forever.
func TestFetcher_NotificationGate_StallProceedsBlind(t *testing.T) {
	// Sequential: mutates the stall-timeout seam var.
	old := hintStallTimeoutVar
	hintStallTimeoutVar = 60 * time.Millisecond
	t.Cleanup(func() { hintStallTimeoutVar = old })

	client := &fakeSlotClient{
		tip: 1000,
		blockFn: func(_ context.Context, slot uint64) (*Block, error) {
			return fakeBlock(slot), nil
		},
	}
	f, dialer := newGatedTestFetcher(t, client)
	connectGate(t, dialer)

	start := time.Now()
	raw, err := f.FetchBlock(context.Background(), 100)
	require.NoError(t, err)
	require.NotNil(t, raw)
	assert.GreaterOrEqual(t, time.Since(start), 40*time.Millisecond,
		"the fetch must wait out the stall timeout, not fetch instantly")

	assert.True(t, f.notifier.stalled.Load())

	// While stalled, the next fetch is immediate.
	start = time.Now()
	_, err = f.FetchBlock(context.Background(), 101)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 20*time.Millisecond, "stalled fetches must not wait")
}

// TestFetcher_NotificationGate_StallResumesOnHint pins the recovery: a hint
// clears the stall, and later unhinted slots are gated again.
func TestFetcher_NotificationGate_StallResumesOnHint(t *testing.T) {
	// Sequential: mutates the stall-timeout seam var. 250ms keeps the
	// 100ms "still gated" probe below one stall deadline.
	old := hintStallTimeoutVar
	hintStallTimeoutVar = 250 * time.Millisecond
	t.Cleanup(func() { hintStallTimeoutVar = old })

	client := &fakeSlotClient{
		tip: 1000,
		blockFn: func(_ context.Context, slot uint64) (*Block, error) {
			return fakeBlock(slot), nil
		},
	}
	f, dialer := newGatedTestFetcher(t, client)
	connectGate(t, dialer)

	// Blind fetch while silent: stall flips on.
	_, err := f.FetchBlock(context.Background(), 100)
	require.NoError(t, err)

	// A hint arrives: stall clears, and unhinted slot 103 is gated again.
	dialer.latest().sub.push(102)
	waitForCondition(t, 2*time.Second, func() bool { return !f.notifier.stalled.Load() },
		"a hint must clear the stall")

	done := make(chan struct{})
	go func() {
		_, _ = f.FetchBlock(context.Background(), 103)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case <-done:
		t.Fatalf("unhinted fetch must be gated after the stall cleared")
	default:
	}

	dialer.latest().sub.push(104)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("hinted fetch must complete after the gate opens")
	}
}

// TestFetcher_NotificationGate_CtxCancel pins prompt cancellation: a parked
// FetchBlock must not outlive its context.
func TestFetcher_NotificationGate_CtxCancel(t *testing.T) {
	client := &fakeSlotClient{
		tip: 1000,
		blockFn: func(_ context.Context, slot uint64) (*Block, error) {
			return fakeBlock(slot), nil
		},
	}
	f, _ := newGatedTestFetcher(t, client) // gate never opens: no hints

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := f.FetchBlock(ctx, 100)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatalf("FetchBlock did not honor ctx cancellation while gated")
	}
}

// TestFetcher_NotificationGate_UngatedWithoutNotifier pins the disabled
// path: without a notifier (NewFetcher / no ws_url) behavior is byte-identical
// to the pre-Phase-5 fetcher.
func TestFetcher_NotificationGate_UngatedWithoutNotifier(t *testing.T) {
	client := &fakeSlotClient{
		tip: 1000,
		blockFn: func(_ context.Context, slot uint64) (*Block, error) {
			return fakeBlock(slot), nil
		},
	}
	f := NewFetcher(client)

	start := time.Now()
	raw, err := f.FetchBlock(context.Background(), 100)
	require.NoError(t, err)
	require.NotNil(t, raw)
	assert.Less(t, time.Since(start), 50*time.Millisecond, "ungated fetch must be immediate")
}

// TestFetcher_CloseStopsNotifier pins the lifecycle: Close tears the gate
// down deterministically and releases parked waiters.
func TestFetcher_CloseStopsNotifier(t *testing.T) {
	client := &fakeSlotClient{
		tip: 1000,
		blockFn: func(_ context.Context, slot uint64) (*Block, error) {
			return fakeBlock(slot), nil
		},
	}
	f, dialer := newGatedTestFetcher(t, client)
	connectGate(t, dialer)
	sub := dialer.latest()

	waiter := make(chan error, 1)
	go func() {
		_, err := f.FetchBlock(context.Background(), 1<<40)
		waiter <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the fetch park on the gate

	require.NoError(t, f.Close())
	select {
	case err := <-waiter:
		// The waiter proceeds blind after stop; the closed client then
		// rejects the getBlock — either way it must return promptly.
		_ = err
	case <-time.After(2 * time.Second):
		t.Fatalf("Close did not release the parked fetch")
	}

	assert.Equal(t, 1, client.closeCallCount(), "the RPC client must be closed")
	// The notifier stopped: further waits return immediately (blind).
	awaitHintDone(t, f.notifier, 1<<41, time.Second)
	assert.Equal(t, 1, sub.closeCount(), "the ws connection must be closed exactly once")
}
