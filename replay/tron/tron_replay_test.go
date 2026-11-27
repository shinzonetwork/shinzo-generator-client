//go:build acceptance
// +build acceptance

package replay

import (
	"testing"
	"time"
)

// tronSpec pins the Tron suite: the shipped config_tron.yml production runs,
// the TEST_TRON_* env knob namespace, the tron_blocks_*.json fixture glob, and
// Tron's 3s block interval, which is a hard acceptance bar, not an
// informational number.
var tronSpec = tronReplaySpec{
	chainLabel:         "Tron",
	configPath:         "../../config/config_tron.yml",
	expectedNetwork:    "Mainnet",
	envPrefix:          "TEST_TRON",
	fixtureGlob:        "tron_blocks_*.json",
	defaultTarget:      3 * time.Second,
	targetDurationHint: "3s",
	skipHint:           "make tron-replay-fetch",
}

// TestTronReplayAcceptance replays the newest captured Tron fixture through
// the full production pipeline and hard-asserts the effective per-block
// interval stays within the chain's 3s block interval.
// TEST_TRON_REPLAY_FIXTURE selects a fixture explicitly; without one, and
// absent a TEST_TRON_TARGET_BLOCK_TIME override, every other knob comes from
// the shipped config so the measurement is what production runs.
func TestTronReplayAcceptance(t *testing.T) {
	runTronReplay(t, tronSpec)
}
