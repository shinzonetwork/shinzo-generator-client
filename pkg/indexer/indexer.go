package indexer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defradb"
	indexerErrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/pruner"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/server"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/signer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/snapshot"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/node"
)

var (
	// ErrMTLSNotImplemented is returned when the mTLS authentication mode is configured but not yet supported.
	ErrMTLSNotImplemented = errors.New("mTLS auth mode is not yet implemented")
	// ErrUnknownAuthMode is returned when an unrecognized schema authentication mode is provided.
	ErrUnknownAuthMode = errors.New("unknown auth mode")

	// errIndexingStopped is the cancel cause used when StopIndexing initiates a
	// clean shutdown of the indexing loop; runConcurrentIndexing maps it to a
	// nil error so a clean stop does not surface as a failure.
	errIndexingStopped = errors.New("indexing stopped")
)

// Version is the generator version, set at build time via -ldflags (see the
// Makefile build target); falls back to "dev" when built without injection.
var Version = "dev" //nolint:gochecknoglobals // set at build time via -ldflags

const (
	// ShortDelayTime is a short delay duration used in various places to give time for operations to complete.
	ShortDelayTime = 500 * time.Millisecond // Give the server time to start.
	// DefaultBlocksToIndexAtOnce is the default number of blocks to index concurrently.
	DefaultBlocksToIndexAtOnce = 10
	// DefaultRetryAttempts is the default number of retry attempts for failed operations.
	DefaultRetryAttempts = 3
	// DefaultRetryDelay is the default delay between retry attempts.
	DefaultRetryDelay = 10 * time.Second
	// DefaultSchemaWaitTimeout is the timeout for waiting for the schema to be applied.
	DefaultSchemaWaitTimeout = 15 * time.Second
	// DefaultDefraReadyTimeout is the timeout for waiting for DefraDB to become ready.
	DefaultDefraReadyTimeout = 30 * time.Second
	// DefaultBlockOffset is the number of blocks behind the latest block to process.
	// This prevents "transaction type not supported" errors from very recent blocks.
	DefaultBlockOffset = 3
	// DefaultWorkersAhead is the number of blocks ahead of the last committed block that the processor will allow itself to get before throttling dispatch.
	DefaultWorkersAhead = 2
	// IndexingStopTimeout bounds how long StopIndexing waits for the block
	// processor to drain before closing resources anyway.
	IndexingStopTimeout = 30 * time.Second
	// IndexingStartStopTimeout bounds how long StopIndexing waits for an
	// in-flight StartIndexing to settle before tearing down anyway.
	IndexingStartStopTimeout = 30 * time.Second
)

// var requiredPeers = []string{} // Here, we can consider adding any "big peers" we need - these requiredPeers can be used as a quick start point to speed up the peer discovery process.

// defaultListenAddress is the default P2P listen address for the embedded DefraDB node.
const defaultListenAddress string = "/ip4/127.0.0.1/tcp/9171"

// ChainIndexer is the main indexer that processes blockchain blocks.
type ChainIndexer struct {
	cfg                       *config.Config
	fetcher                   chains.Fetcher
	converter                 chains.Converter
	blockHandler              *defra.BlockHandler
	shouldIndex               bool
	isStarted                 bool
	hasIndexedAtLeastOneBlock bool
	defraNode                 *node.Node              // Embedded DefraDB node (nil if using external)
	networkHandler            *defradb.NetworkHandler // P2P network handler (nil if using external)
	healthServer              *server.HealthServer
	pruner                    *pruner.Pruner        // Document pruner for removing old blocks.
	snapshotter               *snapshot.Snapshotter // Snapshot exporter for archiving blocks.
	currentBlock              int64
	lastProcessedTime         time.Time
	indexingCancel            context.CancelCauseFunc // Cancel for the indexing loop; nil unless concurrent indexing is running.
	indexingDone              chan struct{}           // Closed when the indexing loop has fully exited; guarded by mutex.
	startInProgress           bool                    // True while StartIndexing is in its init phase (pre-handoff); guarded by mutex.
	startDone                 chan struct{}           // Closed when StartIndexing settles (returns, or hands off to the drainable indexing loop); guarded by mutex.
	initCancel                context.CancelCauseFunc // Cancels the init context of an in-flight StartIndexing; guarded by mutex, nil when no init is running.
	stopMu                    sync.Mutex              // Serializes StopIndexing bodies: the error guard's Stop and an external Stop must never teardown concurrently.
	mutex                     sync.RWMutex
}

// IsStarted returns true if the indexer has been started.
func (i *ChainIndexer) IsStarted() bool {
	return i.isStarted
}

