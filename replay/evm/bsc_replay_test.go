//go:build acceptance
// +build acceptance

package replay

import (
	"testing"
	"time"
)

// bscSpec pins the BSC suite: the shipped config_bsc.yaml production runs,
// the BSC_* env namespace, the bsc_blocks_*.json fixture glob, and BSC
// mainnet's block interval since the Fermi hard fork (January 2026), which is
// a hard acceptance bar, not an informational number.
var bscSpec = replaySpec{
	chainLabel:         "BSC",
	configPath:         "../../config/config_bsc.yaml",
	expectedNetwork:    "Mainnet",
	envPrefix:          "BSC",
	fixtureGlob:        "bsc_blocks_*.json",
	defaultTarget:      450 * time.Millisecond,
	targetDurationHint: "450ms or 750ms",
	skipHint:           "make bsc-replay-fetch",
}

// TestBSCReplayAcceptance replays the newest captured BSC fixture through
// the full production pipeline and hard-asserts the effective per-block
// interval stays within the chain's block interval.
// BSC_REPLAY_FIXTURE selects a fixture explicitly; without one, and absent
// a BSC_TARGET_BLOCK_TIME override, every other knob comes from the shipped
// config so the measurement is what production runs.
func TestBSCReplayAcceptance(t *testing.T) {
	runEVMReplay(t, bscSpec)
}
