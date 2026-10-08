package indexer

// indexer_latency_test.go is the end-to-end breach integration test for the
// maximum-allowable-network-latency feature: a chain whose block fetches are
// slower than the configured threshold must fill the rolling window, trip the
// tripwire on the first full window, cancel the indexing loop, tear down every
// owned subsystem, and surface the typed system error from StartIndexing.
//
// Mutation detection:
//   - Commit hook calling StopIndexing instead of cancelling (the deadlock
//     trap): StopIndexing waits on indexingDone, which only closes when
//     runConcurrentIndexing exits, so StartIndexing never settles → the
//     bounded select below fails instead of hanging the suite.
//   - Cause mapping losing the errNetworkLatencyExceeded branch: the start
//     returns a context artifact (or nil) → the typed-error assertions fail.
//   - Monitor never tripping (threshold ignored, window never filling): the
//     indexing loop runs forever against the endless mock chain → the same
//     bounded select fails.
//   - Teardown skipped on the breach path: the nil assertions on the
//     fetcher/defra node fail.
//
// Goroutine-leak safety is the -race run plus the same bounded select: the
// breach path exits the processor drain before returning, so StartIndexing
// settling within the bound proves the drain completed (bounded by
// IndexingStopTimeout).

import (
	"encoding/json"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Latency-breach integration constants: every block fetch sleeps longer than
// the threshold, so the rolling average strictly exceeds it exactly when the
// window fills (the monitor only judges full windows).
const (
	latencyThresholdMs = 100
	latencyWindow      = 3
	latencyFetchSleep  = 150 * time.Millisecond
)

func TestStartIndexing_NetworkLatencyBreach(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()

	var blockFetchCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if innerErr := json.Unmarshal(rawParams[0], &blockParam); innerErr == nil && blockParam == defaultBlockParamLatest {
					return fullBlockResponse("0x186b1", nil), nil
				}
			}
			// Every real block fetch is slower than the threshold: one
			// sample each, strictly above the tripwire.
			time.Sleep(latencyFetchSleep)
			n := blockFetchCount.Add(1)
			return fullBlockResponse(fmt.Sprintf("0x%x", 100000+n), nil), nil
		case ethBlockNumber:
			return "0x186b1", nil
		case ethGetBlockReceipts:
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	t.Cleanup(rpcServer.Close)

	// Health server enabled so the breach teardown's "stop the health server"
	// step has something real to stop.
	healthPort := freeHealthPort(t)
	cfg := startPathBaseCfg(rpcServer.URL, testDefraRandomURL, tmpDir, config.IndexerConfig{
		StartHeight:         1000,
		ConcurrentBlocks:    1, // in-order commits: the 3rd commit fills the window
		ReceiptWorkers:      2,
		MaxDocsPerTxn:       100,
		HealthServerPort:    healthPort,
		StartBuffer:         10,
		MaxNetworkLatencyMs: latencyThresholdMs,
		LatencyWindowBlocks: latencyWindow,
	})

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	stopped := false
	t.Cleanup(func() {
		if !stopped {
			indexer.shouldIndex = false
			indexer.StopIndexing()
		}
	})

	errCh := startIndexingBackground(t, indexer)

	// The breach is the termination: after three committed blocks the rolling
	// average trips the threshold, the loop is cancelled, and the start
	// returns the typed system error. The bound is generous so the select can
	// only fail when the breach machinery is genuinely broken.
	select {
	case startErr := <-errCh:
		require.Error(t, startErr, "a breached start must surface the typed system error")

		var ierr errors.IndexerError
		require.ErrorAs(t, startErr, &ierr)
		assert.Equal(t, errors.CodeServiceUnavailable, ierr.Code())
		assert.Equal(t, errors.Critical, ierr.Severity())
		assert.Contains(t, startErr.Error(), "network latency exceeded")
		assert.Contains(t, startErr.Error(), "catch up with the network tip")

		// The typed error carries the frozen tripwire values as structured
		// context (block number + metadata) for operators and post-mortems.
		errCtx := ierr.Context()
		require.NotNil(t, errCtx.BlockNumber)
		assert.Positive(t, *errCtx.BlockNumber)
		require.NotNil(t, errCtx.Metadata)
		assert.Equal(t, int64(latencyThresholdMs), errCtx.Metadata["threshold_ms"])
		assert.Equal(t, latencyWindow, errCtx.Metadata["window_size"])
		avgMs, ok := errCtx.Metadata["average_ms"].(int64)
		require.True(t, ok, "average_ms metadata should be an int64")
		assert.Greater(t, avgMs, int64(latencyThresholdMs))
	case <-time.After(60 * time.Second):
		t.Fatal("StartIndexing never returned after the latency breach (deadlock trap or breach never tripped?)")
	}

	// The breach branch owns the teardown: lifecycle flags reset and every
	// assigned subsystem closed and nilled.
	assert.False(t, indexer.isStarted, "indexer should be stopped after a breach")
	assert.False(t, indexer.shouldIndex, "indexer should not be indexing after a breach")
	assert.Nil(t, indexer.fetcher, "fetcher should be closed and nilled after a breach")
	assert.Nil(t, indexer.defraNode, "defra node should be closed and nilled after a breach")

	// The monitor is deliberately not nilled on teardown: its frozen window
	// stays readable for post-mortem inspection through /metrics.
	metrics := indexer.GetLatencyMetrics()
	require.NotNil(t, metrics, "frozen latency metrics should outlive the teardown")
	assert.Equal(t, int64(latencyThresholdMs), metrics.ThresholdMs)
	assert.Equal(t, latencyWindow, metrics.WindowSize)
	assert.Greater(t, metrics.AverageMs, int64(latencyThresholdMs))

	// The health server started above must have been stopped by the breach
	// teardown: nothing listens on the port anymore. The bound is generous;
	// Stop runs before StartIndexing returns.
	assert.Eventually(t, func() bool {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", healthPort), 250*time.Millisecond)
		if dialErr != nil {
			return true
		}
		_ = conn.Close()
		return false
	}, 10*time.Second, 100*time.Millisecond, "health server should stop accepting connections after a breach")

	stopped = true
}