// HasIndexedAtLeastOneBlock returns true if at least one block has been indexed.
func (i *ChainIndexer) HasIndexedAtLeastOneBlock() bool {
	return i.hasIndexedAtLeastOneBlock
}

// GetDefraDBPort returns the port of the embedded DefraDB node, or -1 if using external DefraDB.
func (i *ChainIndexer) GetDefraDBPort() int {
	if i.defraNode == nil {
		return -1
	}
	return defra.GetPort(i.defraNode)
}

// CreateIndexer creates a new ChainIndexer with the provided configuration.
func CreateIndexer(cfg *config.Config) (*ChainIndexer, error) {
	if cfg == nil {
		return nil, indexerErrors.NewConfigurationError(
			"indexer",
			"CreateIndexer",
			"config is nil",
			"host=nil, port=nil",
			nil,
			indexerErrors.WithMetadata("host", "nil"),
			indexerErrors.WithMetadata("port", "nil"))
	}
	return &ChainIndexer{
		cfg:                       cfg,
		shouldIndex:               false,
		isStarted:                 false,
		hasIndexedAtLeastOneBlock: false,
	}, nil
}

// StartIndexing initializes dependencies and starts concurrent block indexing.
func (i *ChainIndexer) StartIndexing(defraStarted bool) (err error) {
	var ctx context.Context
	cfg := i.cfg

	if cfg == nil {
		return fmt.Errorf("configuration is required - use config.LoadConfig() to load configuration")
	}

	cfg.DefraDB.P2P.BootstrapPeers = append(cfg.DefraDB.P2P.BootstrapPeers, []string{
		// Add any "big peers" here to speed up peer discovery.
	}...)

	if logger.Sugar == nil {
		logger.Init(cfg.Logger.Development)
	}
	logger.Sugar.Infof("Starting Shinzo Network Generator %s", Version)

	// Init runs against a stop-cancellable context: initDefra, Connect and
	// resolveStartHeight all honor ctx, so a concurrent StopIndexing aborts a
	// parked RPC promptly instead of leaving init doomed after a timed-out wait.
	initCtx, initCancel := context.WithCancelCause(context.Background())
	defer initCancel(nil)

	// Error guard with abort mapping: a stop-during-init cancellation
	// (errIndexingStopped cause) surfaces as a wrapped context.Canceled —
	// report nil so an aborted start doesn't look like an init failure, and
	// let the stopping StopIndexing own the teardown instead of racing it.
	defer i.finishStart(initCtx, &err)

	// Mark the start as in-flight. Registered after the error guard so LIFO
	// runs the settle defer BEFORE the guard's own StopIndexing on error
	// paths — the guard then never waits on this very start (no deadlock).
	i.beginStart(initCancel)
	defer i.markStartSettled()

	// 1. Create fetcher (no dial yet) + converter — via factory dispatch, no evm import
	fetcher, err := chains.NewFetcher(cfg)
	if err != nil {
		return fmt.Errorf("failed to create fetcher: %w", err)
	}
	i.fetcher = fetcher
	i.converter, err = chains.NewConverter(cfg)
	if err != nil {
		return fmt.Errorf("failed to create converter: %w", err)
	}

	// 2. Log prefix (uses converter only — no RPC needed)
	logger.Sugar.Infof("Indexing chain: %s (prefix: %s)", cfg.Chain.Name+"__"+cfg.Chain.Network, i.converter.Collections().Prefix())

	// 3. Start DefraDB (uses converter.Collections() + converter.GetCollections())
	ctx, err = i.initDefra(initCtx, cfg, defraStarted)
	if err != nil {
		return err
	}

	// 4. Connect fetcher (context-aware dial — was done in NewAdapter at construction)
	if err := i.fetcher.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect fetcher: %w", err)
	}

	// 5. Create block handler (was done in adapter.Init)
	i.blockHandler, err = newBlockHandlerFn(i.defraNode, cfg.Indexer.MaxDocsPerTxn)
	if err != nil {
		return fmt.Errorf("failed to create block handler: %w", err)
	}

	// 6. Resolve start height (uses converter + fetcher, no chain param)
	nextBlockToProcess, err := i.resolveStartHeight(ctx, cfg)
	if err != nil {
		return err
	}

	i.shouldIndex = true
	logger.Sugar.Info("Starting indexer - will process latest blocks from Geth ", cfg.Geth.NodeURL)

	// 7. Init services (pruner/snapshot/health — now take converter)
	if err := i.initServices(ctx, cfg); err != nil {
		return err
	}

	// 8. Run concurrent indexing
	if cfg.Indexer.ConcurrentBlocks >= 1 && i.defraNode != nil {
		logger.Sugar.Infof("Using concurrent block processing with %d workers", cfg.Indexer.ConcurrentBlocks)
		return i.runConcurrentIndexing(ctx, nextBlockToProcess, cfg)
	}
	return nil
}

