//go:build acceptance
// +build acceptance

package replay

import (
	"testing"
	"time"
)

// celoSpec pins the Celo suite: the shipped config_celo.yaml, the
// CELO_* env namespace, the celo_blocks_*.json fixture glob, and the
// chain's measured block interval as the hard acceptance bar.
var celoSpec = replaySpec{
	chainLabel:         "Celo",
	configPath:         "../../config/config_celo.yaml",
	expectedNetwork:    "Mainnet",
	envPrefix:          "CELO",
	fixtureGlob:        "celo_blocks_*.json",
	defaultTarget:      1 * time.Second,
	targetDurationHint: "1s",
	skipHint:           "make celo-replay-fetch",
}

// TestCeloReplayAcceptance replays the newest captured Celo fixture
// through the full production pipeline and hard-asserts the effective
// per-block interval stays within the chain's block interval.
func TestCeloReplayAcceptance(t *testing.T) {
	runEVMReplay(t, celoSpec)
}
