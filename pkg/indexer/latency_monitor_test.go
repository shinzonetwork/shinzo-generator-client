package indexer

// latency_monitor_test.go covers the rolling-window semantics: fill-only
// behavior below the full window, breach evaluated exactly once the window
// is full, O(1) eviction keeping the running total consistent, tracking
// without enforcement at threshold 0, the sticky frozen window after a
// breach, the metrics snapshot, and race-safe concurrent access. The
// scenarios are driven by sample sequences so each case exercises the full
// Record → verdict → Metrics path in one pass.

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// latencySample is one Record call, with an optional expectation that the
// sample itself trips the breach verdict.
type latencySample struct {
	block      int64
	latency    time.Duration
	wantBreach bool
}

func TestNetworkLatencyMonitor_WindowSemantics(t *testing.T) {
	tests := []struct {
		name      string
		threshold time.Duration
		window    int
		samples   []latencySample
		// want is the frozen metrics snapshot after the whole sequence; for
		// breaching sequences the window is sticky, so it reflects the state
		// that tripped the wire.
		want NetworkLatencyMetrics
	}{
		{
			name:      "fill below full window never breaches despite samples above threshold",
			threshold: 100 * time.Millisecond,
			window:    4,
			samples: []latencySample{
				{block: 1, latency: 500 * time.Millisecond},
				{block: 2, latency: 500 * time.Millisecond},
				{block: 3, latency: 500 * time.Millisecond},
			},
			want: NetworkLatencyMetrics{AverageMs: 500, WindowSize: 4, ThresholdMs: 100, LastBlock: 3},
		},
		{
			name:      "breach exactly when the window fills",
			threshold: 100 * time.Millisecond,
			window:    3,
			samples: []latencySample{
				{block: 1, latency: 150 * time.Millisecond},
				{block: 2, latency: 150 * time.Millisecond},
				{block: 3, latency: 150 * time.Millisecond, wantBreach: true},
			},
			want: NetworkLatencyMetrics{AverageMs: 150, WindowSize: 3, ThresholdMs: 100, LastBlock: 3},
		},
		{
			name:      "average must strictly exceed the threshold",
			threshold: 200 * time.Millisecond,
			window:    2,
			samples: []latencySample{
				// Exactly at the threshold is not a breach.
				{block: 1, latency: 200 * time.Millisecond},
				{block: 2, latency: 200 * time.Millisecond},
				// Eviction pushes the average past it: (200+201)/2 > 200ms.
				{block: 3, latency: 201 * time.Millisecond, wantBreach: true},
			},
			want: NetworkLatencyMetrics{AverageMs: 200, WindowSize: 2, ThresholdMs: 200, LastBlock: 3},
		},
		{
			name:      "eviction keeps the running sum consistent",
			threshold: 0,
			window:    3,
			samples: []latencySample{
				{block: 1, latency: 10 * time.Millisecond},
				{block: 2, latency: 20 * time.Millisecond},
				{block: 3, latency: 30 * time.Millisecond},
				// Evict 10ms, add 40ms: window now {20, 30, 40}.
				{block: 4, latency: 40 * time.Millisecond},
				// Evict 20ms, add 10ms: window now {30, 40, 10}, total 80/3 → 26.
				{block: 5, latency: 10 * time.Millisecond},
			},
			want: NetworkLatencyMetrics{AverageMs: 26, WindowSize: 3, ThresholdMs: 0, LastBlock: 5},
		},
		{
			name:      "threshold zero is track only",
			threshold: 0,
			window:    2,
			samples: []latencySample{
				{block: 1, latency: time.Hour},
				{block: 2, latency: time.Hour},
				{block: 3, latency: time.Hour},
			},
			want: NetworkLatencyMetrics{
				AverageMs:   time.Hour.Milliseconds(),
				WindowSize:  2,
				ThresholdMs: 0,
				LastBlock:   3,
			},
		},
		{
			name:      "sticky breach freezes the window",
			threshold: 100 * time.Millisecond,
			window:    2,
			samples: []latencySample{
				{block: 1, latency: 150 * time.Millisecond},
				{block: 2, latency: 150 * time.Millisecond, wantBreach: true},
				// Post-breach records are no-ops: window and last block stay
				// frozen so post-mortem metrics reflect the triggering state.
				{block: 3, latency: time.Millisecond},
				{block: 4, latency: time.Millisecond},
			},
			want: NetworkLatencyMetrics{AverageMs: 150, WindowSize: 2, ThresholdMs: 100, LastBlock: 2},
		},
		{
			name:      "negative sample clamped to zero",
			threshold: 0,
			window:    2,
			samples: []latencySample{
				{block: 1, latency: -time.Second},
			},
			want: NetworkLatencyMetrics{AverageMs: 0, WindowSize: 2, ThresholdMs: 0, LastBlock: 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			monitor := NewNetworkLatencyMonitor(tc.threshold, tc.window)

			for _, sample := range tc.samples {
				err := monitor.Record(sample.block, sample.latency)

				if sample.wantBreach {
					var breach *LatencyBreachError
					require.ErrorAs(t, err, &breach)
					// The error must echo the verdict inputs and, because the
					// window froze at the breach, its average matches the
					// final metrics snapshot.
					assert.Equal(t, tc.threshold.Milliseconds(), breach.ThresholdMs)
					assert.Equal(t, tc.window, breach.WindowSize)
					assert.Equal(t, sample.block, breach.LastBlock)
					assert.Equal(t, tc.want.AverageMs, breach.AverageMs)
					assert.Contains(t, err.Error(), "exceeds")
				} else {
					require.NoError(t, err)
				}
			}

			assert.Equal(t, tc.want, monitor.Metrics())
		})
	}
}