// initDefra starts or connects to DefraDB and returns an updated context with identity.
func (i *ChainIndexer) initDefra(ctx context.Context, cfg *config.Config, defraStarted bool) (context.Context, error) {
	if !defraStarted {

		logger.Sugar.Debugf("P2P config: ListenAddr: '%s', BootstrapPeers: %v, Enabled: %t",
			cfg.DefraDB.P2P.ListenAddr, cfg.DefraDB.P2P.BootstrapPeers, cfg.DefraDB.P2P.Enabled)

		var replicationFilter client.ReplicationFilter
		if !cfg.DefraDB.P2P.AcceptIncoming {
			replicationFilter = &indexerReplicationFilter{}
		}

		defraNode, networkHandler, err := defradb.StartDefraInstance(cfg,
			defradb.NewSchemaApplierFromDir(i.converter.Collections()), nil, replicationFilter, i.converter.GetCollections()...)
		if err != nil {
			return ctx, fmt.Errorf("failed to start DefraDB instance: %w", err)
		}
		i.defraNode = defraNode
		i.networkHandler = networkHandler

		if err := waitForDefraDBFn(defraNode.APIURL); err != nil {
			return ctx, err
		}

		// Get the identity context for block signing
		identityCtx, err := defradb.GetIdentityContext(ctx, cfg)
		if err != nil {
			logger.Sugar.Warnf("Failed to get identity context for block signing: %v (block signatures may not work)", err)
		} else {
			ctx = identityCtx
			logger.Sugar.Info("Identity context initialized for block signing")
		}
	} else {
		if err := waitForDefraDBFn(cfg.DefraDB.URL); err != nil {
			return ctx, err
		}
		if err := defradb.ApplyCollectionSchemasViaHTTP(ctx, cfg.DefraDB.URL, i.converter.Collections()); err != nil {
			return ctx, fmt.Errorf("failed to apply schema to external DefraDB: %w", err)
		}
	}

	if i.defraNode == nil {
		return ctx, fmt.Errorf("defraNode is required - external DefraDB via HTTP is no longer supported")
	}

	return ctx, nil
}

// resolveStartHeight determines the block number to start indexing from.
func (i *ChainIndexer) resolveStartHeight(ctx context.Context, cfg *config.Config) (int64, error) {
	configuredHeight := int64(cfg.Indexer.StartHeight)
	var highestExisting int64
	var pruneQueue *pruner.IndexerQueue

	if cfg.Pruner.Enabled {
		pruneQueue = pruner.NewIndexerQueue()
		queueFilePath := filepath.Join(cfg.DefraDB.Store.Path, "prune_queue.gob")
		loaded, err := pruneQueue.LoadFromFile(queueFilePath)
		if err != nil {
			logger.Sugar.Warnf("Failed to load prune queue from disk: %v", err)
		} else if loaded > 0 {
			logger.Sugar.Infof("Restored %d entries from prune queue file", loaded)
		}
		highestExisting = pruneQueue.HighestBlockNumber()
	}

	if highestExisting == 0 {
		nBlock, err := i.converter.GetHighestStoredBlockNumber(ctx, i.defraNode)
		if err != nil {
			logger.Sugar.Debugf("No existing blocks found in DB: %v", err)
		} else {
			highestExisting = nBlock
		}
	}

	latestBlock, err := i.fetcher.FetchHighestBlockNumber(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get latest block number from RPC: %w", err)
	}
	chainTip := latestBlock
	startBuffer := int64(cfg.Indexer.StartBuffer)

	switch {
	case highestExisting > 0:
		resumeFrom := highestExisting + 1
		gap := chainTip - highestExisting
		if gap > startBuffer {
			resumeFrom = chainTip - startBuffer
			logger.Sugar.Infof("Gap of %d blocks, skipping ahead to %d (chain tip: %d)", gap, resumeFrom, chainTip)
		}
		cfg.Indexer.StartHeight = int(resumeFrom)
		logger.Sugar.Infof("Resuming from block %d (highest existing: %d, chain tip: %d)", cfg.Indexer.StartHeight, highestExisting, chainTip)
	case configuredHeight > 0:
		logger.Sugar.Infof("Starting from configured height %d (chain tip: %d)", configuredHeight, chainTip)
	default:
		cfg.Indexer.StartHeight = max(int(chainTip-startBuffer), 0)
		logger.Sugar.Infof("No existing blocks, starting from %d (chain tip: %d)", cfg.Indexer.StartHeight, chainTip)
	}

	return int64(cfg.Indexer.StartHeight), nil
}

