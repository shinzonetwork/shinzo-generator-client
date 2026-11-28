//go:build acceptance
// +build acceptance

package replay

import (
	"os"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/replay"
	"github.com/stretchr/testify/require"
)

// TestBitcoinReplayConfigKeepsSuiteKnobs pins the env-naming contract the
// suite relies on: the scrub inside NewReplayConfig unsets the BITCOIN_
// connection vars the config loader reads, while the suite's TEST_BITCOIN_*
// knobs sit beyond that blanket prefix, so ResolveReplayServices and the
// fixture, cap, and target resolvers read what the developer set instead of
// seeing a silently emptied environment. NewReplayConfig restores placeholder
// BITCOIN_RPC_URL and basic-auth values so the bitcoin adapter's rpc_url and
// auth validation passes; the endpoint hook replaces them, so the developer's
// real values never reach the loader. Renaming a knob back into a scrubbed
// env family fails here loudly instead of dead-knobbing the pruner or
// snapshotter mid-run.
func TestBitcoinReplayConfigKeepsSuiteKnobs(t *testing.T) {
	knobs := []struct {
		name  string
		value string
	}{
		{bitcoinSpec.envPrefix + "_REPLAY_FIXTURE", "bitcoin_blocks_1000_1099.json"},
		{bitcoinSpec.envPrefix + "_REPLAY_MAX_BLOCKS", "7"},
		{bitcoinSpec.envPrefix + "_TARGET_BLOCK_TIME", "10m"},
		{bitcoinSpec.envPrefix + "_REPLAY_PRUNER", "off"},
		{bitcoinSpec.envPrefix + "_REPLAY_PRUNER_INTERVAL_SECONDS", "7"},
		{bitcoinSpec.envPrefix + "_REPLAY_PRUNER_MAX_BLOCKS", "42"},
		{bitcoinSpec.envPrefix + "_REPLAY_SNAPSHOT", "off"},
		{bitcoinSpec.envPrefix + "_REPLAY_SNAPSHOT_INTERVAL_SECONDS", "13"},
		{bitcoinSpec.envPrefix + "_REPLAY_SNAPSHOT_BLOCKS_PER_FILE", "29"},
	}
	for _, knob := range knobs {
		t.Setenv(knob.name, knob.value)
	}
	t.Setenv("BITCOIN_RPC_URL", "https://developer-node:8332")
	t.Setenv("BITCOIN_ARCHIVE_RPC_URL", "https://developer-archive:8332")

	cfg := replay.NewReplayConfig(t, bitcoinSpec.configPath, bitcoinSpec.chainLabel, bitcoinSpec.expectedNetwork, t.TempDir(), nil)

	for _, knob := range knobs {
		require.Equal(t, knob.value, os.Getenv(knob.name), "%s must survive the scrub", knob.name)
	}
	require.NotEqual(t, "https://developer-node:8332", os.Getenv("BITCOIN_RPC_URL"),
		"developer BITCOIN_RPC_URL must not reach the loader; NewReplayConfig restores a placeholder the bitcoin hook replaces")
	require.Empty(t, os.Getenv("BITCOIN_ARCHIVE_RPC_URL"),
		"developer BITCOIN_ARCHIVE_RPC_URL must be scrubbed; an archive endpoint would silently join the measurement")

	replay.ResolveReplayServices(t, cfg, bitcoinSpec.envPrefix)
	require.False(t, cfg.Pruner.Enabled, "%s=off must reach ResolveReplayServices", bitcoinSpec.envPrefix+"_REPLAY_PRUNER")
	require.False(t, cfg.Snapshot.Enabled, "%s=off must reach ResolveReplayServices", bitcoinSpec.envPrefix+"_REPLAY_SNAPSHOT")
}
