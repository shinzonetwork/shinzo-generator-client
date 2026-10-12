package evm

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRPCStats_NilReceiverSafe(t *testing.T) {
	t.Parallel()

	var s *rpcStats
	s.record("GetBlockByNumber", time.Millisecond, time.Millisecond, false)
	s.record("GetBlockReceipts", time.Millisecond, 0, true)
	assert.Equal(t, "", s.render())
}

func TestRPCStats_RenderEmpty(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "", newRPCStats().render())
}

func TestRPCStats_RenderSingleCall(t *testing.T) {
	t.Parallel()

	s := newRPCStats()
	s.record("GetBlockByNumber", 420*time.Millisecond, 52*time.Millisecond, false)

	out := s.render()
	assert.Contains(t, out, "GetBlockByNumber 1x net 420ms local 52ms")
	assert.NotContains(t, out, "avg")
	assert.Contains(t, out, "rpc total net 420ms local 52ms")
}

func TestRPCStats_RenderMultiCallWithMinMax(t *testing.T) {
	t.Parallel()

	s := newRPCStats()
	s.record("GetTransactionReceipt", 10*time.Millisecond, time.Millisecond, false)
	s.record("GetTransactionReceipt", 2*time.Millisecond, time.Millisecond, false)
	s.record("GetTransactionReceipt", 41*time.Millisecond, time.Millisecond, false)

	out := s.render()
	assert.Contains(t, out, "GetTransactionReceipt 3x net 53ms local 3ms")
	assert.Contains(t, out, "avg 17ms min 2ms max 41ms")
}

func TestRPCStats_RenderFailedCalls(t *testing.T) {
	t.Parallel()

	s := newRPCStats()
	s.record("GetBlockByNumber", 420*time.Millisecond, 52*time.Millisecond, false)
	s.record("GetBlockReceipts", 31*time.Millisecond, 0, true)
	s.record("GetTransactionReceipt", 6*time.Millisecond, 0, false)
	s.record("GetTransactionReceipt", 8*time.Millisecond, 0, true)

	out := s.render()
	assert.Contains(t, out, "GetBlockReceipts 1x (1 failed)")
	assert.Contains(t, out, "GetTransactionReceipt 2x net 6ms avg 6ms min 6ms max 6ms (1 failed)")
	assert.Contains(t, out, "rpc total net 426ms local 52ms")
}

func TestRPCStats_RenderAllCallsFailed(t *testing.T) {
	t.Parallel()

	s := newRPCStats()
	s.record("GetNetworkID", time.Millisecond, 0, true)
	s.record("GetNetworkID", time.Millisecond, 0, true)

	out := s.render()
	assert.Contains(t, out, "GetNetworkID 2x (2 failed)")
	// Failed calls only: no latency aggregates for the method itself.
	assert.NotContains(t, out, "GetNetworkID 2x net")
	assert.Contains(t, out, "rpc total net 0ms local 0ms")
}

func TestRPCStats_ConcurrentRecord(t *testing.T) {
	t.Parallel()

	s := newRPCStats()
	const workers, calls = 4, 50 //nolint:mnd
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range calls {
				s.record("GetTransactionReceipt", time.Millisecond, time.Millisecond, false)
			}
		})
	}
	wg.Wait()

	m, ok := s.methods["GetTransactionReceipt"]
	if !ok {
		t.Fatal("expected GetTransactionReceipt stats")
	}
	assert.Equal(t, workers*calls, m.calls)
	assert.Equal(t, 0, m.failed)
	assert.Equal(t, time.Duration(workers*calls)*time.Millisecond, m.net)
}

func TestRPCStats_FirstRecordedOrder(t *testing.T) {
	t.Parallel()

	s := newRPCStats()
	s.record("GetBlockByNumber", time.Millisecond, 0, false)
	s.record("GetTransactionReceipt", time.Millisecond, 0, false)
	s.record("GetLatestBlockNumber", time.Millisecond, 0, false)

	out := s.render()
	first := out[:strings.Index(out, " | ")]
	assert.Contains(t, first, "GetBlockByNumber")
}

func TestWithRPCStats_RoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	assert.Nil(t, rpcStatsFrom(ctx)) // absent → nil

	s := newRPCStats()
	found := rpcStatsFrom(withRPCStats(ctx, s))
	assert.Equal(t, s, found)
}

func TestFmtRPCDur(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "0ms", fmtRPCDur(0))
	assert.Equal(t, "7ms", fmtRPCDur(7*time.Millisecond))
	assert.Equal(t, "999ms", fmtRPCDur(999*time.Millisecond))
	assert.Equal(t, "1.00s", fmtRPCDur(time.Second))
	assert.Equal(t, "1.10s", fmtRPCDur(1100*time.Millisecond))
}
