//go:build acceptance
// +build acceptance

package replay

import (
	"testing"
	"time"
)

// avalancheSpec pins the Avalanche suite: the shipped config_avalanche.yaml, the
// AVALANCHE_* env namespace, the avalanche_blocks_*.json fixture glob, and the
// chain's measured block interval as the hard acceptance bar.
var avalancheSpec = replaySpec{
	chainLabel:         "Avalanche",
	configPath:         "../../config/config_avalanche.yaml",
	expectedNetwork:    "Mainnet",
	envPrefix:          "AVALANCHE",
	fixtureGlob:        "avalanche_blocks_*.json",
	defaultTarget:      1070 * time.Millisecond,
	targetDurationHint: "~1.07s",
	skipHint:           "make avalanche-replay-fetch",
}

// TestAvalancheReplayAcceptance replays the newest captured Avalanche fixture
// through the full production pipeline and hard-asserts the effective
// per-block interval stays within the chain's block interval.
func TestAvalancheReplayAcceptance(t *testing.T) {
	runEVMReplay(t, avalancheSpec)
}
