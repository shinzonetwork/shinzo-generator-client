package solana

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go/rpc/ws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fakes: dialer → subscriber → subscription, mirroring the production seam
// chain (slotDialer → slotSubscriber → slotSubscription).
// ---------------------------------------------------------------------------

// errFakeStreamEnded is returned by a closed fake subscription's Recv,
// standing in for the solana-go ErrSubscriptionClosed that a dropped
// connection produces.
var errFakeStreamEnded = errors.New("fake subscription stream ended")

type fakeSlotSubscription struct {
	slots  chan *ws.SlotResult
	errs   chan error
	mu     sync.Mutex
	unsubs int
}

func newFakeSlotSubscription() *fakeSlotSubscription {
	return &fakeSlotSubscription{
		slots: make(chan *ws.SlotResult, 64),
		errs:  make(chan error, 1),
	}
}

func (f *fakeSlotSubscription) Recv(ctx context.Context) (*ws.SlotResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res, ok := <-f.slots:
		if !ok {
			return nil, errFakeStreamEnded
		}
		return res, nil
	case err, ok := <-f.errs:
		if !ok {
			return nil, errFakeStreamEnded
		}
		return nil, err
	}
}

func (f *fakeSlotSubscription) Unsubscribe() {
	f.mu.Lock()
	f.unsubs++
	f.mu.Unlock()
}

func (f *fakeSlotSubscription) unsubscribeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unsubs
}

// push delivers one slot notification. Buffered: tests never block on a
// slow consumer, mirroring the production contract that the receive
// goroutine must never stall on its own consumer.
func (f *fakeSlotSubscription) push(slot uint64) {
	f.slots <- &ws.SlotResult{Slot: slot}
}

// endStream simulates the subscription dying (connection drop): every
// subsequent Recv fails until the notifier reconnects.
func (f *fakeSlotSubscription) endStream() {
	close(f.errs)
}

type fakeSlotSubscriber struct {
	sub    *fakeSlotSubscription
	mu     sync.Mutex
	closes int
}

func (f *fakeSlotSubscriber) SlotSubscribe() (slotSubscription, error) {
	return f.sub, nil
}

func (f *fakeSlotSubscriber) Close() {
	f.mu.Lock()
	f.closes++
	f.mu.Unlock()
}

func (f *fakeSlotSubscriber) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

// fakeSlotDialer hands out a fresh subscriber per dial. When gate is set,
// the first dial blocks until the gate closes (or its ctx cancels), so a
// test can configure the notifier before the connection loop proceeds
// without any real network I/O.
type fakeSlotDialer struct {
	gate        chan struct{}
	mu          sync.Mutex
	dials       int
	failNext    int
	subscribers []*fakeSlotSubscriber
}

func newFakeSlotDialer() *fakeSlotDialer {
	return &fakeSlotDialer{}
}

func (d *fakeSlotDialer) Dial(ctx context.Context) (slotSubscriber, error) {
	d.mu.Lock()
	gate := d.gate
	fail := d.failNext > 0
	if fail {
		d.failNext--
	}
	d.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.dials++
	if fail {
		return nil, errors.New("dial refused by fake endpoint")
	}
	sub := &fakeSlotSubscriber{sub: newFakeSlotSubscription()}
	d.subscribers = append(d.subscribers, sub)
	return sub, nil
}

func (d *fakeSlotDialer) dialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials
}

// latest returns the most recent subscriber (nil before any dial succeeds).
func (d *fakeSlotDialer) latest() *fakeSlotSubscriber {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.subscribers) == 0 {
		return nil
	}
	return d.subscribers[len(d.subscribers)-1]
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

// newGatedNotifier builds a started notifier whose first dial is parked on
// the returned gate. Tests configure tunables (via the seam vars), then
// close the gate to let the connection proceed.
func newGatedNotifier(t *testing.T) (*slotNotifier, *fakeSlotDialer) {
	t.Helper()
	dialer := &fakeSlotDialer{gate: make(chan struct{})}
	n := newSlotNotifierWithDialer("ws://test", dialer)
	n.start(context.Background())
	t.Cleanup(n.stop)
	return n, dialer
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v: %s", timeout, msg)
}

func awaitHintDone(t *testing.T, n *slotNotifier, slot uint64, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- n.AwaitHint(ctx, slot) }()
	select {
	case err := <-done:
		require.NoError(t, err, "AwaitHint(%d)", slot)
	case <-time.After(timeout):
		t.Fatalf("AwaitHint(%d) did not return within %v", slot, timeout)
	}
}

// ---------------------------------------------------------------------------
// AwaitHint behavior.
// ---------------------------------------------------------------------------

