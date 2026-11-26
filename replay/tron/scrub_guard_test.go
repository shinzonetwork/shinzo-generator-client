//go:build acceptance
// +build acceptance

package replay

import (
	"os"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/replay"
	"github.com/stretchr/testify/require"
)

// TestReplayConfigKeepsSuiteKnobs pins the env-naming contract the suite
// relies on: the scrub inside NewReplayConfig unsets the TRON_ connection
// vars the config loader reads, while the suite's TEST_TRON_* knobs sit
// beyond that blanket prefix, so ResolveReplayServices and the fixture,
// cap, and target resolvers read what the developer set instead of seeing
// a silently emptied environment. NewReplayConfig restores a placeholder
// TRON_RPC_URL so the tron adapter's non-empty node_url validation passes;
// the endpoint hook replaces it, so the developer's real value never
// reaches the loader. Renaming a knob back into a scrubbed env family
// fails here loudly instead of dead-knobbing the pruner or snapshotter
// mid-run.
func TestReplayConfigKeepsSuiteKnobs(t *testing.T) {
	knobs := []struct {
		name  string
		value string
	}{
		{tronSpec.envPrefix + "_REPLAY_FIXTURE", "tron_blocks_1000_1499.json"},
		{tronSpec.envPrefix + "_REPLAY_MAX_BLOCKS", "7"},
		{tronSpec.envPrefix + "_TARGET_BLOCK_TIME", "2s"},
		{tronSpec.envPrefix + "_REPLAY_PRUNER", "off"},
		{tronSpec.envPrefix + "_REPLAY_PRUNER_INTERVAL_SECONDS", "7"},
		{tronSpec.envPrefix + "_REPLAY_PRUNER_MAX_BLOCKS", "42"},
		{tronSpec.envPrefix + "_REPLAY_SNAPSHOT", "off"},
		{tronSpec.envPrefix + "_REPLAY_SNAPSHOT_INTERVAL_SECONDS", "13"},
		{tronSpec.envPrefix + "_REPLAY_SNAPSHOT_BLOCKS_PER_FILE", "29"},
	}
	for _, knob := range knobs {
		t.Setenv(knob.name, knob.value)
	}
	t.Setenv("TRON_RPC_URL", "https://api.trongrid.io")

	cfg := replay.NewReplayConfig(t, tronSpec.configPath, tronSpec.chainLabel, tronSpec.expectedNetwork, t.TempDir(), nil)

	for _, knob := range knobs {
		require.Equal(t, knob.value, os.Getenv(knob.name), "%s must survive the scrub", knob.name)
	}
	require.NotEqual(t, "https://api.trongrid.io", os.Getenv("TRON_RPC_URL"),
		"developer TRON_RPC_URL must not reach the loader; NewReplayConfig restores a placeholder the tron hook replaces")

	replay.ResolveReplayServices(t, cfg, tronSpec.envPrefix)
	require.False(t, cfg.Pruner.Enabled, "%s=off must reach ResolveReplayServices", tronSpec.envPrefix+"_REPLAY_PRUNER")
	require.False(t, cfg.Snapshot.Enabled, "%s=off must reach ResolveReplayServices", tronSpec.envPrefix+"_REPLAY_SNAPSHOT")
}
