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
// relies on: the scrub inside NewReplayConfig unsets the SOLANA_ connection
// vars the config loader reads, while the suite's TEST_SOLANA_* knobs sit
// beyond that blanket prefix, so ResolveReplayServices and the fixture,
// cap, and target resolvers read what the developer set instead of seeing
// a silently emptied environment. NewReplayConfig restores a placeholder
// SOLANA_RPC_URL so the solana adapter's non-empty rpc_url validation passes;
// the endpoint hook replaces it, so the developer's real value never
// reaches the loader. Renaming a knob back into a scrubbed env family
// fails here loudly instead of dead-knobbing the pruner or snapshotter
// mid-run.
func TestReplayConfigKeepsSuiteKnobs(t *testing.T) {
	knobs := []struct {
		name  string
		value string
	}{
		{solanaSpec.envPrefix + "_REPLAY_FIXTURE", "solana_blocks_1000_1499.json"},
		{solanaSpec.envPrefix + "_REPLAY_MAX_BLOCKS", "7"},
		{solanaSpec.envPrefix + "_TARGET_BLOCK_TIME", "400ms"},
		{solanaSpec.envPrefix + "_REPLAY_PRUNER", "off"},
		{solanaSpec.envPrefix + "_REPLAY_PRUNER_INTERVAL_SECONDS", "7"},
		{solanaSpec.envPrefix + "_REPLAY_PRUNER_MAX_BLOCKS", "42"},
		{solanaSpec.envPrefix + "_REPLAY_SNAPSHOT", "off"},
		{solanaSpec.envPrefix + "_REPLAY_SNAPSHOT_INTERVAL_SECONDS", "13"},
		{solanaSpec.envPrefix + "_REPLAY_SNAPSHOT_BLOCKS_PER_FILE", "29"},
	}
	for _, knob := range knobs {
		t.Setenv(knob.name, knob.value)
	}
	t.Setenv("SOLANA_RPC_URL", "https://api.devnet.solana.com")

	cfg := replay.NewReplayConfig(t, solanaSpec.configPath, solanaSpec.chainLabel, solanaSpec.expectedNetwork, t.TempDir(), nil)

	for _, knob := range knobs {
		require.Equal(t, knob.value, os.Getenv(knob.name), "%s must survive the scrub", knob.name)
	}
	require.NotEqual(t, "https://api.devnet.solana.com", os.Getenv("SOLANA_RPC_URL"),
		"developer SOLANA_RPC_URL must not reach the loader; NewReplayConfig restores a placeholder the solana hook replaces")

	replay.ResolveReplayServices(t, cfg, solanaSpec.envPrefix)
	require.False(t, cfg.Pruner.Enabled, "%s=off must reach ResolveReplayServices", solanaSpec.envPrefix+"_REPLAY_PRUNER")
	require.False(t, cfg.Snapshot.Enabled, "%s=off must reach ResolveReplayServices", solanaSpec.envPrefix+"_REPLAY_SNAPSHOT")
}
