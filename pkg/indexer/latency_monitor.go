package indexer

// latency_monitor.go owns the rolling network-latency window and the breach
// decision for one chain. It is the single source of truth for both the
// sampling history and the "chain cannot keep up" verdict, so every sample
// is observed in commit order by exactly one caller.

import (
	"fmt"
	"sync"
	"time"
)

// minLatencyWindow floors the window size; a monitor with a zero or negative
// window would never reach full-window state and could never report metrics
// over a meaningful sample set.
const minLatencyWindow = 1

// NetworkLatencyMetrics is a point-in-time snapshot of the latency monitor,
// consumed by the health server and by the typed breach error. AverageMs is
// the rolling mean over the currently held samples; WindowSize is the
// configured window; ThresholdMs is 0 when enforcement is off; LastBlock is
// the most recent committed block recorded — informational for breach
// post-mortems only, never reported by the health server.
type NetworkLatencyMetrics struct {
	AverageMs   int64
	WindowSize  int
	ThresholdMs int64
	LastBlock   int64
}

// LatencyBreachError is returned by Record when the rolling average first
// exceeds the configured threshold. It carries the measurement snapshot that
// produced the verdict so the caller can log and propagate the decision
// without re-reading the monitor.
type LatencyBreachError struct {
	AverageMs   int64
	ThresholdMs int64
	WindowSize  int
	LastBlock   int64
}

func (e *LatencyBreachError) Error() string {
	return fmt.Sprintf(
		"network latency exceeded: average fetch of %dms over the last %d blocks exceeds the %dms threshold (last recorded block %d); at this pace the indexer cannot catch up with the network tip — co-locate the generator with the node or use a lower-latency deployment location",
		e.AverageMs, e.WindowSize, e.ThresholdMs, e.LastBlock,
	)
}

// NetworkLatencyMonitor keeps a fixed-size ring buffer of per-block fetch
// latencies plus a running total, enabling O(1) average updates per sample.
// While the window fills, samples are recorded only; once full, every new
// sample evicts the oldest one and the average is recomputed. Tracking is
// always on (even with a zero threshold, i.e. enforcement off); the breach
// verdict is evaluated only once the window is full, and it is sticky: after
// a breach the window is frozen so post-mortem metrics reflect the state
// that tripped the wire.
type NetworkLatencyMonitor struct {
	mu         sync.RWMutex
	samples    []time.Duration
	head       int
	count      int
	total      time.Duration
	threshold  time.Duration
	windowSize int
	lastBlock  int64
	breached   bool
}

// NewNetworkLatencyMonitor creates a monitor over the given window size.
// A threshold of zero means tracking only — no enforcement.
func NewNetworkLatencyMonitor(threshold time.Duration, windowSize int) *NetworkLatencyMonitor {
	if windowSize < minLatencyWindow {
		windowSize = minLatencyWindow
	}

	return &NetworkLatencyMonitor{
		samples:    make([]time.Duration, windowSize),
		threshold:  threshold,
		windowSize: windowSize,
	}
}

// Record adds the fetch latency of the given committed block to the window.
// Samples are recorded even when enforcement is off; failed fetches must not
// reach this method so the window always holds the last N successfully
// committed blocks. Once the window is full and enforcement is armed
// (threshold > 0), the recomputed average is compared against the threshold
// and a *LatencyBreachError is returned exactly once when it exceeds it;
// every subsequent call is a no-op.
func (m *NetworkLatencyMonitor) Record(blockNum int64, latency time.Duration) error {
	if latency < 0 {
		latency = 0
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.breached {
		return nil
	}

	if m.count == m.windowSize {
		m.total -= m.samples[m.head]
		m.samples[m.head] = latency
		m.head = (m.head + 1) % m.windowSize
	} else {
		m.samples[(m.head+m.count)%m.windowSize] = latency
		m.count++
	}
	m.total += latency
	m.lastBlock = blockNum

	if m.threshold > 0 && m.count == m.windowSize {
		if average := m.total / time.Duration(m.count); average > m.threshold {
			m.breached = true
			return &LatencyBreachError{
				AverageMs:   average.Milliseconds(),
				ThresholdMs: m.threshold.Milliseconds(),
				WindowSize:  m.windowSize,
				LastBlock:   m.lastBlock,
			}
		}
	}

	return nil
}

// WindowFull reports whether the rolling window has filled. Until then the
// average covers only the samples recorded so far and is not yet
// representative of a full window.
func (m *NetworkLatencyMonitor) WindowFull() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.count == m.windowSize
}

// Metrics returns a snapshot of the current window state for observability.
func (m *NetworkLatencyMonitor) Metrics() NetworkLatencyMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var averageMs int64
	if m.count > 0 {
		averageMs = m.total.Milliseconds() / int64(m.count)
	}

	return NetworkLatencyMetrics{
		AverageMs:   averageMs,
		WindowSize:  m.windowSize,
		ThresholdMs: m.threshold.Milliseconds(),
		LastBlock:   m.lastBlock,
	}
}
