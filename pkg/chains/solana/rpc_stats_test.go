package solana

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
	s.record("GetBlock", time.Millisecond, time.Millisecond, false)
	s.record("GetBlockFromArchive", time.Millisecond, 0, true)
	assert.Equal(t, "", s.render())
}

func TestRPCStats_RenderEmpty(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "", newRPCStats().render())
}

func TestRPCStats_RenderSingleCall(t *testing.T) {
	t.Parallel()

	s := newRPCStats()
	s.record("GetBlock", 420*time.Millisecond, 52*time.Millisecond, false)

	out := s.render()
	assert.Contains(t, out, "GetBlock 1x net 420ms local 52ms")
	assert.NotContains(t, out, "avg")
	assert.Contains(t, out, "rpc total net 420ms local 52ms")
}

func TestRPCStats_RenderOmitsZeroLocal(t *testing.T) {
	t.Parallel()

	s := newRPCStats()
	s.record("GetSlot", 41*time.Millisecond, 0, false)

	out := s.render()
	assert.Contains(t, out, "GetSlot 1x net 41ms")
	// The method segment omits local; the total always carries it.
	assert.NotContains(t, out, "GetSlot 1x net 41ms local")
}

func TestRPCStats_RenderMultiCallWithMinMax(t *testing.T) {
	t.Parallel()

	s := newRPCStats()
	s.record("GetBlock", 10*time.Millisecond, time.Millisecond, false)
	s.record("GetBlock", 2*time.Millisecond, time.Millisecond, false)
	s.record("GetBlock", 41*time.Millisecond, time.Millisecond, false)

	out := s.render()
	assert.Contains(t, out, "GetBlock 3x net 53ms local 3ms")
	assert.Contains(t, out, "avg 17ms min 2ms max 41ms")
}

func TestRPCStats_RenderFailedCalls(t *testing.T) {
	t.Parallel()

	s := newRPCStats()
	s.record("GetBlock", 420*time.Millisecond, 52*time.Millisecond, false)
	s.record("GetBlockFromArchive", 31*time.Millisecond, 0, true)
	s.record("GetSlot", 6*time.Millisecond, 0, false)
	s.record("GetSlot", 8*time.Millisecond, 0, true)

	out := s.render()
	assert.Contains(t, out, "GetBlockFromArchive 1x (1 failed)")
	assert.Contains(t, out, "GetSlot 2x net 6ms avg 6ms min 6ms max 6ms (1 failed)")
	assert.Contains(t, out, "rpc total net 426ms local 52ms")
}

func TestRPCStats_RenderAllCallsFailed(t *testing.T) {
	t.Parallel()

	s := newRPCStats()
	s.record("GetSlot", time.Millisecond, 0, true)
	s.record("GetSlot", time.Millisecond, 0, true)

	out := s.render()
	assert.Contains(t, out, "GetSlot 2x (2 failed)")
	// Failed calls only: no latency aggregates for the method itself.
	assert.NotContains(t, out, "GetSlot 2x net")
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
				s.record("GetBlock", time.Millisecond, time.Millisecond, false)
			}
		})
	}
	wg.Wait()

	m, ok := s.methods["GetBlock"]
	if !ok {
		t.Fatal("expected GetBlock stats")
	}
	assert.Equal(t, workers*calls, m.calls)
	assert.Equal(t, 0, m.failed)
	assert.Equal(t, time.Duration(workers*calls)*time.Millisecond, m.net)
}

func TestRPCStats_FirstRecordedOrder(t *testing.T) {
	t.Parallel()

	s := newRPCStats()
	s.record("GetBlock", time.Millisecond, 0, false)
	s.record("GetBlockFromArchive", time.Millisecond, 0, false)
	s.record("GetSlot", time.Millisecond, 0, false)

	out := s.render()
	first, _, _ := strings.Cut(out, " | ")
	assert.Contains(t, first, "GetBlock")
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
