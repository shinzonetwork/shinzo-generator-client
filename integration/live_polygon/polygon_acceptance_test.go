//go:build live

// Package live_polygon holds the Polygon acceptance test: the generator must
// stay at the tip of Polygon mainnet (block time ~1.5-1.75s) over a sustained
// period against a real RPC endpoint.
//
// Required env: POLYGON_RPC_URL (test skips without it).
// Optional env: POLYGON_WS_URL, POLYGON_API_KEY, POLYGON_ACCEPTANCE_DURATION
// (default 15m), POLYGON_ACCEPTANCE_WARMUP (default 2m),
// POLYGON_ACCEPTANCE_MAX_LAG in blocks (default 150).
package live_polygon

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envInt(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

func TestPolygonGeneratorStaysAtTip(t *testing.T) {
	rpcURL := os.Getenv("POLYGON_RPC_URL")
	if rpcURL == "" {
		t.Skip("POLYGON_RPC_URL not set")
	}
	logger.InitConsoleOnly(true)

	// Self-contained defaults for the embedded DefraDB the indexer starts.
	if os.Getenv("SCHEMA_AUTH_MODE") == "" {
		t.Setenv("SCHEMA_AUTH_MODE", "none")
	}
	if os.Getenv("DEFRADB_KEYRING_SECRET") == "" {
		t.Setenv("DEFRADB_KEYRING_SECRET", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	}

	cfg, err := config.LoadConfig("../../config/config.yaml")
	require.NoError(t, err)
	cfg.Chain.Name = "Polygon"
	cfg.Chain.Network = "Mainnet"
	cfg.DefraDB.Store.Path = t.TempDir()
	cfg.Geth.NodeURL = rpcURL
	cfg.Geth.WsURL = os.Getenv("POLYGON_WS_URL")
	cfg.Geth.APIKey = os.Getenv("POLYGON_API_KEY")
	// The Ethereum-tuned default of 8 falls ~15 blocks/min behind on Polygon;
	// DefraDB store latency (~2s/block) is the bottleneck. 16 keeps up.
	cfg.Indexer.ConcurrentBlocks = 16

	// Independent tip reader.
	tipClient, err := evm.NewEthereumClient(rpcURL, cfg.Geth.WsURL, cfg.Geth.APIKey, cfg.Geth.APIKeyType)
	require.NoError(t, err)
	defer tipClient.Close()

	chainIndexer, err := indexer.CreateIndexer(cfg)
	require.NoError(t, err)

	go func() {
		if err := chainIndexer.StartIndexing(false); err != nil {
			logger.Sugar.Errorf("indexer failed: %v", err)
		}
	}()
	defer chainIndexer.StopIndexing()

	require.Eventually(t, chainIndexer.HasIndexedAtLeastOneBlock, 90*time.Second, 2*time.Second,
		"no block indexed within 90s")

	var (
		duration = envDuration("POLYGON_ACCEPTANCE_DURATION", 15*time.Minute)
		warmup   = envDuration("POLYGON_ACCEPTANCE_WARMUP", 2*time.Minute)
		maxLag   = envInt("POLYGON_ACCEPTANCE_MAX_LAG", 150)
		started  = time.Now()
		deadline = started.Add(duration)

		maxPostWarmupLag int64
		lastStored       = chainIndexer.GetCurrentBlock()
		lastProgress     = time.Now()
		samples          int
	)

	for time.Now().Before(deadline) {
		tip, err := tipClient.GetLatestBlockNumber(context.Background())
		require.NoError(t, err)
		stored := chainIndexer.GetCurrentBlock()
		lag := tip.Int64() - stored

		if stored > lastStored {
			lastStored = stored
			lastProgress = time.Now()
		}

		if time.Since(started) > warmup {
			if lag > maxPostWarmupLag {
				maxPostWarmupLag = lag
			}
			samples++
			require.LessOrEqualf(t, lag, maxLag, "tip lag %d exceeds bound %d", lag, maxLag)
		}
		require.Less(t, time.Since(lastProgress), 30*time.Second, "stored block stalled")
		time.Sleep(5 * time.Second)
	}

	t.Logf("acceptance: %d post-warmup samples, max lag %d blocks (bound %d)", samples, maxPostWarmupLag, maxLag)
	require.Positive(t, samples, "no post-warmup samples taken")
}