// newSchemaAuthenticator builds the authenticator for the schema endpoint and
// warns when token auth is fail-closed with zero API keys configured.
func newSchemaAuthenticator(cfg *config.Config) (server.Authenticator, error) {
	auth, err := newAuthenticator(cfg.Indexer.SchemaAuthMode, cfg.Indexer.SchemaAPIKeys)
	if err != nil {
		return nil, fmt.Errorf("schema auth configuration error: %w", err)
	}
	if (cfg.Indexer.SchemaAuthMode == constants.SchemaAuthModeToken || cfg.Indexer.SchemaAuthMode == "") && len(cfg.Indexer.SchemaAPIKeys) == 0 {
		logger.Sugar.Warn("schema auth is fail-closed with zero API keys configured — " +
			"all schema requests will return 503. Set SCHEMA_API_KEYS env var (comma-separated) " +
			"or set SCHEMA_AUTH_MODE=none to disable token auth")
	}
	return auth, nil
}

// initServices starts the health server, pruner, and snapshotter if configured.
func (i *ChainIndexer) initServices(ctx context.Context, cfg *config.Config) error {
	if cfg.Indexer.HealthServerPort > 0 {
		if err := i.initHealthServer(cfg); err != nil {
			return err
		}
	}

	if cfg.Pruner.Enabled && i.defraNode != nil {
		i.pruner = pruner.NewPruner(&cfg.Pruner, i.defraNode, i.converter)
		pruneQueue := pruner.NewIndexerQueue()
		// Binds the queue to its file before anything tracks into it. Save is a no-op until this
		// runs, so without it the queue is never written and never survives a restart.
		queueFilePath := filepath.Join(cfg.DefraDB.Store.Path, "prune_queue.gob")
		restored, err := pruneQueue.LoadFromFile(queueFilePath)
		if err != nil {
			logger.Sugar.Warnf("Failed to load prune queue from disk: %v", err)
		} else if restored > 0 {
			logger.Sugar.Infof("Restored %d entries from prune queue file", restored)
		}
		i.pruner.SetQueue(pruneQueue)
		i.blockHandler.SetDocIDTracker(&indexerQueueTracker{
			queue:       pruneQueue,
			collections: i.converter.Collections(),
		})
		logger.Sugar.Infof("Prune queue ready (queue=%d, max_blocks=%d)", pruneQueue.Len(), cfg.Pruner.MaxBlocks)
		if err := i.pruner.Start(ctx); err != nil {
			logger.Sugar.Warnf("Failed to start pruner: %v", err)
		}
	}

	if cfg.Snapshot.Enabled && i.defraNode != nil {
		i.snapshotter = snapshot.New(&cfg.Snapshot, i.defraNode, i.converter)
		if err := i.snapshotter.Start(ctx); err != nil {
			logger.Sugar.Warnf("Failed to start snapshotter: %v", err)
		}
		if i.healthServer != nil {
			i.healthServer.SetSnapshotter(i.snapshotter)
		}
	}

	return nil
}

// initHealthServer creates and starts the health server with schema and hub endpoints configured.
func (i *ChainIndexer) initHealthServer(cfg *config.Config) error {
	// The configured address is rewritten before the API binds, so probe the bound address.
	var healthDefraURL string
	if i.defraNode != nil {
		healthDefraURL = i.defraNode.APIURL
	}
	i.healthServer = server.NewHealthServer(cfg.Indexer.HealthServerPort, i, healthDefraURL)
	if i.defraNode != nil {
		i.healthServer.SetDefraNode(i.defraNode)
	}
	if cfg.Chain.Hub != "" {
		i.healthServer.SetShinzoHubRESTBase(server.ShinzoHubAPIURL(cfg.Chain.Hub, server.ShinzoHubProtoAPIPort))
	}

	auth, err := newSchemaAuthenticator(cfg)
	if err != nil {
		return err
	}
	prefix := i.converter.Collections().Prefix()
	sdl, err := i.converter.GetSchema()
	if err != nil {
		return fmt.Errorf("load schema for chain %s: %w", prefix, err)
	}
	if err := i.healthServer.EnableSchemaEndpoint(sdl, i.converter.Collections(), auth); err != nil {
		return fmt.Errorf("enable schema endpoint: %w", err)
	}
	go func() {
		if err := i.healthServer.Start(); err != nil {
			logger.Sugar.Errorf("Health server failed: %v", err)
		}
	}()
	if cfg.Indexer.OpenBrowserOnStart {
		go func() {
			time.Sleep(ShortDelayTime)
			openBrowser(fmt.Sprintf("http://localhost:%d/health", cfg.Indexer.HealthServerPort))
			logger.Sugar.Infof("Opened health page in browser")
		}()
	}
	return nil
}

