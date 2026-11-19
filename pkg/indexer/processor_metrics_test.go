package indexer

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestProcessBlocks_BlocksMetric checks that every height leaving the ordered
// commit queue is counted once: committed stores as success, and failed
// converts and failed stores as error.
func TestProcessBlocks_BlocksMetric(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	const tip = 106
	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			if height > tip {
				return nil, fmt.Errorf("block not found")
			}
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
	}

	// Height 103 fails to convert once.
	converter := chainConverter()
	convert := converter.ConvertFn
	var convert103 atomic.Int64
	converter.ConvertFn = func(ctx context.Context, raw any) (chains.ConversionResult, error) {
		if b, ok := raw.(*servedBlock); ok && b.height == 103 && convert103.Add(1) == 1 {
			return chains.ConversionResult{}, fmt.Errorf("convert exploded")
		}
		return convert(ctx, raw)
	}

	// Height 101 fails to store once.
	reorg := &mockReorgHandler{storedHashes: map[int64]string{99: canonicalHash(99)}}
	var store101 atomic.Int64
	storer := &mockBlockStorer{
		storeFn: func(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
			num := blockNumOfResult(result)
			if num == 101 && store101.Add(1) == 1 {
				return nil, fmt.Errorf("store exploded")
			}
			reorg.mu.Lock()
			reorg.storedHashes[num] = extractBlockHash(result.Groups)
			reorg.mu.Unlock()
			return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
		},
	}

	p := NewConcurrentBlockProcessor(fetcher, converter, storer, reorg, 1, 0)
	reader := sdkmetric.NewManualReader()
	blocks, err := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test").Int64Counter("blocks")
	require.NoError(t, err)
	p.metrics.blocks = blocks

	rec := &commitRecorder{}
	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	waitForCommits(t, rec, 5, errCh)
	assert.Equal(t, []int64{100, 102, 104, 105, 106}, rec.snapshot())

	counts := func() map[string]int64 {
		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(t.Context(), &rm))
		got := map[string]int64{}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				for _, dp := range sum.DataPoints {
					result, _ := dp.Attributes.Value(attribute.Key("result"))
					got[result.AsString()] = dp.Value
				}
			}
		}
		return got
	}
	// The failures at 101 and 103 are counted before the later heights commit.
	assert.Equal(t, map[string]int64{"success": 5, "error": 2}, counts())

	cancel()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for ProcessBlocks to stop")
	}
}