func TestNetworkLatencyMonitor_MetricsSnapshot(t *testing.T) {
	tests := []struct {
		name      string
		threshold time.Duration
		window    int
		samples   []latencySample
		want      NetworkLatencyMetrics
	}{
		{
			name:      "fresh monitor reports zeros",
			threshold: 100 * time.Millisecond,
			window:    3,
			want:      NetworkLatencyMetrics{AverageMs: 0, WindowSize: 3, ThresholdMs: 100, LastBlock: 0},
		},
		{
			name:      "partial window averages held samples only",
			threshold: 100 * time.Millisecond,
			window:    3,
			samples: []latencySample{
				{block: 42, latency: 250 * time.Millisecond},
			},
			want: NetworkLatencyMetrics{AverageMs: 250, WindowSize: 3, ThresholdMs: 100, LastBlock: 42},
		},
		{
			name:      "constructor floors the window below one",
			threshold: 0,
			window:    0,
			samples: []latencySample{
				{block: 1, latency: 5 * time.Millisecond},
			},
			want: NetworkLatencyMetrics{AverageMs: 5, WindowSize: 1, ThresholdMs: 0, LastBlock: 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			monitor := NewNetworkLatencyMonitor(tc.threshold, tc.window)

			for _, sample := range tc.samples {
				require.NoError(t, monitor.Record(sample.block, sample.latency))
			}

			assert.Equal(t, tc.want, monitor.Metrics())
		})
	}
}

func TestNetworkLatencyMonitor_ConcurrentRecordAndMetrics(t *testing.T) {
	// Threshold far above the sampled latencies so concurrent writers never
	// race a breach; the point is data-race detection under -race.
	monitor := NewNetworkLatencyMonitor(10*time.Second, 8)

	const goroutines = 16
	const samplesPerGoroutine = 50

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			for i := range samplesPerGoroutine {
				_ = monitor.Record(int64(g*100+i), time.Millisecond)
				_ = monitor.Metrics()
			}
		})
	}
	wg.Wait()

	metrics := monitor.Metrics()
	assert.Equal(t, 8, metrics.WindowSize)
	assert.Equal(t, int64(1), metrics.AverageMs)
}