// runConcurrentIndexing runs the indexer with concurrent block processing.
// The context is wrapped with a cancel cause so StopIndexing can trigger a
// clean shutdown (surfaced as a nil error) while external context
// cancellation still propagates as context.Canceled.
func (i *ChainIndexer) runConcurrentIndexing(
	ctx context.Context,
	startBlock int64,
	cfg *config.Config,
) error {
	i.shouldIndex = true
	i.isStarted = true

	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	i.mutex.Lock()
	i.indexingCancel = cancel
	i.indexingDone = done
	i.mutex.Unlock()
	defer func() {
		cancel(nil)
		close(done)
		i.mutex.Lock()
		i.indexingCancel = nil
		i.indexingDone = nil
		i.mutex.Unlock()
	}()

	// Init is complete and indexingCancel is already registered: from here
	// the indexing drain in StopIndexing owns shutdown, so release any
	// StopIndexing parked in waitStartSettled.
	i.markStartSettled()

	// A stop that arrived during init cancelled the init context, which this
	// context derives from: bail before constructing a processor that would
	// exit immediately anyway.
	if errors.Is(context.Cause(ctx), errIndexingStopped) {
		return nil
	}

	processor := NewConcurrentBlockProcessor(
		i.fetcher,
		i.converter,
		i.blockHandler,
		cfg.Indexer.ConcurrentBlocks,
		cfg.Indexer.BlocksPerMinute,
	)

	err := processor.ProcessBlocks(ctx, startBlock, func(blockNum int64) {
		i.updateBlockInfo(blockNum)
		i.hasIndexedAtLeastOneBlock = true
	})
	if errors.Is(context.Cause(ctx), errIndexingStopped) {
		return nil
	}
	return err
}

// beginStart marks a StartIndexing as in-flight by setting the flag, creating
// the settle channel and storing the init cancel handle, all guarded by
// i.mutex.
func (i *ChainIndexer) beginStart(initCancel context.CancelCauseFunc) {
	i.mutex.Lock()
	defer i.mutex.Unlock()
	i.startInProgress = true
	i.startDone = make(chan struct{})
	i.initCancel = initCancel
}

// markStartSettled signals that StartIndexing is no longer in its init
// phase: it has either returned or (once runConcurrentIndexing registered
// the indexing drain) handed shutdown ownership to StopIndexing's drain.
// Swap-under-mutex close makes repeated calls no-ops, so the channel is
// closed exactly once regardless of which site runs first. The stored
// init cancel is dropped without calling it: at handoff the indexing loop's
// context derives from the init context, so cancelling here would kill the
// freshly started loop — StartIndexing's own defer releases it on return.
func (i *ChainIndexer) markStartSettled() {
	i.mutex.Lock()
	defer i.mutex.Unlock()
	if i.startDone != nil {
		close(i.startDone)
		i.startDone = nil
	}
	i.startInProgress = false
	i.initCancel = nil
}

// finishStart is StartIndexing's exit guard. A start aborted by a concurrent
// StopIndexing fails its init calls with an abort artifact — the cancellation
// can surface either as a wrapped context.Canceled (ctx.Err() returns) or as
// the errIndexingStopped cause itself (net/http propagates the context cause
// on request cancellation) — while the context cause is errIndexingStopped.
// That combination is reported as nil (a stopped start is not an init
// failure) and the teardown is left to the already-running StopIndexing
// instead of starting a second concurrent one. Genuine init failures still
// tear down residual state via StopIndexing.
func (i *ChainIndexer) finishStart(initCtx context.Context, errp *error) {
	if *errp == nil {
		return
	}
	if errors.Is(context.Cause(initCtx), errIndexingStopped) &&
		(errors.Is(*errp, context.Canceled) || errors.Is(*errp, errIndexingStopped)) {
		*errp = nil
		return
	}
	// teardown must not inherit initCtx: by the time a genuine init failure
	// reaches this guard the context may already be cancelled, and shutdown
	// operations must not be aborted by it.
	i.StopIndexing() //nolint:contextcheck // teardown needs a fresh context; initCtx is cancelled on the error path
}