func TestSlotNotifier_DisabledNil(t *testing.T) {
	t.Parallel()

	var n *slotNotifier
	require.NoError(t, n.AwaitHint(context.Background(), 100))
	n.stop() // nil-safe no-op
}

func TestSlotNotifier_HintSatisfiesAwait(t *testing.T) {
	t.Parallel()

	n, dialer := newGatedNotifier(t)
	close(dialer.gate)
	waitForCondition(t, 2*time.Second, func() bool { return dialer.dialCount() == 1 }, "first dial")

	// Out-of-order arrivals: 103 first, then 100. hintedMax takes the max.
	dialer.latest().sub.push(103)
	awaitHintDone(t, n, 103, 2*time.Second)
	// A lower slot is satisfied by the higher hint (max semantics).
	awaitHintDone(t, n, 100, time.Second)

	dialer.latest().sub.push(100)
	awaitHintDone(t, n, 101, time.Second)

	// A slot beyond the highest hint still waits — until it arrives.
	blocked := make(chan error, 1)
	go func() { blocked <- n.AwaitHint(context.Background(), 104) }()
	select {
	case err := <-blocked:
		t.Fatalf("AwaitHint(104) returned before its hint: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	dialer.latest().sub.push(104)
	awaitHintDone(t, n, 104, 2*time.Second)
}

func TestSlotNotifier_MultipleWaitersOneHint(t *testing.T) {
	t.Parallel()

	n, dialer := newGatedNotifier(t)
	close(dialer.gate)
	waitForCondition(t, 2*time.Second, func() bool { return dialer.dialCount() == 1 }, "first dial")

	const waiters = 8
	results := make(chan error, waiters)
	for range waiters {
		go func() { results <- n.AwaitHint(context.Background(), 300) }()
	}

	time.Sleep(50 * time.Millisecond)
	dialer.latest().sub.push(300)

	// One hint must wake every waiter (close-and-replace broadcast, not a
	// single-value channel a single waiter could consume).
	for range waiters {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatalf("a waiter was not woken by the hint")
		}
	}
}

func TestSlotNotifier_StallThenBlindThenRecover(t *testing.T) {
	// Sequential: mutates the stall-timeout seam var. 250ms keeps the
	// 100ms "still blocked" probe below one stall deadline.
	old := hintStallTimeoutVar
	hintStallTimeoutVar = 250 * time.Millisecond
	t.Cleanup(func() { hintStallTimeoutVar = old })

	n, dialer := newGatedNotifier(t)
	close(dialer.gate)
	waitForCondition(t, 2*time.Second, func() bool { return dialer.dialCount() == 1 }, "first dial")

	// No hints: the wait degrades to blind after the stall timeout.
	awaitHintDone(t, n, 100, 2*time.Second)
	assert.True(t, n.stalled.Load(), "stall must be flagged after the timeout")

	// While stalled, further waits return immediately (no added latency).
	start := time.Now()
	awaitHintDone(t, n, 200, time.Second)
	assert.Less(t, time.Since(start), 20*time.Millisecond, "stalled waits must be instant")

	// The next hint clears the stall: gating resumes.
	dialer.latest().sub.push(201)
	waitForCondition(t, 2*time.Second, func() bool { return !n.stalled.Load() },
		"a hint must clear the stall")
	blocked := make(chan error, 1)
	go func() { blocked <- n.AwaitHint(context.Background(), 300) }()
	select {
	case err := <-blocked:
		t.Fatalf("AwaitHint(300) returned while stalled mode was cleared but its slot is unhinted: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	dialer.latest().sub.push(300)
	awaitHintDone(t, n, 300, 2*time.Second)
}

func TestSlotNotifier_DeadlineNotExtendedBySubSlotHints(t *testing.T) {
	// Sequential: mutates the stall-timeout seam var.
	old := hintStallTimeoutVar
	hintStallTimeoutVar = 150 * time.Millisecond
	t.Cleanup(func() { hintStallTimeoutVar = old })

	n, dialer := newGatedNotifier(t)
	close(dialer.gate)
	waitForCondition(t, 2*time.Second, func() bool { return dialer.dialCount() == 1 }, "first dial")

	// A hint below the requested slot arrives mid-wait: the per-call
	// deadline must still expire on schedule (no indefinite starvation).
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		time.Sleep(30 * time.Millisecond)
		dialer.latest().sub.push(50)
		done <- n.AwaitHint(context.Background(), 100)
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
		elapsed := time.Since(start)
		assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond, "deadline must hold despite sub-slot hints")
		assert.Less(t, elapsed, time.Second, "deadline must not be extended by sub-slot hints")
		assert.True(t, n.stalled.Load())
	case <-time.After(2 * time.Second):
		t.Fatalf("AwaitHint did not time out to the stall deadline")
	}
}

func TestSlotNotifier_CtxCancelDuringWait(t *testing.T) {
	t.Parallel()

	n, _ := newGatedNotifier(t) // gate stays closed: no hints ever arrive

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.AwaitHint(ctx, 500) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatalf("AwaitHint did not honor ctx cancellation")
	}
}

