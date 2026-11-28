//go:build acceptance
// +build acceptance

package replay

import (
	"testing"
	"time"
)

// bitcoinSpec pins the Bitcoin suite: the shipped config_bitcoin.yaml
// production runs, the TEST_BITCOIN_* env knob namespace, the
// bitcoin_blocks_*.json fixture glob, and Bitcoin's 10-minute block interval,
// which is a hard acceptance bar, not an informational number.
var bitcoinSpec = bitcoinReplaySpec{
	chainLabel:         "Bitcoin",
	configPath:         "../../config/config_bitcoin.yaml",
	expectedNetwork:    "Mainnet",
	envPrefix:          "TEST_BITCOIN",
	fixtureGlob:        "bitcoin_blocks_*.json",
	defaultTarget:      10 * time.Minute,
	targetDurationHint: "10m",
	skipHint:           "mise run replay-fetch --chain bitcoin --adapter bitcoin",
}

// TestBitcoinReplayAcceptance replays the newest captured Bitcoin fixture
// through the full production pipeline and hard-asserts the effective
// per-block interval stays within Bitcoin's 10-minute block interval.
// TEST_BITCOIN_REPLAY_FIXTURE selects a fixture explicitly; without one, and
// absent a TEST_BITCOIN_TARGET_BLOCK_TIME override, every other knob comes
// from the shipped config so the measurement is what production runs.
func TestBitcoinReplayAcceptance(t *testing.T) {
	runBitcoinReplay(t, bitcoinSpec)
}