// waitStartSettled blocks until an in-flight StartIndexing has settled
// (returned, or handed off to the drainable indexing loop), bounded by
// IndexingStartStopTimeout. It never holds i.mutex while waiting.
func (i *ChainIndexer) waitStartSettled() {
	i.mutex.Lock()
	inProgress, startDone := i.startInProgress, i.startDone
	i.mutex.Unlock()
	if !inProgress || startDone == nil {
		return
	}
	select {
	case <-startDone:
	case <-time.After(IndexingStartStopTimeout):
		logger.Sugar.Warn("StartIndexing still running; tearing down anyway")
	}
}

// StopIndexing halts the indexer and cleanly shuts down all subsystems.
// Calls are serialized by stopMu so the error guard's Stop and an external
// Stop — which can wake at the same moment when a parked start settles —
// never run teardown concurrently.
func (i *ChainIndexer) StopIndexing() {
	i.stopMu.Lock()
	defer i.stopMu.Unlock()

	// Abort an in-flight StartIndexing first: init calls honor the init
	// context, so a parked RPC returns promptly and the start settles instead
	// of this wait having to ride out the full timeout on a hung endpoint.
	i.mutex.Lock()
	initCancel := i.initCancel
	i.mutex.Unlock()
	if initCancel != nil {
		initCancel(errIndexingStopped)
	}

	// Wait for the aborted/in-flight StartIndexing to settle (return, or hand
	// off to the indexing loop) before touching anything it may be assigning
	// or using mid-init (fetcher, defraNode) — bounded by
	// IndexingStartStopTimeout.
	i.waitStartSettled()

	// Drain the indexing loop before any subsystem teardown: cancel the
	// indexing context and wait for the block processor (workers + signers)
	// to exit so nothing is mid-query when the fetcher/defra node close.
	i.mutex.Lock()
	cancel := i.indexingCancel
	done := i.indexingDone
	i.mutex.Unlock()

	if cancel != nil {
		cancel(errIndexingStopped)
		select {
		case <-done:
		case <-time.After(IndexingStopTimeout):
			logger.Sugar.Warn("block processor stop timed out; closing resources anyway")
		}
	}

	i.teardownSubsystems()
}

// teardownSubsystems closes and nils every owned subsystem. All steps are
// nil-checked, so a repeated serialized Stop is a benign no-op, and a
// mid-init abort still releases each component already assigned.
func (i *ChainIndexer) teardownSubsystems() {
	i.shouldIndex = false
	i.isStarted = false

	// Stop snapshotter before pruner (capture data before it's pruned)
	if i.snapshotter != nil {
		i.snapshotter.Stop()
		i.snapshotter = nil
	}

	// Stop pruner
	if i.pruner != nil {
		i.pruner.Stop()
		i.pruner = nil
	}

	// Stop health server
	if i.healthServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), DefaultRetryDelay)
		defer cancel()
		_ = i.healthServer.Stop(ctx)
	}

	// Close fetcher (closes RPC client)
	if i.fetcher != nil {
		_ = i.fetcher.Close()
		i.fetcher = nil
	}

	// Stop P2P network handler before closing the node
	if i.networkHandler != nil {
		ctx, cancel := context.WithTimeout(context.Background(), DefaultRetryDelay)
		defer cancel()
		_ = i.networkHandler.StopNetwork(&ctx)
		i.networkHandler = nil
	}

	// Close embedded DefraDB node if it exists
	if i.defraNode != nil {
		_ = i.defraNode.Close(context.Background())
		i.defraNode = nil
	}
}

// IsHealthy returns true if the indexer is running and has processed blocks recently.
func (i *ChainIndexer) IsHealthy() bool {
	i.mutex.RLock()
	defer i.mutex.RUnlock()

	// Consider healthy if started and processed at least one block recently
	if !i.isStarted {
		return false
	}

	// If we've never processed a block, we're still healthy (starting up)
	if i.lastProcessedTime.IsZero() {
		return true
	}

	// Consider unhealthy if no blocks processed in last 10 minutes
	return time.Since(i.lastProcessedTime) < 10*time.Minute
}

// GetCurrentBlock returns the last processed block number.
func (i *ChainIndexer) GetCurrentBlock() int64 {
	i.mutex.RLock()
	defer i.mutex.RUnlock()
	return i.currentBlock
}

// GetLastProcessedTime returns the time at which the last block was processed.
func (i *ChainIndexer) GetLastProcessedTime() time.Time {
	i.mutex.RLock()
	defer i.mutex.RUnlock()
	return i.lastProcessedTime
}