// ---------------------------------------------------------------------------
// Connection loop: reconnects, dial failures, shutdown.
// ---------------------------------------------------------------------------

func TestSlotNotifier_ReconnectAfterStreamEnd(t *testing.T) {
	// Sequential: mutates the backoff seam var.
	old := reconnectBackoffBaseVar
	reconnectBackoffBaseVar = 5 * time.Millisecond
	t.Cleanup(func() { reconnectBackoffBaseVar = old })

	n, dialer := newGatedNotifier(t)
	close(dialer.gate)
	waitForCondition(t, 2*time.Second, func() bool { return dialer.dialCount() == 1 }, "first dial")
	first := dialer.latest()

	first.sub.push(10)
	awaitHintDone(t, n, 10, 2*time.Second)

	first.sub.endStream()
	waitForCondition(t, 5*time.Second, func() bool { return dialer.dialCount() == 2 }, "reconnect dial")
	assert.Equal(t, 1, first.closeCount(), "the dead connection must be closed before redialing")

	dialer.latest().sub.push(20)
	awaitHintDone(t, n, 20, 2*time.Second)
}

func TestSlotNotifier_DialFailuresThenSuccess(t *testing.T) {
	// Sequential: mutates the backoff seam var.
	old := reconnectBackoffBaseVar
	reconnectBackoffBaseVar = 5 * time.Millisecond
	t.Cleanup(func() { reconnectBackoffBaseVar = old })

	dialer := newFakeSlotDialer()
	dialer.failNext = 2
	n := newSlotNotifierWithDialer("ws://test", dialer)
	n.start(context.Background())
	t.Cleanup(n.stop)

	waitForCondition(t, 5*time.Second, func() bool { return dialer.dialCount() == 3 },
		"two failed dials must be followed by a successful one")

	dialer.latest().sub.push(42)
	awaitHintDone(t, n, 42, 2*time.Second)
}

func TestSlotNotifier_DialErrorFallsToBlindStall(t *testing.T) {
	// Sequential: mutates both seam vars.
	oldStall := hintStallTimeoutVar
	oldBase := reconnectBackoffBaseVar
	hintStallTimeoutVar = 50 * time.Millisecond
	reconnectBackoffBaseVar = 5 * time.Millisecond
	t.Cleanup(func() {
		hintStallTimeoutVar = oldStall
		reconnectBackoffBaseVar = oldBase
	})

	dialer := newFakeSlotDialer()
	dialer.failNext = 1 << 30 // effectively always fails
	n := newSlotNotifierWithDialer("ws://test", dialer)
	n.start(context.Background())
	t.Cleanup(n.stop)

	// A never-connecting websocket degrades to the blind path after one
	// stall timeout — indexing must not depend on the notifier coming up.
	awaitHintDone(t, n, 100, 2*time.Second)
	assert.True(t, n.stalled.Load())
}

func TestSlotNotifier_StopReleasesWaiters(t *testing.T) {
	t.Parallel()

	n, dialer := newGatedNotifier(t)
	close(dialer.gate)
	waitForCondition(t, 2*time.Second, func() bool { return dialer.dialCount() == 1 }, "first dial")
	sub := dialer.latest()

	waiter := make(chan error, 1)
	go func() { waiter <- n.AwaitHint(context.Background(), 1<<40) }()
	time.Sleep(50 * time.Millisecond) // let the waiter park on the signal

	start := time.Now()
	n.stop()
	select {
	case err := <-waiter:
		require.NoError(t, err, "stop must release waiters without error")
	case <-time.After(2 * time.Second):
		t.Fatalf("stop did not release the parked waiter")
	}
	assert.Less(t, time.Since(start), time.Second, "stop must not wait out the stall timeout")

	// Post-stop behavior: instant return, no further work, idempotent stop.
	awaitHintDone(t, n, 1<<41, time.Second)
	n.stop()
	assert.Equal(t, 1, sub.closeCount(), "the connection must be closed exactly once on stop")
	assert.Equal(t, 1, sub.sub.unsubscribeCount(), "the subscription must be unsubscribed on stop")
}
