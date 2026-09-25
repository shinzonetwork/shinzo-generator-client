package solana

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// rpcStats accumulates per-method RPC latency for one fetch, splitting each
// method's time into the network round-trip ("net") and the local response
// conversion ("local"). It rides the call context (withRPCStats) so every
// Client call in a fetch — the block fetch, the archive route, and the tip
// query that classifies missing slots — records into one summary without the
// fetcher threading a collector parameter through the call tree. A nil
// collector (no stats in the context) makes every record call a no-op, so
// the client never branches on whether tracking is wired up.
type rpcStats struct {
	mu      sync.Mutex
	order   []string // methods in first-recorded order
	methods map[string]*rpcMethodStats
}

// rpcMethodStats holds the aggregate latency of one RPC method. Failed
// calls bump calls/failed only — their latency is excluded from the
// aggregates so error-path noise never skews avg/min/max.
type rpcMethodStats struct {
	calls  int
	failed int
	net    time.Duration // successful round-trips, total
	local  time.Duration // successful conversions, total
	minNet time.Duration // 0 = unset (first success sets it)
	maxNet time.Duration
}

// newRPCStats returns an empty collector.
func newRPCStats() *rpcStats {
	return &rpcStats{methods: map[string]*rpcMethodStats{}}
}

// record adds one call observation for method. net is the round-trip
// duration, local the conversion duration (0 for failed calls). Safe on a
// nil receiver — calls made without stats in the context are ignored.
func (s *rpcStats) record(method string, net, local time.Duration, failed bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.methods[method]
	if !ok {
		m = &rpcMethodStats{}
		s.methods[method] = m
		s.order = append(s.order, method)
	}
	m.calls++
	if failed {
		m.failed++
		return
	}
	m.net += net
	m.local += local
	if m.minNet == 0 || net < m.minNet {
		m.minNet = net
	}
	if net > m.maxNet {
		m.maxNet = net
	}
}

// render returns the per-method summary for the rpc detail log line, e.g.
//
//	GetBlock 1x net 220ms local 9ms | GetSlot 1x net 41ms | rpc total net 261ms local 9ms
//
// Methods appear in first-recorded order; "" when nothing was recorded. The
// transport's internal rate-limit retries (retryTransport) are invisible
// here by design: one observed net duration covers however many HTTP
// attempts it took.
func (s *rpcStats) render() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var parts []string
	var totalNet, totalLocal time.Duration
	for _, name := range s.order {
		m := s.methods[name]
		totalNet += m.net
		totalLocal += m.local

		if m.calls-m.failed == 0 {
			parts = append(parts, fmt.Sprintf("%s %dx (%d failed)", name, m.calls, m.failed))
			continue
		}
		part := fmt.Sprintf("%s %dx net %s", name, m.calls, fmtRPCDur(m.net))
		if m.local > 0 {
			part += " local " + fmtRPCDur(m.local)
		}
		if m.calls > 1 {
			avg := m.net / time.Duration(m.calls-m.failed)
			part += fmt.Sprintf(" avg %s min %s max %s", fmtRPCDur(avg), fmtRPCDur(m.minNet), fmtRPCDur(m.maxNet))
		}
		if m.failed > 0 {
			part += fmt.Sprintf(" (%d failed)", m.failed)
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ""
	}
	parts = append(parts, fmt.Sprintf("rpc total net %s local %s", fmtRPCDur(totalNet), fmtRPCDur(totalLocal)))
	return strings.Join(parts, " | ")
}

// fmtRPCDur renders sub-second durations in milliseconds and the rest in
// seconds, keeping the log line compact and greppable.
func fmtRPCDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

// rpcStatsContextKey carries the per-fetch rpcStats through the RPC call
// stack. An unexported struct type prevents key collisions.
type rpcStatsContextKey struct{}

// withRPCStats returns a context carrying s so downstream Client calls
// record into it.
func withRPCStats(ctx context.Context, s *rpcStats) context.Context {
	return context.WithValue(ctx, rpcStatsContextKey{}, s)
}

// rpcStatsFrom returns the rpcStats carried by ctx, or nil when absent.
func rpcStatsFrom(ctx context.Context) *rpcStats {
	s, _ := ctx.Value(rpcStatsContextKey{}).(*rpcStats)
	return s
}
