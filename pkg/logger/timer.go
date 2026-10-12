package logger

import (
	"fmt"
	"strings"
	"time"
)

// PerfTimer records per-stage durations for one logical operation (e.g.
// processing one block) and renders them in the canonical perf-log format
// "stage1 X.XXs, stage2 X.XXs, ..., total X.XXs". It is not safe for
// concurrent use; each goroutine creates its own.
type PerfTimer struct {
	begin time.Time // operation start, for the total
	last  time.Time // boundary of the current (unfinished) stage
	parts []string
}

// NewPerfTimer starts a timer whose first stage begins immediately.
func NewPerfTimer() *PerfTimer {
	now := time.Now()
	return &PerfTimer{begin: now, last: now}
}

// Stage closes the current stage, recording its elapsed time under name,
// and opens the next one.
func (t *PerfTimer) Stage(name string) {
	t.Stagef("%s", name)
}

// Stagef is Stage with a formatted stage name (e.g. "logs (%d docs)", n).
func (t *PerfTimer) Stagef(format string, args ...any) {
	name := fmt.Sprintf(format, args...)
	d := time.Since(t.last).Seconds()
	t.last = time.Now()
	t.parts = append(t.parts, fmt.Sprintf("%s %.2fs", name, d))
}

// Total renders "stage X.XXs, ..., total X.XXs" with the total measured
// from NewPerfTimer. Call once, at the end of the operation.
func (t *PerfTimer) Total() string {
	t.parts = append(t.parts, fmt.Sprintf("total %.2fs", time.Since(t.begin).Seconds()))
	return strings.Join(t.parts, ", ")
}
