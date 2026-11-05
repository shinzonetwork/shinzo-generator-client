//go:build acceptance
// +build acceptance

package replay

import (
	"testing"
	"time"
)

// ethereumSpec pins the Ethereum suite: the shipped config.yaml production
// runs, the ETHEREUM_* env namespace, the ethereum_blocks_*.json fixture
// glob, and Ethereum mainnet's block interval, which is a hard acceptance
// bar, not an informational number.
var ethereumSpec = replaySpec{
	chainLabel:         "Ethereum",
	configPath:         "../../config/config.yaml",
	expectedNetwork:    "Mainnet",
	envPrefix:          "ETHEREUM",
	fixtureGlob:        "ethereum_blocks_*.json",
	defaultTarget:      12 * time.Second,
	targetDurationHint: "10s or 12s",
	skipHint:           "make replay-fetch",
}

// TestEthereumReplayAcceptance replays the newest captured Ethereum fixture
// through the full production pipeline and hard-asserts the effective
// per-block interval stays within the chain's block interval.
// ETHEREUM_REPLAY_FIXTURE selects a fixture explicitly; without one, and
// absent an ETHEREUM_TARGET_BLOCK_TIME override, every other knob comes from
// the shipped config so the measurement is what production runs.
func TestEthereumReplayAcceptance(t *testing.T) {
	runEVMReplay(t, ethereumSpec)
}