// GetPeerInfo returns DefraDB P2P network information.
func (i *ChainIndexer) GetPeerInfo() (*server.P2PInfo, error) {
	i.mutex.RLock()
	defer i.mutex.RUnlock()

	// If no embedded DefraDB node, return nil.
	if i.defraNode == nil {
		return nil, fmt.Errorf("defra is nil - peer info not available for external DefraDB")
	}

	ctx := context.Background()

	// Use NetworkHandler to determine if P2P is active.
	networkActive := i.networkHandler != nil && i.networkHandler.IsNetworkActive()

	// Get this node's own peer info (listening addresses).
	ownAddresses, err := i.defraNode.DB.PeerInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("error fetching own peer info: %w", err)
	}
	ownPeers, _ := defradb.BootstrapIntoPeers(ownAddresses)

	var selfInfo *server.PeerInfo
	if len(ownPeers) > 0 {
		// Collect all addresses for our own peer ID.
		var addresses []string
		for _, p := range ownPeers {
			addresses = append(addresses, p.Addresses...)
		}
		selfInfo = &server.PeerInfo{
			ID:        ownPeers[0].ID,
			Addresses: addresses,
			PublicKey: extractPublicKeyFromPeerID(ownPeers[0].ID),
		}
	}

	// Get actually connected peers (may fail if P2P is not initialized).
	activePeerStrings, err := i.defraNode.DB.ActivePeers(ctx)
	if err != nil {
		activePeerStrings = nil // P2P not available, treat as no peers.
	}
	activePeers, _ := defradb.BootstrapIntoPeers(activePeerStrings)

	// Deduplicate peers by ID and merge addresses.
	peerMap := make(map[string]*server.PeerInfo)
	for _, peer := range activePeers {
		if existing, ok := peerMap[peer.ID]; ok {
			existing.Addresses = append(existing.Addresses, peer.Addresses...)
		} else {
			peerMap[peer.ID] = &server.PeerInfo{
				ID:        peer.ID,
				Addresses: peer.Addresses,
				PublicKey: extractPublicKeyFromPeerID(peer.ID),
			}
		}
	}
	serverPeerInfo := make([]server.PeerInfo, 0, len(peerMap))
	for _, p := range peerMap {
		serverPeerInfo = append(serverPeerInfo, *p)
	}

	return &server.P2PInfo{
		Self:     selfInfo,
		PeerInfo: serverPeerInfo,
		Enabled:  networkActive,
	}, nil
}

// extractPublicKeyFromPeerID attempts to extract the public key from a libp2p PeerID.
func extractPublicKeyFromPeerID(peerID string) string {
	// Parse the PeerID string into a libp2p peer.ID
	id, err := peer.Decode(peerID)
	if err != nil {
		logger.Sugar.Warnf("Failed to decode PeerID %s: %v", peerID, err)
		return ""
	}

	// Extract the public key from the PeerID.
	pubKey, err := id.ExtractPublicKey()
	if err != nil {
		logger.Sugar.Warnf("Failed to extract public key from PeerID %s: %v", peerID, err)
		return ""
	}

	// Convert public key to bytes and then to hex string.
	pubKeyBytes, err := pubKey.Raw()
	if err != nil {
		logger.Sugar.Warnf("Failed to get raw bytes from public key: %v", err)
		return ""
	}

	// Return hex-encoded public key.
	return hex.EncodeToString(pubKeyBytes)
}

// updateBlockInfo updates the current block and last processed time.
func (i *ChainIndexer) updateBlockInfo(blockNum int64) {
	i.mutex.Lock()
	defer i.mutex.Unlock()
	i.currentBlock = blockNum
	i.lastProcessedTime = time.Now()
}

// execCommand is a variable to allow mocking exec.Command in tests. It is used by openBrowser to launch the default web browser.
var execCommand = exec.Command //nolint:gochecknoglobals // test seam for mocking exec.Command in unit tests

// newBlockHandlerFn is a test seam for mocking defra.NewBlockHandler in StartIndexing error-path tests.
var newBlockHandlerFn = defra.NewBlockHandler //nolint:gochecknoglobals // test seam for mocking defra.NewBlockHandler in unit tests

// waitForDefraDBFn is a test seam for mocking defra.WaitForDefraDB in StartIndexing error-path tests.
var waitForDefraDBFn = defra.WaitForDefraDB //nolint:gochecknoglobals // test seam for mocking defra.WaitForDefraDB in unit tests

// openBrowser opens the specified URL in the default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = execCommand("cmd", "/c", "start", url)
	case "darwin":
		cmd = execCommand("open", url)
	default: // linux and others
		cmd = execCommand("xdg-open", url)
	}

	if err := cmd.Start(); err != nil {
		logger.Sugar.Warnf("Failed to open browser: %v", err)
		return
	}
	logger.Sugar.Infof("Opened health page in browser: %s", url)
}

