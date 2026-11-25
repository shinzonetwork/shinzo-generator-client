//go:build acceptance
// +build acceptance

package replay

import (
	"testing"
	"time"
)

// polygonSpec pins the Polygon suite: the shipped config_polygon.yaml, the
// POLYGON_* env namespace, the polygon_blocks_*.json fixture glob, and the
// chain's measured block interval as the hard acceptance bar.
var polygonSpec = replaySpec{
	chainLabel:         "Polygon",
	configPath:         "../../config/config_polygon.yaml",
	expectedNetwork:    "Mainnet",
	envPrefix:          "POLYGON",
	fixtureGlob:        "polygon_blocks_*.json",
	defaultTarget:      1500 * time.Millisecond,
	targetDurationHint: "1.5s",
	skipHint:           "make polygon-replay-fetch",
}

// TestPolygonReplayAcceptance replays the newest captured Polygon fixture
// through the full production pipeline and hard-asserts the effective
// per-block interval stays within the chain's block interval.
func TestPolygonReplayAcceptance(t *testing.T) {
	runEVMReplay(t, polygonSpec)
}
