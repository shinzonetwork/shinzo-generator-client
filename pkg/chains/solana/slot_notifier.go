package solana

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"

	"github.com/gagliardetto/solana-go/rpc/ws"
)

const (
	// hintStallTimeout bounds one AwaitHint wait: a healthy cluster processes
	// slots every ~400ms, so a hint for any height ≥ the requested slot lands
	// well inside this window. On expiry the waiter flips to blind fetches
	// (today's polling behavior) until the next hint arrives, so a silent or
	// broken websocket never adds more than one timeout of latency per
	// stall episode.
	hintStallTimeout = 5 * time.Second
	// reconnectBackoffBase is the first reconnect delay after a websocket
	// failure.
	reconnectBackoffBase = 500 * time.Millisecond
	// reconnectBackoffMax caps the exponential reconnect delay: the notifier
	// retries forever (soft-fallback policy), but never naps longer than this
	// between attempts.
	reconnectBackoffMax = 30 * time.Second
	// reconnectBackoffFactor doubles the reconnect delay after every failed
	// attempt until reconnectBackoffMax caps it.
	reconnectBackoffFactor = 2
)

// Test seams for the timing constants, mirroring the execCommand-style
// package-var seams used elsewhere in this codebase.
var (
	hintStallTimeoutVar     = hintStallTimeout     //nolint:gochecknoglobals // test seam
	reconnectBackoffBaseVar = reconnectBackoffBase //nolint:gochecknoglobals // test seam
)

// slotSubscription is the subset of *ws.SlotSubscription the notifier uses,
// kept as an interface so tests can inject a fake without dialing.
type slotSubscription interface {
	Recv(ctx context.Context) (*ws.SlotResult, error)
	Unsubscribe()
}

// slotSubscriber produces one subscription from a live websocket connection
// and tears the connection down. It abstracts *ws.Client so the reconnect
// loop can be driven by fakes.
type slotSubscriber interface {
	SlotSubscribe() (slotSubscription, error)
	Close()
}

// slotDialer opens websocket connections and yields slot subscribers. The
// production implementation dials the configured wss endpoint; tests inject
// fakes here.
type slotDialer interface {
	Dial(ctx context.Context) (slotSubscriber, error)
}

// wsDialer is the production slotDialer: it connects to the configured
// websocket endpoint and hands out slot subscriptions. The solana-go client
// ships its own ping/pong keepalive, so dead connections surface as Recv
// errors rather than silent hangs.
type wsDialer struct {
	url     string
	headers http.Header
}

// newWSDialer builds the production dialer for one endpoint. The optional
// header parity matches the HTTP client's api_key/api_key_type handling.
func newWSDialer(url, apiKey, apiKeyType string) *wsDialer {
	return &wsDialer{
		url:     url,
		headers: wsAuthHeaders(apiKey, apiKeyType),
	}
}

// Dial connects to the websocket endpoint. Dial failures carry the
// provider's HTTP status/body when available, so a rejected key or an
// endpoint without websocket support is diagnosable from the log alone.
func (d *wsDialer) Dial(ctx context.Context) (slotSubscriber, error) {
	client, err := ws.ConnectWithOptions(ctx, d.url, &ws.Options{
		HttpHeader: d.headers,
	})
	if err != nil {
		return nil, err
	}
	return &wsSlotSubscriber{client: client}, nil
}

// wsSlotSubscriber adapts *ws.Client to the slotSubscriber seam.
type wsSlotSubscriber struct {
	client *ws.Client
}

func (s *wsSlotSubscriber) SlotSubscribe() (slotSubscription, error) {
	return s.client.SlotSubscribe()
}

func (s *wsSlotSubscriber) Close() {
	s.client.Close()
}