// SignMessages signs a registration message using both DefraDB and P2P keys.
func (i *ChainIndexer) SignMessages(message string) (server.DefraPKRegistration, server.PeerIDRegistration, error) {
	signedMsg, err := signer.SignWithDefraKeys(message, i.defraNode, i.cfg)
	if err != nil {
		return server.DefraPKRegistration{}, server.PeerIDRegistration{}, err
	}

	// Sign with peer ID
	peerSignedMsg, err := signer.SignWithP2PKeys(message, i.defraNode, i.cfg)
	if err != nil {
		return server.DefraPKRegistration{}, server.PeerIDRegistration{}, err
	}

	// Get node and peer public keys from signer helpers.
	nodePubKey, err := i.GetNodePublicKey()
	if err != nil {
		return server.DefraPKRegistration{}, server.PeerIDRegistration{}, fmt.Errorf("failed to get node public key: %w", err)
	}

	peerPubKey, err := i.GetPeerPublicKey()
	if err != nil {
		return server.DefraPKRegistration{}, server.PeerIDRegistration{}, fmt.Errorf("failed to get peer public key: %w", err)
	}

	return server.DefraPKRegistration{
			PublicKey:   nodePubKey,
			SignedPKMsg: signedMsg,
		}, server.PeerIDRegistration{
			PeerID:        peerPubKey,
			SignedPeerMsg: peerSignedMsg,
		}, nil
}

// SignRegistrationMessage signs a registration message using only the DefraDB identity key.
func (i *ChainIndexer) SignRegistrationMessage(message string) (server.DefraPKRegistration, error) {
	signedMsg, err := signer.SignWithDefraKeys(message, i.defraNode, i.cfg)
	if err != nil {
		return server.DefraPKRegistration{}, err
	}

	nodePubKey, err := i.GetNodePublicKey()
	if err != nil {
		return server.DefraPKRegistration{}, fmt.Errorf("failed to get node public key: %w", err)
	}

	return server.DefraPKRegistration{
		PublicKey:   nodePubKey,
		SignedPKMsg: signedMsg,
	}, nil
}

// GetSourceChainInfo returns the ShinzoHub registration source chain metadata for this indexer.
func (i *ChainIndexer) GetSourceChainInfo() (string, uint64) {
	if i == nil || i.cfg == nil {
		return "", 0
	}

	name := strings.ToLower(strings.TrimSpace(i.cfg.Chain.Name))
	network := strings.ToLower(strings.TrimSpace(i.cfg.Chain.Network))
	if (name == "" || name == "ethereum") && (network == "" || network == "mainnet") {
		return "ethereum", 1
	}

	return "", 0
}

// GetNodePublicKey returns the DefraDB node's public key as a hex string.
func (i *ChainIndexer) GetNodePublicKey() (string, error) {
	return signer.GetDefraPublicKey(i.defraNode, i.cfg)
}

// GetPeerPublicKey returns the P2P peer's public key as a hex string.
func (i *ChainIndexer) GetPeerPublicKey() (string, error) {
	return signer.GetP2PPublicKey(i.defraNode, i.cfg)
}

// GetPrunerMetrics returns the current pruner metrics, or nil if pruner is not enabled.
func (i *ChainIndexer) GetPrunerMetrics() *pruner.Metrics {
	if i.pruner == nil {
		return nil
	}
	metrics := i.pruner.GetMetrics()
	return &metrics
}

// newAuthenticator constructs an Authenticator based on the configured auth mode.
func newAuthenticator(mode string, keys []string) (server.Authenticator, error) {
	switch mode {
	case constants.SchemaAuthModeNone:
		return server.NoOpAuthenticator{}, nil
	case constants.SchemaAuthModeToken, "":
		return server.NewBearerAuthenticator(keys), nil
	case constants.SchemaAuthModeMTLS:
		return nil, ErrMTLSNotImplemented
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownAuthMode, mode)
	}
}

// indexerQueueTracker adapts pruner's IndexerQueue to the local DocIDTrackerInterface.
type indexerQueueTracker struct {
	queue       *pruner.IndexerQueue
	collections chains.Collections
}

func (t *indexerQueueTracker) TrackBlock(_ context.Context, blockNumber int64, result *defra.BlockCreationResult) error {
	return t.queue.TrackBlockDocIDs(blockNumber, result.BlockID, result.OtherDocIDs, result.BlockSignatureID)
}
