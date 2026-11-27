//go:build acceptance
// +build acceptance

package replay

import (
	"testing"
	"time"
)

// solanaSpec pins the Solana suite: the shipped config_solana.yaml production
// runs, the TEST_SOLANA_* env knob namespace, the solana_blocks_*.json
// fixture glob, and Solana's current slot interval, which is a hard
// acceptance bar, not an informational number. The slot-time reduction
// finished at 200 ms in October 2026 (stepping down through 350 ms, 300 ms,
// and 250 ms from the original 400 ms), so a fresh capture gates at 200 ms.
// That cadence is the right bar even under Alpenglow: its 150 ms figure is
// finality latency, how fast a produced block becomes irreversible, not how
// often blocks are produced. An older fixture gates harder today and
// TEST_SOLANA_TARGET_BLOCK_TIME moves the bar back to its era.
var solanaSpec = solanaReplaySpec{
	chainLabel:         "Solana",
	configPath:         "../../config/config_solana.yaml",
	expectedNetwork:    "Mainnet",
	envPrefix:          "TEST_SOLANA",
	fixtureGlob:        "solana_blocks_*.json",
	defaultTarget:      200 * time.Millisecond,
	targetDurationHint: "200ms",
	skipHint:           "make solana-replay-fetch",
}

// TestSolanaReplayAcceptance replays the newest captured Solana fixture
// through the full production pipeline and hard-asserts the effective
// per-produced-block interval stays within the chain's slot interval.
// TEST_SOLANA_REPLAY_FIXTURE selects a fixture explicitly; without one, and
// absent a TEST_SOLANA_TARGET_BLOCK_TIME override, every other knob comes
// from the shipped config so the measurement is what production runs.
func TestSolanaReplayAcceptance(t *testing.T) {
	runSolanaReplay(t, solanaSpec)
}
