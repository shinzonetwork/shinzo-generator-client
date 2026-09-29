package logger

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPerfTimer_StageAccounting(t *testing.T) {
	t.Parallel()

	timer := NewPerfTimer()
	time.Sleep(5 * time.Millisecond) //nolint:mnd
	timer.Stage("fetch")
	time.Sleep(5 * time.Millisecond) //nolint:mnd
	timer.Stage("store")

	out := timer.Total()
	for _, want := range []string{"fetch ", "store ", "total "} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got %q", want, out)
		}
	}
	if !strings.Contains(out, ", ") {
		t.Errorf("expected stages joined with \", \", got %q", out)
	}
}

func TestPerfTimer_StagefFormatting(t *testing.T) {
	t.Parallel()

	timer := NewPerfTimer()
	timer.Stagef("logs (%d docs)", 42)

	out := timer.Total()
	if !strings.Contains(out, "logs (42 docs) ") {
		t.Errorf("expected formatted stage name in output, got %q", out)
	}
	if !strings.HasSuffix(out, "s") {
		t.Errorf("expected output to end with a duration, got %q", out)
	}
}

func TestPerfTimer_TotalIncludesAllStages(t *testing.T) {
	t.Parallel()

	timer := NewPerfTimer()
	timer.Stage("a")
	timer.Stage("b")
	timer.Stage("c")

	out := timer.Total()
	if got := strings.Count(out, ","); got != 3 { // separators between a, b, c, total
		t.Errorf("expected 4 parts (3 stages + total), got %d separators: %q", got, out)
	}
}

func TestPerfTimer_TotalCoversStages(t *testing.T) {
	t.Parallel()

	timer := NewPerfTimer()
	time.Sleep(10 * time.Millisecond) //nolint:mnd
	timer.Stage("only")

	out := timer.Total()
	total := parseStageSeconds(t, out, "total")
	stage := parseStageSeconds(t, out, "only")
	if total+0.001 < stage {
		t.Errorf("total %.3fs should be >= stage %.3fs", total, stage)
	}
}

func TestPerfTimer_ConcurrentTimersIndependent(t *testing.T) {
	t.Parallel()

	done := make(chan string, 2)
	run := func() {
		timer := NewPerfTimer()
		timer.Stage("x")
		time.Sleep(2 * time.Millisecond) //nolint:mnd
		timer.Stage("y")
		done <- timer.Total()
	}
	go run()
	go run()

	for range 2 {
		out := <-done
		if !strings.Contains(out, "x ") || !strings.Contains(out, "y ") {
			t.Errorf("expected both stages in %q", out)
		}
	}
}

// parseStageSeconds extracts the duration value from the "name X.XXs" part of
// a PerfTimer rendering.
func parseStageSeconds(t *testing.T, out, name string) float64 {
	t.Helper()
	for part := range strings.SplitSeq(out, ", ") {
		after, ok := strings.CutPrefix(part, name+" ")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSuffix(after, "s"), 64)
		if err != nil {
			t.Fatalf("failed to parse duration %q in part %q: %v", after, part, err)
		}
		return v
	}
	t.Fatalf("no part starting with %q in %q", name, out)
	return 0
}