// slotNotifier consumes slotSubscribe notifications and exposes them as
// advisory readiness hints for FetchBlock.
//
// Hint semantics (deliberately conservative): slotSubscribe fires at
// processed commitment, notifications can regress, duplicate, and reference
// fork slots that never confirm. A hint therefore means only "a block was
// observed at this slot; fetching now is likely to succeed" — never "unhinted
// heights are empty". Skip classification stays in FetchBlock's tip
// comparison; the notifier never infers skips from notification gaps.
//
// Concurrency: any number of FetchBlock callers may AwaitHint concurrently.
// A single hint must wake all of them, so wakeups use the close-and-replace
// broadcast pattern (one channel closed per hint) instead of a shared data
// channel whose single value one waiter could consume. Hint state crosses
// goroutines via atomics; the mutex only guards the signal channel swap.
type slotNotifier struct {
	url    string
	dialer slotDialer
	done   chan struct{}

	// hintedMax tracks the highest observed slot. Monotonic: hints arrive
	// unordered and can regress, so every update goes through max.
	hintedMax atomic.Uint64
	// stalled records that a waiter already hit the stall timeout. While
	// set, AwaitHint returns immediately (blind fetches) so a sustained
	// websocket outage adds no per-fetch latency. Cleared by the receive
	// goroutine on every hint.
	stalled atomic.Bool

	mu       sync.Mutex
	signal   chan struct{} // closed on every hint; nil after stop
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// newSlotNotifier wires a notifier around the production dialer. It performs
// no network I/O; start launches the connection loop.
func newSlotNotifier(url, apiKey, apiKeyType string) *slotNotifier {
	return newSlotNotifierWithDialer(url, newWSDialer(url, apiKey, apiKeyType))
}

// newSlotNotifierWithDialer lets tests inject a scripted dialer.
func newSlotNotifierWithDialer(url string, dialer slotDialer) *slotNotifier {
	return &slotNotifier{
		url:    url,
		dialer: dialer,
		done:   make(chan struct{}),
	}
}

// start launches the connection loop. Call once per notifier. The parent
// context seeds the loop's lifetime (cancellation is deliberately detached
// by the caller, since e.g. Connect's dial-timeout ctx ends before the
// notifier does); stop is the only shutdown path.
func (n *slotNotifier) start(ctx context.Context) {
	n.mu.Lock()
	n.signal = make(chan struct{})
	n.mu.Unlock()

	n.wg.Add(1)
	go n.run(ctx)
}

// AwaitHint blocks until a slot >= slot has been observed (advisory
// readiness), the stall timeout expires (the caller proceeds blind), or ctx
// is cancelled. A disabled (nil) or stopped notifier returns immediately.
// The deadline is fixed per call: hints below the requested slot do not
// extend it.
func (n *slotNotifier) AwaitHint(ctx context.Context, slot uint64) error {
	if n == nil {
		return nil
	}
	deadline := time.NewTimer(hintStallTimeoutVar)
	defer deadline.Stop()

	for {
		if n.stopped() || n.stalled.Load() || n.hintedMax.Load() >= slot {
			return nil
		}
		n.mu.Lock()
		signal := n.signal
		n.mu.Unlock()

		if signal == nil {
			return nil // stopped between checks: proceed blind
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-signal:
			// Re-check state: any hint may have satisfied the slot.
		case <-deadline.C:
			if n.stalled.CompareAndSwap(false, true) {
				logger.Sugar.Warnf(
					"slot notifications silent for %v; fetching blind until the next notification",
					hintStallTimeoutVar)
			}
			return nil
		}
	}
}

// stop shuts the notifier down: the connection loop exits, the websocket
// connection is torn down, and all AwaitHint waiters are released.
// Idempotent and safe on a nil notifier.
func (n *slotNotifier) stop() {
	if n == nil {
		return
	}
	n.stopOnce.Do(func() {
		n.mu.Lock()
		signal := n.signal
		n.signal = nil
		n.mu.Unlock()
		if signal != nil {
			close(signal)
		}
		close(n.done)
	})
	n.wg.Wait()
}

func (n *slotNotifier) stopped() bool {
	select {
	case <-n.done:
		return true
	default:
		return false
	}
}

// run is the connection loop: dial, subscribe, consume notifications, and
// reconnect with capped exponential backoff on every failure — forever,
// until stop. Indexing continues through the polling path during outages.
func (n *slotNotifier) run(parent context.Context) {
	defer n.wg.Done()

	// The run context outlives any caller-scoped cancellation of parent
	// (stop releases any Recv blocked on a silent connection immediately
	// via the done watcher).
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		select {
		case <-n.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	backoff := reconnectBackoffBaseVar
	for {
		if n.stopped() {
			return
		}

		subscriber, err := n.dialer.Dial(ctx)
		if err == nil {
			var sub slotSubscription
			sub, err = subscriber.SlotSubscribe()
			if err == nil {
				backoff = reconnectBackoffBaseVar
				logger.Sugar.Infof("slot notifications active (%s)", n.url)
				n.consume(ctx, sub)
			}
			subscriber.Close()
		}
		if n.stopped() {
			return
		}
		if err != nil {
			logger.Sugar.Warnf("slot notification reconnect in %v: %v", backoff, err)
		}

		select {
		case <-n.done:
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*reconnectBackoffFactor, reconnectBackoffMax)
	}
}

// consume drains one subscription, translating notifications into hints.
// It never blocks on consumer-side backpressure: state updates are atomics
// and the broadcast is a channel close, so the solana-go internal buffer
// (capped at 200) can never overflow due to a slow AwaitHint waiter.
func (n *slotNotifier) consume(ctx context.Context, sub slotSubscription) {
	// Unsubscribe before the caller closes the connection: on the production
	// client the unsubscribe message needs the socket still open.
	defer sub.Unsubscribe()
	for {
		res, err := sub.Recv(ctx)
		if err != nil {
			if !n.stopped() {
				logger.Sugar.Debugf("slot notification stream ended: %v", err)
			}
			return
		}
		if res != nil {
			n.record(res.Slot)
		}
	}
}

// record applies one notification: max-monotonic hint update, stall clear,
// broadcast. Called only from the connection goroutine.
func (n *slotNotifier) record(slot uint64) {
	for {
		current := n.hintedMax.Load()
		if slot <= current {
			break
		}
		if n.hintedMax.CompareAndSwap(current, slot) {
			break
		}
	}
	n.stalled.Store(false)

	n.mu.Lock()
	if n.signal != nil {
		close(n.signal)
		n.signal = make(chan struct{})
	}
	n.mu.Unlock()
}

// wsAuthHeaders builds the websocket dial headers for header-authenticated
// providers, mirroring the HTTP client's api_key/api_key_type handling.
func wsAuthHeaders(apiKey, apiKeyType string) http.Header {
	if apiKey == "" {
		return nil
	}
	name := apiKeyType
	if name == "" {
		name = "x-api-key"
	}
	return http.Header{name: []string{apiKey}}
}
