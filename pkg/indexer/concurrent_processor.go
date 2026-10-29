package indexer

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	stderrors "errors"
)

const (
	// BlockNotFoundRetryDelay is the delay before retrying when a block is not yet available on chains.
	BlockNotFoundRetryDelay = 3 * time.Second

	// DispatchThrottleDelay is the delay when the processor is too far ahead of committed blocks.
	DispatchThrottleDelay = 100 * time.Millisecond

	// FinalizedPollInterval is how often dispatch asks for the finalized height once it has reached
	// it, or while the fetcher cannot report one.
	FinalizedPollInterval = time.Second

	// transactionConflictRetryBaseDelay is the base delay for retrying
	// transaction conflicts on store.
	transactionConflictRetryBaseDelay = 50 * time.Millisecond

	// MaxRPCRetries is the maximum number of retries for non-"not found" RPC
	// errors.
	MaxRPCRetries = 3

	// RPCErrorRetryBaseDelay is the base delay for retrying RPC errors.
	RPCErrorRetryBaseDelay = 500 * time.Millisecond

	// maxRollbacksPerRun bounds consecutive rollbacks without an intervening
	// successful commit (episode semantics: any commit resets the counter).
	// Exceeding it means the RPC node is persistently serving inconsistent
	// data.
	maxRollbacksPerRun = 64

	// maxRollbacksPerHeight bounds consecutive rollbacks restarting from the
	// same height; repeating the same height means the node keeps serving
	// forked data for that height.
	maxRollbacksPerHeight = 3

	// maxRewalksPerEpisode bounds the chain re-anchors tolerated during one
	// confirmation wait: each parked result that breaks the verified
	// new-chain segment re-anchors the episode, and a node that keeps
	// flip-flopping between chains must not park the run forever.
	maxRewalksPerEpisode = 64

	// maxGapRefillsPerWait bounds the same-height re-fetches during one
	// confirmation wait: usable content that still yields no hashes would
	// otherwise spin the wait forever.
	maxGapRefillsPerWait = 64

	// maxWalkBackDepth is the hard cap on the reorg walk-back depth when the
	// chain cannot report a finalized height. Finality is not a walk bound,
	// so with a reported finalized height the descent continues below it and
	// the cap does not apply.
	maxWalkBackDepth = 64
)

// errReorgDetected is the cancel cause recorded on the run-scoped context
// when a run must end: either the episode procedure resolved a reorg or a
// fatal run error was recorded. Nothing branches on the cause - the
// rollbackPoint sentinel and fatalErr drive the restart and the returned
// error - but it keeps the cancellation reason inspectable.
var errReorgDetected = stderrors.New("reorg detected")

// rollbackPoint marks a resolved reorg: the walk-back found the common
// ancestor at forkPoint, so the orphan range [forkPoint+1, oldTip] must be
// rolled back and indexing restarted from forkPoint+1. oldTip is the highest
// height dispatched when the run drained; heights above the last stored
// block hold nothing to delete, so it acts as an upper bound. chainHashes
// carries the verified new-chain hash per height for the walked segment -
// it travels to the rollback handler as the explicit chainHashes
// parameter so the roller deletes only heights whose
// stored hash disagrees with the new chain.
type rollbackPoint struct {
	forkPoint int64
	oldTip    int64

	// chainHashes maps height to the verified new-chain hash for every
	// height the walk-back and the confirmation wait touched: the common
	// ancestor, every descended height, the parked mismatch and its
	// confirmed successor.
	chainHashes map[int64]string
}

// reorgSignal describes a detected continuity break: the stored (orphan)
// hash at rollbackFrom and the chain-side hash it disagrees with. It seeds
// both the log line and the walk-back descent.
type reorgSignal struct {
	rollbackFrom int64
	orphanHash   string
	chainHash    string
}

// reorgEpisode is the confirmation-wait state between a detected mismatch
// and the confirmed resolution: results at and above baseHeight are held
// back (parked, never stored - the ordered pop drops them and keeps only
// the hashes) while the collector waits for the first in-order chained
// successor to confirm the new chain, re-anchors on every further
// disagreement (bounded by maxRewalksPerEpisode), and then hands the
// fork-point search to the walk-back.
//
// Stores run on the bounded pool, so an episode can also open
// asynchronously (stalePinned): a pool store failed at a height whose
// stored hash was verified stale by the failure handler. That episode
// finalizes immediately - the stale evidence is already two-sided - and
// the walk-back descends from the stale height instead of waiting for a
// popped in-order successor.
type reorgEpisode struct {
	// baseHeight is the first parked height: the mismatching block itself.
	baseHeight int64
	// baseParentHash is the new chain's hash at baseHeight-1, taken from the
	// mismatching result's parent hash; the walk-back descent starts here.
	baseParentHash string
	// lastParkedHash is the new chain's hash at the highest parked height;
	// the next in-order parked result must chain from it.
	lastParkedHash string
	// rewalks counts re-anchors performed during this confirmation wait.
	rewalks int
	// gapRefills counts unusable-verification re-fetches performed during
	// this confirmation wait.
	gapRefills int

	// stalePinned marks an episode opened asynchronously by the pool-side
	// failure handler: a store failure was verified against the stored hash
	// at the failed height and disagrees with the verified fetched hash.
	stalePinned bool
	// staleTopHeight is the highest height verified stale for this episode
	// (updated while completions drain during the finalize): the walk-back
	// descends from it because every lower stale height must be visited,
	// and descent above it would match the landed canonical stores before
	// reaching the orphan.
	staleTopHeight int64
	// staleTopHash is the verified new-chain hash at staleTopHeight, taken
	// from the failed store's fetched content; the descent verifies against
	// the stored record from here.
	staleTopHash string
}

// storeCompletion reports one pool-side store outcome back to the collector.
type storeCompletion struct {
	// item echoes the fetched (checked) item the store consumed; its
	// BlockNum, Hash, and ParentHash drive the ordered apply and the
	// commit bookkeeping.
	item *BlockResult
	// computed is the storeWithRetry outcome for the same height.
	computed *BlockResult
	// stale is the pool-side stale-orphan verification of a failed store:
	// non-nil only when the stored hash at the failed height was queried,
	// and it disagrees with the failed item's verified fetched hash.
	stale *reorgSignal
}

type processorMetrics struct {
	blocks           metric.Int64Counter
	storeDuration    metric.Float64Histogram
	reorgs           metric.Int64Counter
	success, failure metric.MeasurementOption
}

// BlockResult holds the result of processing a block.
type BlockResult struct {
	BlockNum   int64
	BlockID    string
	Hash       string
	ParentHash string
	Success    bool
	Error      error
	// Converted carries the conversion result the bounded store pool stores
	// - but only after the parentHash continuity check passed on the
	// collector's pop flow.
	Converted chains.ConversionResult
}

// BlockStorer is the store-side interface used by the processor. The concrete
// *defra.BlockHandler satisfies it; the interface enables pure unit tests with
// a mock.
type BlockStorer interface {
	Store(ctx context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error)
	SignExisting(ctx context.Context, result chains.ConversionResult, blockHash string, blockNumber int64) (string, error)
}

// ConcurrentBlockProcessor processes multiple blocks concurrently.
type ConcurrentBlockProcessor struct {
	fetcher         chains.Fetcher
	converter       chains.Converter
	blockHandler    BlockStorer
	reorgHandler    defra.ReorgHandler
	workers         int
	blocksPerMinute int
	resultChan      chan *BlockResult
	signWg          sync.WaitGroup
	pendingMu       sync.Mutex
	pending         map[int64]*BlockResult
	nextToCommit    int64

	// finalizedOnly bounds dispatch at the fetcher's finalized height. finalized is the last
	// height the fetcher reported; only the dispatch loop uses it.
	finalizedOnly bool
	finalized     int64

	// bounded store pool: the collector never calls Store. Fetch/convert
	// workers hand checked results to storeChan, pool goroutines run
	// storeWithRetry, and completions flow back on completionChan for the
	// collector's ordered apply. The pool size caps store concurrency at
	// p.workers; all three channels live one run, so a rollback restart
	// builds a fresh pool.
	storeChan      chan *BlockResult
	completionChan chan storeCompletion
	// inFlightWg counts stores handed to the pool: the collector Adds when a
	// checked item starts waiting for the pool (before any handoff can
	// start), and the count leaves the group exactly once per item - a pool
	// worker Dones after its completion send, the collector Dones when it
	// abandons the waiting item (episode hold-back or cancellation), and
	// drainInFlightStores Dones a still-held dispatch at its entry, because
	// a held item's release sites all sit on the collector, which the drain
	// blocks. The registration therefore happens before the send's
	// rendezvous, so even an instantly reported store cannot drop ahead of
	// its Add, and Wait returning implies every registration is either
	// paired with an already-buffered completion or was abandoned before
	// the drain. Add and Wait both run on the single collector goroutine,
	// so the Add-while-Waiting hazard cannot occur, and the group needs no
	// per-run reset: shutdown settles the pool before the next run can
	// start.
	inFlightWg sync.WaitGroup

	// holdingDispatch is the one checked item waiting for pool capacity: it
	// is given away by the collector's select's store-send case, so the
	// collector consumes completions and results while the pool is busy
	// instead of blocking on the send outside the select. It is owned by
	// the collector goroutine alone (pop, hold, send, and abandonment all
	// run there, as does the drain it calls synchronously), so it needs no
	// lock; nil-with-nil-channel keeps the send case permanently unready
	// while nothing waits, so no empty send can fire.
	holdingDispatch *BlockResult

	// episodeRollbacks increments per executed rollback in the current
	// episode sequence - consecutive rollbacks without an intervening
	// successful commit, since every commit resets it - and backs the
	// rollback-loop guards.
	episodeRollbacks atomic.Int64

	// Run-scoped continuity state for the parentHash check, guarded by
	// pendingMu and reset by processBlocksOnce at each run start. The check
	// runs on FETCHED hashes in pop order, so lastCheckedNum/lastCheckedHash
	// always describe the last popped item (the seed for the run's first
	// item) and never depend on store outcomes.
	runStartBlock   int64
	seedHash        string
	lastCheckedNum  int64
	lastCheckedHash string
	committedHashes map[int64]string
	rollbackPoint   *rollbackPoint
	episode         *reorgEpisode
	fatalErr        error
	// heightRollbacks counts consecutive rollbacks per restart height; any
	// successful commit resets it. Guarded by pendingMu.
	heightRollbacks map[int64]int
	dispatchedUpTo  int64
	runCancel       context.CancelCauseFunc

	// completions apply in height order through this cursor and reorder
	// buffer: pool stores finish out of order, so the collector holds
	// completions until the apply cursor reaches their height. Heights
	// that failed fetch or convert are never dispatched to the pool, so
	// they can never produce a completion; applyHoles records them so the
	// cursor skips them instead of stranding on one forever.
	applyCursor        int64
	reorderCompletions map[int64]storeCompletion
	applyHoles         map[int64]struct{}

	// configuredStart is the lowest height any ProcessBlocks call started
	// from: nothing is ever stored below it, so the reorg walk-back treats
	// the first empty descended height under it as the verification boundary
	// (empties above it may be failed-store holes and are descended past, so
	// the restart above the resolved fork point re-fills them). MaxInt64
	// until the first call pins the actual bound.
	configuredStart int64

	metrics processorMetrics
}

// NewConcurrentBlockProcessor creates a new concurrent processor. The
// reorgHandler is the rollback primitive consumed by the episode procedure
// (walk-back queries and the rollback itself); it may be nil, in which case
// a detected reorg stops indexing with an error instead of resolving it (no
// seed query, gap fallback, or walk-back is possible without it).
func NewConcurrentBlockProcessor(
	fetcher chains.Fetcher,
	converter chains.Converter,
	blockHandler BlockStorer,
	reorgHandler defra.ReorgHandler,
	workers int,
	blocksPerMinute int,
) *ConcurrentBlockProcessor {
	meter := otel.Meter("github.com/shinzonetwork/shinzo-generator-client/pkg/indexer")
	blocks, err := meter.Int64Counter("shinzo.generator.blocks",
		metric.WithDescription("Blocks leaving the ordered commit queue, by result."),
		metric.WithUnit("{block}"),
	)
	if err != nil {
		otel.Handle(err)
	}
	storeDuration, err := meter.Float64Histogram("shinzo.generator.store.duration",
		metric.WithDescription("Duration of one block store attempt, by result."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.01, 0.025, 0.05, 0.075, 0.1, 0.15, 0.2, 0.3, 0.4, 0.5, 0.75, 1, 2.5, 5, 10), //nolint:mnd
	)
	if err != nil {
		otel.Handle(err)
	}
	reorgs, err := meter.Int64Counter("shinzo.generator.reorgs",
		metric.WithDescription("Reorgs confirmed by the indexer."),
		metric.WithUnit("{reorg}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	return &ConcurrentBlockProcessor{
		fetcher:         fetcher,
		converter:       converter,
		blockHandler:    blockHandler,
		reorgHandler:    reorgHandler,
		workers:         workers,
		blocksPerMinute: blocksPerMinute,
		resultChan:      make(chan *BlockResult, workers*DefaultWorkersAhead),
		pending:         make(map[int64]*BlockResult),
		configuredStart: math.MaxInt64,
		metrics: processorMetrics{
			blocks:        blocks,
			storeDuration: storeDuration,
			reorgs:        reorgs,
			success:       metric.WithAttributeSet(attribute.NewSet(attribute.String("result", "success"))),
			failure:       metric.WithAttributeSet(attribute.NewSet(attribute.String("result", "error"))),
		},
	}
}

// ProcessBlocks dispatches blocks to fetch/convert workers, checks results
// in pop order, and stores the checked blocks through a bounded pool. Nothing
// is stored before the parentHash continuity check passes: the checked item
// that carries a detected mismatch (and everything the chain serves past it)
// is held back, waits for the new chain to confirm during the episode
// procedure, walks back by hash to the fork point, and hands the orphan
// range to the rollback below. Rollback failure, walk-back failure,
// rollback-loop guard trips, and a re-store refused because a soft-deleted
// content-derived docID cannot be re-added are all fatal: the run stops
// loudly instead of leaving silently un-indexable heights behind.
//
// The onBlockProcessed callback contract: it fires exactly once per
// successfully committed height, in strict height order, always on one
// goroutine (the result collector). Heights whose fetch or convert failed
// are never stored, so they fire no callback in the run that failed them; a
// later run that re-fills them fires it then, and the order stays strict with
// a gap exactly at the unstored height. Heights rolled back during the run
// fire no second callback until the restart re-commits them. It runs outside
// the processor's internal lock, so it may take its own locks, but it must
// not call ProcessBlocks (or otherwise start a run) re-entrantly, and it must
// return promptly: it serializes the ordered apply, so a slow callback
// throttles indexing.
func (p *ConcurrentBlockProcessor) ProcessBlocks(
	ctx context.Context,
	startBlock int64,
	onBlockProcessed func(blockNum int64),
) error {
	from := startBlock
	// Pin the walk-back's lower bound: nothing is ever stored below the start
	// height this processor was invoked with, so a descended empty height
	// below it ends the walk-back instead of being descended as a hole. A
	// re-invocation on the same processor can only lower the bound.
	if startBlock < p.configuredStart {
		p.configuredStart = startBlock
	}
	seed, err := p.resolveSeed(ctx, from)
	if err != nil {
		return err
	}

	p.episodeRollbacks.Store(0)
	p.pendingMu.Lock()
	p.heightRollbacks = make(map[int64]int)
	p.pendingMu.Unlock()

	for {
		rb, err := p.processBlocksOnce(ctx, from, seed, onBlockProcessed)
		if fatalErr := p.fatalFailure(); fatalErr != nil {
			return fatalErr
		}
		if rb == nil {
			// A nil rollbackPoint means the run was cancelled for a reason
			// other than reorg detection; the error is always non-nil then.
			return err
		}

		restart := rb.forkPoint + 1
		rollbacks := p.episodeRollbacks.Add(1)
		p.pendingMu.Lock()
		p.heightRollbacks[restart]++
		heightCount := p.heightRollbacks[restart]
		p.pendingMu.Unlock()
		if rollbacks > maxRollbacksPerRun {
			return fmt.Errorf("rollback guard: %d rollbacks exceed the per-run limit of %d",
				rollbacks, maxRollbacksPerRun) //nolint:err113
		}
		if heightCount > maxRollbacksPerHeight {
			return fmt.Errorf("rollback guard: height %d rolled back %d times, exceeding the limit of %d - the RPC node may persistently serve forked data",
				restart, heightCount, maxRollbacksPerHeight) //nolint:err113
		}

		if p.reorgHandler == nil {
			return fmt.Errorf("reorg detected at height %d but no rollback handler is configured", restart) //nolint:err113
		}

		logger.Sugar.Warnf("Rolling back orphan blocks [%d, %d] after reorg detection; restarting from %d",
			restart, rb.oldTip, restart)
		// The verified new-chain hashes go in as an explicit parameter: the
		// roller deletes strictly by height and hash, so the rollback fails
		// safe whenever a height carries no verified hash.
		if err := p.reorgHandler.RollbackBlocks(ctx, restart, rb.oldTip, rb.chainHashes); err != nil {
			return fmt.Errorf("rollback blocks [%d, %d]: %w (partial rollback followed by re-indexing would leave duplicate heights; indexing stopped)",
				restart, rb.oldTip, err) //nolint:err113
		}

		from = restart
		seed, err = p.resolveSeed(ctx, from)
		if err != nil {
			return err
		}
	}
}

// fatalFailure returns the fatal error the run recorded, if any. It is only
// meaningful after a run has fully drained (the collector writes it before
// cancelling).
func (p *ConcurrentBlockProcessor) fatalFailure() error {
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	return p.fatalErr
}

// failRun records a fatal run error (the first one wins) and cancels the run
// context: dispatch exits on the cancellation, the collector drains without
// committing, and ProcessBlocks returns the recorded error. The cancel cause
// is only an exit sentinel - the reason travels in fatalErr.
func (p *ConcurrentBlockProcessor) failRun(err error) {
	p.pendingMu.Lock()
	if p.fatalErr == nil {
		p.fatalErr = err
	}
	p.pendingMu.Unlock()
	p.runCancel(errReorgDetected)
}

// resolveSeed returns the hash the continuity check should expect as the
// parent of the first block of a run starting at startBlock. The seed comes
// from the previous run's committed history when available (the predecessor
// was committed in-run, possibly before a rollback purged it), otherwise
// from the stored block at startBlock-1 via the reorgHandler; an empty seed
// means the predecessor is unknown and its continuity check is skipped.
func (p *ConcurrentBlockProcessor) resolveSeed(ctx context.Context, startBlock int64) (string, error) {
	if startBlock <= 0 {
		return "", nil
	}

	p.pendingMu.Lock()
	seed := p.committedHashes[startBlock-1]
	p.pendingMu.Unlock()
	if seed != "" || p.reorgHandler == nil {
		return seed, nil
	}

	seed, err := p.reorgHandler.GetStoredBlockHash(ctx, startBlock-1)
	if err != nil {
		return "", fmt.Errorf("query seed hash for block %d: %w", startBlock-1, err) //nolint:err113
	}
	return seed, nil
}

// processBlocksOnce runs one dispatch/collect lifecycle from startBlock. It
// returns a rollbackPoint when the episode procedure resolved a reorg (the
// run-cause error is then the reorg sentinel and may be ignored); with a nil
// rollbackPoint the run ended by cancellation - a fatal run error is
// returned through the processor's fatalErr instead.
func (p *ConcurrentBlockProcessor) processBlocksOnce(
	ctx context.Context,
	startBlock int64,
	seedHash string,
	onBlockProcessed func(blockNum int64),
) (*rollbackPoint, error) {
	runCtx, runCancel := context.WithCancelCause(ctx)
	defer runCancel(nil)

	p.pendingMu.Lock()
	p.nextToCommit = startBlock
	p.runStartBlock = startBlock
	p.seedHash = seedHash
	// The check chain is seeded with the run's seed hash: the first popped
	// item's parent check compares against it (an empty seed skips).
	p.lastCheckedNum = startBlock - 1
	p.lastCheckedHash = seedHash
	p.applyCursor = startBlock
	p.committedHashes = make(map[int64]string)
	p.reorderCompletions = make(map[int64]storeCompletion)
	p.applyHoles = make(map[int64]struct{})
	p.rollbackPoint = nil
	p.episode = nil
	p.fatalErr = nil
	// Collector-only state: the previous run's collector exited inside
	// shutdown (collectWg.Wait), so resetting here cannot race it.
	p.holdingDispatch = nil
	p.dispatchedUpTo = startBlock - 1
	p.pending = make(map[int64]*BlockResult)
	// Fresh channels (and a fresh pool) per run: shutdown closes the previous
	// ones, and a rollback restart runs processBlocksOnce again.
	p.resultChan = make(chan *BlockResult, p.workers*DefaultWorkersAhead)
	p.storeChan = make(chan *BlockResult, p.workers*DefaultWorkersAhead)
	// The completion buffer keeps a pool worker's completion send
	// non-blocking through the collector's brief stretches without a
	// receive (the synchronous pop-flow work between selects): outstanding
	// completions are all queued dispatches plus one per pool worker. It
	// does not carry the drain's termination - the drain consumes while it
	// waits, so a full buffer can never hold a worker's Done hostage.
	p.completionChan = make(chan storeCompletion, p.workers*(DefaultWorkersAhead+1))
	p.runCancel = runCancel
	p.pendingMu.Unlock()

	workChan, wg, storeWg, collectWg := p.startWorkers(runCtx, onBlockProcessed)

	shutdown := func() {
		close(workChan)
		wg.Wait()
		// No results are produced any more; tell the collector. The fetch
		// workers stop sending at cancellation through their own ctx select,
		// and wg.Wait settled them above, so no send can follow the close.
		close(p.resultChan)
		// The store channel is not closed here: the collector is its only
		// sender, and it closes the channel itself on cancellation. A close
		// from this side would race the collector's dispatch send; the
		// collector's close cannot, because close and send share one
		// goroutine and every post-cancellation dispatch is abandoned before
		// the select. This Wait therefore settles only after the collector
		// fired its cancellation case, which the run's always-cancelled
		// context guarantees.
		storeWg.Wait()
		// Pool workers drain the store channel before exiting, so completions
		// stop being sent once storeWg is settled; closing the completion
		// channel then lets the collector drain the tail and exit.
		close(p.completionChan)
		collectWg.Wait()
		p.signWg.Wait()
	}

	err := p.dispatchLoop(runCtx, startBlock, workChan, shutdown)

	// dispatchLoop only exits by run-context cancellation, so err is always
	// non-nil here; the rollbackPoint (set by the episode procedure before
	// cancellation) tells the caller whether the cancellation was a reorg.
	p.pendingMu.Lock()
	rb := p.rollbackPoint
	if rb != nil {
		rb.oldTip = p.dispatchedUpTo
	}
	p.pendingMu.Unlock()
	return rb, err
}

// startWorkers launches fetch/convert workers, the bounded store pool (the
// only callers of Store), and the result-collection goroutine that also
// consumes pool completions.
func (p *ConcurrentBlockProcessor) startWorkers(ctx context.Context, onBlockProcessed func(blockNum int64)) (chan int64, *sync.WaitGroup, *sync.WaitGroup, *sync.WaitGroup) {
	workChan := make(chan int64, p.workers*DefaultWorkersAhead)

	var wg sync.WaitGroup
	for range p.workers {
		wg.Go(func() {
			for blockNum := range workChan {
				result := p.fetchAndConvertBlock(ctx, blockNum)
				select {
				case p.resultChan <- result:
				case <-ctx.Done():
					return
				}
			}
		})
	}

	var storeWg sync.WaitGroup
	for range p.workers {
		storeWg.Go(func() {
			for item := range p.storeChan {
				p.runStoreTask(ctx, item)
			}
		})
	}

	var collectWg sync.WaitGroup
	collectWg.Go(func() {
		p.collectResults(ctx, onBlockProcessed)
	})

	return workChan, &wg, &storeWg, &collectWg
}

// runStoreTask is the pool worker path for one checked item: run
// storeWithRetry, run the failure handler's stale-orphan verification on a
// failure (so the walk-back sees a quiescent DB state later), and report the
// completion back to the collector's ordered apply. It runs only on pool
// goroutines - the collector never stores.
func (p *ConcurrentBlockProcessor) runStoreTask(ctx context.Context, item *BlockResult) {
	computed := p.storeWithRetry(ctx, item.BlockNum, item.Converted)
	completion := storeCompletion{item: item, computed: computed}
	if computed.Error != nil && !errors.IsErrDocumentDeleted(computed.Error) && p.reorgHandler != nil {
		completion.stale = p.detectStaleOrphan(ctx, item)
	}
	// The completion send precedes the in-flight drop: a drain waiting on
	// the group therefore resumes only after this completion already sits
	// in the buffer, so no report can be lost between the counter and the
	// channel.
	p.completionChan <- completion
	p.inFlightWg.Done()
}

// detectStaleOrphan is the pool-side failure handler: for a store that
// failed at height H it queries the stored hash at H and compares it
// against the failed item's verified fetched hash. A disagreement re-opens
// the old stale-orphan detection - the stored orphan is now itself the
// mismatching height (rollbackFrom H), so the walk-back descends from H
// once the in-flight stores drain. An empty height warns the hole (the
// walk-back descends past it and the restart above the fork re-fills it);
// matching content, an unusable queried hash, or a query failure leaves the
// height unverifiable with a warning. The queried height is stable: H's
// store definitively failed, and no other in-flight store writes H.
func (p *ConcurrentBlockProcessor) detectStaleOrphan(ctx context.Context, item *BlockResult) *reorgSignal {
	if item.Hash == "" {
		logger.Sugar.Warnf("Block %d: store failed and the fetched hash was unusable; the height stays unverifiable", item.BlockNum)
		return nil
	}
	stored, err := p.reorgHandler.GetStoredBlockHash(ctx, item.BlockNum)
	if err != nil {
		logger.Sugar.Warnf("Block %d: stored-hash query failed for the failure handler at %d (%v); the height stays unverifiable",
			item.BlockNum, item.BlockNum, err)
		return nil
	}
	if stored == "" {
		logger.Sugar.Warnf("Block %d: store failed leaving nothing stored; height %d is a hole the restart will re-fill", item.BlockNum, item.BlockNum)
		return nil
	}
	if stored == item.Hash {
		logger.Sugar.Warnf("Block %d: store failed but the matching content is already stored; height %d keeps its content (the next run finds it idempotent)", item.BlockNum, item.BlockNum)
		return nil
	}
	return &reorgSignal{
		rollbackFrom: item.BlockNum,
		orphanHash:   stored,
		chainHash:    item.Hash,
	}
}

// collectResults runs the single collector loop: one popped result through
// the ordered check per turn, one result arrival or one pool store
// completion per select, and a checked item waiting for pool capacity
// inside the select itself - so completions keep draining even when the
// pool is backed up (a long check pass can never make the collector stop
// consuming them) and even while an episode parks results. It returns once
// both sources have closed (shutdown).
func (p *ConcurrentBlockProcessor) collectResults(ctx context.Context, onBlockProcessed func(blockNum int64)) {
	resultsClosed := false
	completionsClosed := false

	// done lets the cancellation case fire once: a nil channel receive
	// blocks forever, so afterwards the select blocks on the channels until
	// shutdown closes them instead of busy-spinning on an always-ready case.
	done := ctx.Done()

	for {
		if p.holdingDispatch == nil {
			next, ok := p.popPending(ctx)
			if ok && next != nil {
				if p.episodeActive() {
					// Confirmation wait: the popped item is held back (never
					// stored) and resolved by the episode rules.
					p.resolveEpisodeItem(ctx, next, onBlockProcessed)
				} else if item := p.checkedForDispatch(ctx, next); item != nil {
					// The in-flight registration is taken here, before the
					// item can reach a pool worker: the send's rendezvous
					// lets the worker finish (a cancelled store reports
					// instantly) far ahead of this goroutine's next
					// instruction, so any registration attempted after the
					// send would race the worker's drop and could go
					// negative. Registration and abandonment are both on
					// this goroutine, so no Done can preempt its Add.
					p.inFlightWg.Add(1)
					p.holdingDispatch = item
				}
			}
		}

		// Once the run is leaving this shape (episode open, rollback point
		// recorded, or cancellation since the check) a waiting dispatch is
		// abandoned: the hold-back covers it too, and the restart re-indexes
		// the height. The registration was taken at creation, so the
		// abandonment drops it before any worker could pair a Done with it.
		if p.holdingDispatch != nil {
			p.pendingMu.Lock()
			held := p.episode != nil || p.rollbackPoint != nil
			p.pendingMu.Unlock()
			if held || ctx.Err() != nil {
				p.inFlightWg.Done()
				p.holdingDispatch = nil
			}
		}

		var storeSender chan *BlockResult
		if p.holdingDispatch != nil {
			storeSender = p.storeChan
		}
		select {
		case result, ok := <-p.resultChan:
			if !ok {
				resultsClosed = true
			} else {
				p.pendingMu.Lock()
				p.pending[result.BlockNum] = result
				p.pendingMu.Unlock()
			}
		case completion, ok := <-p.completionChan:
			if !ok {
				completionsClosed = true
			} else {
				p.applyCompletion(ctx, completion, onBlockProcessed, false)
			}
		case storeSender <- p.holdingDispatch:
			p.holdingDispatch = nil
		case <-done:
			// The collector is storeChan's only sender, so it owns the
			// close: running it here (rather than in shutdown) puts close
			// and send on one goroutine, and the abandon check above nils
			// every post-cancellation dispatch before the select, so no
			// send can follow the close. The WaitGroup registration pairs
			// with the abandonment drop: cancellation raced the check and
			// the pool never received the item.
			if p.holdingDispatch != nil {
				p.inFlightWg.Done()
			}
			p.holdingDispatch = nil
			done = nil // fire once; afterwards block on the channels until shutdown closes them
			close(p.storeChan)
		}
		if resultsClosed && completionsClosed {
			return
		}
	}
}

// checkedForDispatch runs one popped result through the ordered check and,
// when the checks passed, hands it back as the item to dispatch to the
// store pool. A nil return means the item resolved inside the call: a
// failed fetch/convert (hole), or a detected mismatch that opened the
// confirmation-wait episode instead of storing.
func (p *ConcurrentBlockProcessor) checkedForDispatch(ctx context.Context, next *BlockResult) *BlockResult {
	// A failed fetch/convert result is never stored (the old hole path):
	// warn, mark the check chain unusable for this height, and let the
	// next usable pop adopt the gap verification for the hole. The height
	// can never produce a store completion, so the apply cursor must be
	// able to skip it: record the hole for the ordered collect. Dispatch
	// continues above the hole (the pop cursor already moved past it at
	// pop time and the throttle keys off it), so the next completion to
	// arrive pulls the cursor past the hole and drains parked successors.
	if !next.Success {
		p.metrics.blocks.Add(ctx, 1, p.metrics.failure)
		logger.Sugar.Warnf("Block %d failed: %v", next.BlockNum, next.Error)
		p.pendingMu.Lock()
		p.lastCheckedNum = next.BlockNum
		p.lastCheckedHash = ""
		p.applyHoles[next.BlockNum] = struct{}{}
		p.pendingMu.Unlock()
		return nil
	}

	if signal, mismatch := p.pendingContinuitySignal(next); mismatch {
		p.startReorgEpisode(signal, next)
		return nil
	}

	if signal, mismatch := p.poppedGapSignal(ctx, next); mismatch {
		p.startReorgEpisode(signal, next)
		return nil
	}

	// The check passed: mark the check chain with this item's fetched hash.
	p.pendingMu.Lock()
	p.lastCheckedNum = next.BlockNum
	p.lastCheckedHash = next.Hash
	p.pendingMu.Unlock()
	return next
}

// pendingContinuitySignal applies the pure parentHash detection rules to a
// successful result N, in the collector's pop order: against the previous
// popped item's fetched hash (the check predecessor) and, at the run seam,
// against the run's seed hash. The check consumes only fetched hashes -
// which are the same value the block carries into the store - so store
// outcomes never gate the check.
func (p *ConcurrentBlockProcessor) pendingContinuitySignal(next *BlockResult) (reorgSignal, bool) {
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	return continuitySignal(p.runStartBlock, p.seedHash, p.lastCheckedNum, p.lastCheckedHash, next)
}

// popPending pops the next ordered pending result and advances nextToCommit.
// ok is false when nothing is waiting, ending the drain pass. With ok=true
// and a nil result the item was drained without commit: a rollback point is
// recorded, or the run was cancelled for a reason other than reorg
// detection - nothing commits and no continuity state advances past the
// rollback point.
func (p *ConcurrentBlockProcessor) popPending(ctx context.Context) (*BlockResult, bool) {
	p.pendingMu.Lock()
	next, ok := p.pending[p.nextToCommit]
	if !ok {
		p.pendingMu.Unlock()
		return nil, false
	}
	delete(p.pending, p.nextToCommit)
	p.nextToCommit++

	if p.rollbackPoint != nil || ctx.Err() != nil {
		p.pendingMu.Unlock()
		return nil, true
	}
	p.pendingMu.Unlock()
	return next, true
}

// episodeActive reports whether a confirmation wait is in progress.
func (p *ConcurrentBlockProcessor) episodeActive() bool {
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	return p.episode != nil
}

// poppedGapSignal adopts the old gap-fallback rule for the one in-run hole
// shape the check predecessor cannot cover: the previous popped item was
// unusable (a failed fetch/convert left no hash to chain from). The stored
// hash at N-1 is compared against N's parentHash; a disagreement opens the
// episode, empties above the seed skip silently (the walk-back descends past
// them, and a failed store's hole is re-filled by the restart). The check
// cursor cannot have been escaped: every popped item's predecessor was
// popped, so this query fires only at the usable pop right after an unusable
// one. The stored-hash query runs without pendingMu; a query failure leaves
// the height unverifiable (warn logged), an empty stored or parent hash
// skips.
func (p *ConcurrentBlockProcessor) poppedGapSignal(ctx context.Context, next *BlockResult) (reorgSignal, bool) {
	p.pendingMu.Lock()
	needGap := next.ParentHash != "" && p.reorgHandler != nil &&
		next.BlockNum != p.runStartBlock &&
		p.lastCheckedNum == next.BlockNum-1 && p.lastCheckedHash == ""
	p.pendingMu.Unlock()
	if !needGap {
		return reorgSignal{}, false
	}

	gapHeight := next.BlockNum - 1
	stored, qErr := p.reorgHandler.GetStoredBlockHash(ctx, gapHeight)
	if qErr != nil {
		logger.Sugar.Warnf("Block %d: stored-hash query failed for the gap fallback at %d (%v); the height stays unverifiable",
			next.BlockNum, gapHeight, qErr)
		return reorgSignal{}, false
	}
	if ctx.Err() != nil {
		return reorgSignal{}, false
	}
	return gapFallbackSignal(stored, next)
}

// applyCompletion parks one pool completion in the reorder buffer and
// applies every completion the cursor can now reach: strictly in height
// order so onBlockProcessed fires in pop order no matter which pool store
// finished first. Parking first keeps a single cursor-advance path, so the
// batch is identical for a completion that lands at the cursor and one that
// completes a parked prefix, and recorded hole heights (failed fetch or
// convert, unstored) are skipped by the same pass.
func (p *ConcurrentBlockProcessor) applyCompletion(ctx context.Context, completion storeCompletion, onBlockProcessed func(blockNum int64), suppress bool) {
	p.pendingMu.Lock()
	p.reorderCompletions[completion.item.BlockNum] = completion
	batch := p.collectReadyCompletionsLocked()
	p.pendingMu.Unlock()

	p.applyBatch(ctx, batch, onBlockProcessed, suppress)
}

// collectReadyCompletionsLocked gathers the ordered batch the apply cursor
// can reach: each recorded hole at the cursor advances it without a
// completion, and each parked completion at the cursor applies next. Two
// invariants keep this pass total: a hole records only at heights the cursor
// sits at or below (the cursor can pass a height only through its completion
// or its recorded hole, and a fetch-failed height has neither), and a hole
// height never carries a parked completion (completions come only from
// dispatched items and the hole height was never dispatched in this run).
// The caller holds pendingMu.
func (p *ConcurrentBlockProcessor) collectReadyCompletionsLocked() []storeCompletion {
	var batch []storeCompletion
	for {
		if _, hole := p.applyHoles[p.applyCursor]; hole {
			delete(p.applyHoles, p.applyCursor)
			p.applyCursor++
			continue
		}
		next, ok := p.reorderCompletions[p.applyCursor]
		if !ok {
			return batch
		}
		delete(p.reorderCompletions, p.applyCursor)
		batch = append(batch, next)
		p.applyCursor++
	}
}

// applyBatch applies the ordered batch: an episode or fatal error recorded
// by an earlier iteration (a stale-pinned episode finalizing mid-batch) puts
// the rest of the batch inside the range about to be rolled back, so it must
// not commit or fire callbacks.
func (p *ConcurrentBlockProcessor) applyBatch(ctx context.Context, batch []storeCompletion, onBlockProcessed func(blockNum int64), suppress bool) {
	for _, c := range batch {
		p.pendingMu.Lock()
		ended := p.rollbackPoint != nil || p.fatalErr != nil
		p.pendingMu.Unlock()
		p.applyOneCompletion(ctx, c, onBlockProcessed, suppress || ended)
	}
}

// applyOneCompletion applies one ordered completion. Failed stores keep the
// old failure rules: the deleted-docID refusal stops the run fatally, any
// other failure is a hole - and a pool-verified stale orphan at the failed
// height opens the stale-pinned episode, which finalizes (drain + walk-back)
// without waiting for an in-order successor because the stored-versus-fetched
// disagreement is already two-sided evidence. Suppressed applies (during a
// stale-pinned drain) consume the completion but book-, callback-, and
// guard-write nothing: their heights sit in the rollback range the episode
// is about to hand back, so the restart re-commits them and fires the
// callbacks in height order.
func (p *ConcurrentBlockProcessor) applyOneCompletion(ctx context.Context, completion storeCompletion, onBlockProcessed func(blockNum int64), suppress bool) {
	item := completion.item
	computed := completion.computed
	if computed.Error != nil {
		p.metrics.blocks.Add(ctx, 1, p.metrics.failure)
		// DefraDB refuses to re-create a document whose content-derived
		// docID was soft-deleted (an A->B->A sequence re-fetches the
		// byte-identical A content after the rollback deleted it). Skipping
		// that refusal silently would leave the height permanently
		// un-indexable: the deleted docs can never be re-added until the
		// pruner hard-purges them, so the run stops loudly and the refusal
		// surfaces as the indexing error instead. The async failure path
		// preserves the fatal classification.
		if errors.IsErrDocumentDeleted(computed.Error) {
			p.failRun(fmt.Errorf("block %d store refused: %w (a soft-deleted content-derived docID cannot be re-added; the deleted documents must be hard-purged by the pruner before this height can be re-indexed; indexing stopped)", //nolint:err113
				item.BlockNum, computed.Error))
			return
		}
		logger.Sugar.Warnf("Block %d failed: %v", item.BlockNum, computed.Error)
		if completion.stale != nil {
			p.pendingMu.Lock()
			ep := p.episode
			ended := p.rollbackPoint != nil || p.fatalErr != nil
			p.pendingMu.Unlock()
			if ended {
				// An earlier batch item or the drain before it already
				// resolved an episode or failed the run: this height sits in
				// the resolved rollback range, so the restart re-indexes it
				// and no second episode may open on top.
				return
			}
			if ep == nil {
				p.openStaleEpisode(ctx, completion, onBlockProcessed)
				return
			}
			if ep.stalePinned && completion.item.BlockNum > ep.staleTopHeight {
				// A drained failure's stale verification raises the
				// walk-back anchor: the descent must start above every
				// verified stale height so all of them are visited.
				p.pendingMu.Lock()
				ep.staleTopHeight = completion.item.BlockNum
				ep.staleTopHash = completion.item.Hash
				p.pendingMu.Unlock()
			}
			// Any other active episode's walk-back already descends through
			// every height between the fork and its base: a lower stale
			// verify needs no opener.
		}
		return
	}

	if suppress {
		// A stale-pinned drain consumed this completion: the store landed on
		// the checked new chain, but its height sits in the rollback range
		// the episode is about to hand back, so no callback or commit
		// bookkeeping may fire for it in this run.
		return
	}
	p.applyCommit(ctx, item, computed, onBlockProcessed)
}

// drainInFlightStores waits for every store the pool accepted - still
// consuming completions while it waits, then draining the channel to empty -
// resolving the whole reorder buffer before the walk-back starts. Nothing
// can arrive after the wait: the collector is the
// only store dispatcher and it is inside this drain. Suppression applies when
// the caller is finalizing a stale-pinned episode: those completions come from
// stores dispatched before the hold-back opened, they land (they cannot be
// recalled) but must not apply callbacks or commit bookkeeping inside the
// run that is about to roll their heights back. A drained failure's own
// stale verification joins the episode's stale walk-back anchor. A pop-time
// episode's drain applies fully: its in-flight stores were dispatched before
// the mismatch height, so their completions are ordinary pre-mismatch
// commits and fire the callback in height order.
func (p *ConcurrentBlockProcessor) drainInFlightStores(ctx context.Context, ep *reorgEpisode, onBlockProcessed func(blockNum int64)) {
	suppress := ep.stalePinned

	// A waiting dispatch's registration can only be released on the
	// collector: the item has not reached the pool, so no worker will pair
	// a Done with it, and the release sites all sit in the collector's
	// select, which is blocked while this drain runs on the collector. The
	// episode that opened may therefore have been triggered by a buffered
	// stale completion (a gap-filler surfaced it) rather than by the
	// incoming one, so the entry drop is unconditional here: whatever the
	// opening path, no unsent registration may enter the wait, and the
	// episode's rollback re-indexes the dropped height exactly like the
	// hold-back. The confirmation-wait episode opens at pop time when
	// nothing can be held, so the drop is a no-op there.
	if p.holdingDispatch != nil {
		p.inFlightWg.Done()
		p.holdingDispatch = nil
	}

	// Registrations are taken when an item starts waiting for the pool, and
	// dropped either by the worker (after its completion send) or by the
	// collector (at abandonment, or by the entry drop above). After the
	// drop, the wait's remaining registrations are all handed-to-pool
	// stores whose completions will arrive.
	//
	// The wait cannot lean on the completion buffer alone: the collector's
	// select hands the pool further stores while earlier reports stay
	// buffered (the send case can win without a receive), so completions
	// already buffered plus completions still outstanding can exceed the
	// buffer at wait entry, and a worker whose send blocks there would never
	// reach its Done. The drain therefore consumes while it waits: every
	// worker send finds a receiver, the send always precedes the Done, so
	// every registration drops and the group settles under any buffer state,
	// including cancellation, whose fast-failed stores still report error
	// completions into this loop.
	groupSettled := make(chan struct{})
	go func() {
		p.inFlightWg.Wait()
		close(groupSettled)
	}()

waiting:
	for {
		select {
		case <-groupSettled:
			break waiting
		case completion, ok := <-p.completionChan:
			if !ok {
				// Shutdown closed the channel: the close follows
				// storeWg.Wait, so the group is settling too; the tail
				// drain below sees the closed channel and returns.
				<-groupSettled
				break waiting
			}
			p.applyCompletion(ctx, completion, onBlockProcessed, suppress)
		}
	}

	// Nothing new can arrive: the collector is the only store dispatcher
	// and it is inside this drain. Consume what landed, in cursor order,
	// until the channel is empty or shutdown closed it.
	for {
		select {
		case completion, ok := <-p.completionChan:
			if !ok {
				return
			}
			p.applyCompletion(ctx, completion, onBlockProcessed, suppress)
		default:
			return
		}
	}
}

// openStaleEpisode opens the stale-pinned episode for one pool-verified
// stale orphan and finalizes it immediately: the failure handler already
// verified the stored hash against the failed item's fetched hash, so the
// walk-back descent from the stale height is the confirmation. The drain
// inside finalize continues collecting further stale verifications before
// the descent starts.
func (p *ConcurrentBlockProcessor) openStaleEpisode(ctx context.Context, completion storeCompletion, onBlockProcessed func(blockNum int64)) {
	item := completion.item
	logger.Sugar.Warnf(
		"Reorg detected: stored block %d (hash %s) disagrees with chain-side block %d (hash %s); holding from height %d",
		item.BlockNum, completion.stale.orphanHash, item.BlockNum, item.Hash, item.BlockNum+1,
	)

	p.pendingMu.Lock()
	p.episode = &reorgEpisode{
		stalePinned:    true,
		baseHeight:     item.BlockNum,
		staleTopHeight: item.BlockNum,
		staleTopHash:   item.Hash,
	}
	p.pendingMu.Unlock()
	p.finalizeEpisode(ctx, p.episode, nil, onBlockProcessed)
}

// applyCommit applies a successful pool store to the ordered commit state
// and fires the completion callback: commit bookkeeping, history trim, and
// the guard reset that ends a consecutive-rollback sequence on progress.
// The caller runs it when the completion applies (in height order via the
// reorder buffer); completions applying during a parked episode wait are
// genuine intervening progress - a pre-mismatch store landing on the
// checked new chain - so the guard reset applies to them too. The callback
// fires after pendingMu is released: it belongs to the ordered apply's
// sequence (applyCommit runs only on the single collector goroutine, so
// ordering and serialization are unchanged), and a callback can no longer
// re-enter a pendingMu path and self-deadlock.
func (p *ConcurrentBlockProcessor) applyCommit(ctx context.Context, item *BlockResult, computed *BlockResult, onBlockProcessed func(blockNum int64)) {
	p.metrics.blocks.Add(ctx, 1, p.metrics.success)
	p.pendingMu.Lock()
	if computed.BlockID != "" {
		logger.Sugar.Infof("Committed block %d (ID: %s)", item.BlockNum, computed.BlockID)
	} else {
		logger.Sugar.Infof("Committed block %d", item.BlockNum)
	}
	p.committedHashes[item.BlockNum] = item.Hash
	for height := range p.committedHashes {
		if height < p.nextToCommit-2 {
			delete(p.committedHashes, height)
		}
	}
	p.episodeRollbacks.Store(0)
	p.heightRollbacks = make(map[int64]int)
	p.pendingMu.Unlock()

	if onBlockProcessed != nil {
		onBlockProcessed(item.BlockNum)
	}
}

// startReorgEpisode parks the mismatching result (never stored) and opens
// the confirmation-wait episode: everything from the mismatch height onward
// is held back until the new chain is confirmed and the walk-back resolves
// the fork point.
func (p *ConcurrentBlockProcessor) startReorgEpisode(signal reorgSignal, parked *BlockResult) {
	logger.Sugar.Warnf(
		"Reorg detected: stored block %d (hash %s) disagrees with chain-side block %d (hash %s); holding block %d back",
		signal.rollbackFrom, signal.orphanHash, parked.BlockNum, signal.chainHash, parked.BlockNum,
	)
	p.pendingMu.Lock()
	p.episode = &reorgEpisode{
		baseHeight:     parked.BlockNum,
		baseParentHash: signal.chainHash,
		lastParkedHash: parked.Hash,
	}
	p.pendingMu.Unlock()
}

// resolveEpisodeItem applies the confirmation-wait rules to one parked item:
// a chained in-order successor confirms the episode (the walk-back
// finalizes it), a chained item that breaks the verified segment re-anchors
// it on the newest chain, and an unusable item (worker failure or a result
// without usable hashes) is re-fetched by height so the wait can continue
// past the gap. Nothing here is ever stored.
func (p *ConcurrentBlockProcessor) resolveEpisodeItem(ctx context.Context, item *BlockResult, onBlockProcessed func(blockNum int64)) {
	for {
		p.pendingMu.Lock()
		ep := p.episode
		if ep == nil || p.rollbackPoint != nil || ctx.Err() != nil {
			p.pendingMu.Unlock()
			return
		}

		if item.Success && item.Hash != "" && item.ParentHash != "" &&
			item.ParentHash == ep.lastParkedHash {
			confirmed := *item
			p.pendingMu.Unlock()
			p.finalizeEpisode(ctx, ep, &confirmed, onBlockProcessed)
			return
		}

		if item.Success && item.Hash != "" && item.ParentHash != "" {
			// Second disagreement: the chain flipped again while nothing has
			// been deleted yet - re-anchor on the newest observation and
			// keep waiting.
			p.reanchorEpisodeLocked(ep, item)
			fatal := p.fatalErr != nil
			p.pendingMu.Unlock()
			if fatal {
				p.runCancel(errReorgDetected)
			}
			return
		}

		fresh, stop, fatal := p.refillEpisodeGap(ctx, ep, item)
		if fatal {
			p.runCancel(errReorgDetected)
		}
		if stop {
			return
		}
		// The re-fetched content re-enters the same rules as a parked item.
		item = fresh
	}
}

// reanchorEpisodeLocked re-anchors the confirmation wait on the newest chain
// observation: the parked base moves to the newest item and the wait
// continues. The re-anchor counter is bounded; exceeding it fails the
// episode fatally (a node flip-flopping between chains must not park the
// run forever). The caller holds pendingMu.
func (p *ConcurrentBlockProcessor) reanchorEpisodeLocked(ep *reorgEpisode, item *BlockResult) {
	if ep.rewalks >= maxRewalksPerEpisode {
		p.episode = nil
		p.fatalErr = fmt.Errorf("reorg detected at height %d: the confirmation wait re-anchored %d times; the RPC node keeps flip-flopping between chains",
			ep.baseHeight, ep.rewalks) //nolint:err113
		return
	}
	ep.rewalks++
	ep.baseHeight = item.BlockNum
	ep.baseParentHash = item.ParentHash
	ep.lastParkedHash = item.Hash
	logger.Sugar.Warnf("Reorg episode: the chain flipped again; re-anchored at height %d (new-chain hash %s)",
		item.BlockNum, item.ParentHash)
}

// refillEpisodeGap re-fetches an unusable verification point so the
// confirmation wait can continue past the gap. The re-fill counter is
// bounded; exceeding it is fatal. A shutdown mid-wait abandons the episode
// silently (nothing was stored or deleted, so nothing is lost - the next
// run's seed and gap checks re-detect the mismatch), while any other
// re-fetch failure is fatal. Called with pendingMu held; always returns
// with it released.
func (p *ConcurrentBlockProcessor) refillEpisodeGap(ctx context.Context, ep *reorgEpisode, item *BlockResult) (fresh *BlockResult, stop, fatal bool) {
	if ep.gapRefills >= maxGapRefillsPerWait {
		p.episode = nil
		p.fatalErr = fmt.Errorf("reorg detected at height %d: the confirmation wait re-fetched %d unusable verification points",
			ep.baseHeight, ep.gapRefills) //nolint:err113
		p.pendingMu.Unlock()
		return nil, true, true
	}
	ep.gapRefills++
	target, baseHeight := item.BlockNum, ep.baseHeight
	p.pendingMu.Unlock()

	refetched := p.fetchAndConvertBlock(ctx, target)
	if refetched.Error != nil {
		if ctx.Err() != nil {
			return nil, true, false
		}
		p.pendingMu.Lock()
		p.episode = nil
		p.fatalErr = fmt.Errorf("reorg detected at height %d: confirmation wait failed at %d: %w",
			baseHeight, target, refetched.Error) //nolint:err113
		p.pendingMu.Unlock()
		return nil, true, true
	}
	return refetched, false, false
}

// finalizeEpisode runs the walk-back for a confirmed episode and records the
// rollback point the outer ProcessBlocks loop acts on. In-flight stores and
// their completions settle BEFORE the walk-back: the stored-hash queries
// need a quiescent DB, and a store landing after its height was rolled back
// would race the soft-delete. The verified new-chain hashes (the walk-back
// descent, the parked mismatch and the confirming successor - none of which
// the descent saw) travel inside the rollback point, so the rollback handler
// can delete strictly by height and hash. A walk-back failure is fatal
// unless the run context is already cancelled (user shutdown: the episode is
// simply abandoned - the next run's seed and gap checks re-detect the
// mismatch).
func (p *ConcurrentBlockProcessor) finalizeEpisode(ctx context.Context, ep *reorgEpisode, confirmed *BlockResult, onBlockProcessed func(blockNum int64)) {
	p.drainInFlightStores(ctx, ep, onBlockProcessed)

	if ctx.Err() != nil {
		logger.Sugar.Warnf("Reorg episode abandoned during shutdown: %v", ctx.Err())
		p.pendingMu.Lock()
		p.episode = nil
		p.pendingMu.Unlock()
		return
	}

	p.metrics.reorgs.Add(ctx, 1)

	var (
		forkPoint   int64
		chainHashes map[int64]string
		err         error
	)
	if ep.stalePinned {
		// The verified stale orphan anchors the descent at the failed
		// height with the verified fetched hash: a descent above it would
		// match the landed pre-mismatch stores and never reach the orphan,
		// and every lower stale height is visited on the way down, verified
		// against the stored record, and covered by the rollback map.
		forkPoint, chainHashes, err = p.resolveForkPoint(ctx, ep.staleTopHeight, ep.staleTopHash)
	} else {
		logger.Sugar.Infof("Reorg episode confirmed: walking back the new chain from height %d (hash %s)",
			ep.baseHeight-1, ep.baseParentHash)
		forkPoint, chainHashes, err = p.resolveForkPoint(ctx, ep.baseHeight-1, ep.baseParentHash)
	}

	if err != nil && ctx.Err() != nil {
		logger.Sugar.Warnf("Reorg episode abandoned during shutdown: %v", err)
		p.pendingMu.Lock()
		p.episode = nil
		p.pendingMu.Unlock()
		return
	}

	p.pendingMu.Lock()
	p.episode = nil
	if err != nil {
		p.fatalErr = fmt.Errorf("reorg detected at height %d: %w", ep.baseHeight, err) //nolint:err113
	} else {
		if !ep.stalePinned {
			// The confirmation wait verified the parked mismatch height and
			// the chained successor above it; the walk-back itself never saw
			// that segment.
			chainHashes[ep.baseHeight] = ep.lastParkedHash
			chainHashes[confirmed.BlockNum] = confirmed.Hash
		}
		p.rollbackPoint = &rollbackPoint{forkPoint: forkPoint, chainHashes: chainHashes}
	}
	p.pendingMu.Unlock()
	p.runCancel(errReorgDetected)
}

// continuitySignal applies the pure parentHash detection rules to a
// successful result N in pop order: the seed check at the run's first block
// (restart seam) and the continuation check against the previous popped
// item's fetched hash. The check predecessor travels in lastCheckedNum /
// lastCheckedHash - the seed for the run's first item, the previous pop's
// hash afterwards - so the comparison never consults store outcomes. The
// gap fallback for an unusable checked predecessor - the stored hash at N-1
// compared against N's parentHash - needs a stored-hash query and is
// resolved separately by poppedGapSignal.
func continuitySignal(runStartBlock int64, seedHash string, lastCheckedNum int64, lastCheckedHash string, next *BlockResult) (reorgSignal, bool) {
	n := next.BlockNum

	if n == runStartBlock && seedHash != "" &&
		next.ParentHash != "" && next.ParentHash != seedHash {
		return reorgSignal{
			rollbackFrom: n - 1,
			orphanHash:   seedHash,
			chainHash:    next.ParentHash,
		}, true
	}

	if lastCheckedNum == n-1 && lastCheckedHash != "" &&
		next.ParentHash != "" && next.ParentHash != lastCheckedHash {
		return reorgSignal{
			rollbackFrom: n - 1,
			orphanHash:   lastCheckedHash,
			chainHash:    next.ParentHash,
		}, true
	}

	return reorgSignal{}, false
}

// gapFallbackSignal compares the stored hash at N-1 (queried by the caller
// outside pendingMu) against the block's parentHash: a stored hash that
// disagrees with the new parent exposes a reorg adjacent to a failed height,
// including a partial store that left a visible block doc. An empty stored
// hash or an empty parent hash is unverifiable and skips.
func gapFallbackSignal(storedHash string, next *BlockResult) (reorgSignal, bool) {
	if storedHash == "" || next.ParentHash == "" || storedHash == next.ParentHash {
		return reorgSignal{}, false
	}
	return reorgSignal{
		rollbackFrom: next.BlockNum - 1,
		orphanHash:   storedHash,
		chainHash:    next.ParentHash,
	}, true
}

// DispatchFinalizedOnly bounds dispatch at the height the fetcher reports as finalized, so no
// block a reorg can still replace is indexed. Call it before ProcessBlocks.
func (p *ConcurrentBlockProcessor) DispatchFinalizedOnly() {
	p.finalizedOnly = true
}

// waitForFinalized returns once the fetcher reports height as finalized, asking every
// FinalizedPollInterval, or with ctx's error once ctx is done.
func (p *ConcurrentBlockProcessor) waitForFinalized(ctx context.Context, height int64) error {
	for height > p.finalized {
		finalized, err := p.fetcher.FetchFinalizedBlockNumber(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			logger.Sugar.Warnf("Block %d waits for dispatch: finalized height unknown: %v", height, err)
		} else {
			p.finalized = finalized
			if height <= finalized {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(FinalizedPollInterval):
		}
	}
	return nil
}

// dispatchLoop sends block numbers to workChan with optional rate limiting.
func (p *ConcurrentBlockProcessor) dispatchLoop(ctx context.Context, startBlock int64, workChan chan int64, shutdown func()) error {
	var minInterval time.Duration
	if p.blocksPerMinute > 0 {
		minInterval = time.Minute / time.Duration(p.blocksPerMinute)
		logger.Sugar.Infof("Rate limiting enabled: %d blocks/minute (interval: %v)", p.blocksPerMinute, minInterval)
	}

	lastDispatch := time.Now().Add(-minInterval)
	nextBlock := startBlock

	for {
		if minInterval > 0 {
			elapsed := time.Since(lastDispatch)
			if elapsed < minInterval {
				select {
				case <-ctx.Done():
					shutdown()
					return ctx.Err()
				case <-time.After(minInterval - elapsed):
				}
			}
		}

		p.pendingMu.Lock()
		// The dispatch throttle anchors on the check cursor: dispatched
		// fetches may not run more than DefaultWorkersAhead*workers heights
		// ahead of the checked frontier.
		tooFarAhead := nextBlock-p.nextToCommit >= int64(p.workers*DefaultWorkersAhead)
		p.pendingMu.Unlock()

		if tooFarAhead {
			select {
			case <-ctx.Done():
				shutdown()
				return ctx.Err()
			case <-time.After(DispatchThrottleDelay):
				continue
			}
		}

		if p.finalizedOnly {
			if err := p.waitForFinalized(ctx, nextBlock); err != nil {
				shutdown()
				return err
			}
		}

		select {
		case <-ctx.Done():
			shutdown()
			return ctx.Err()
		case workChan <- nextBlock:
			lastDispatch = time.Now()
			p.pendingMu.Lock()
			p.dispatchedUpTo = nextBlock
			p.pendingMu.Unlock()
			nextBlock++
		}
	}
}

// fetchAndConvertBlock fetches and converts one block - the full worker
// path since the store moved into the bounded pool. The block hash and
// parent hash are extracted from the conversion result: the parent hash
// feeds the continuity check, and the conversion result itself is handed to
// the store pool only after that check passed.
func (p *ConcurrentBlockProcessor) fetchAndConvertBlock(ctx context.Context, blockNum int64) *BlockResult {
	raw, err := p.fetchBlockWithRetry(ctx, blockNum)
	if err != nil {
		return &BlockResult{BlockNum: blockNum, Error: err}
	}

	converted, err := p.converter.Convert(ctx, raw)
	if err != nil {
		return &BlockResult{BlockNum: blockNum, Error: fmt.Errorf("convert block: %w", err)}
	}

	return &BlockResult{
		BlockNum:   blockNum,
		Hash:       extractBlockHash(converted.Groups),
		ParentHash: extractBlockParentHash(converted.Groups),
		Converted:  converted,
		// Success marks a fetch+convert pass: the ordered check, the store
		// pool and the episode's confirmation rules treat the content as
		// verified input, not as a completed store.
		Success: true,
	}
}

// fetchBlockWithRetry fetches a block from the fetcher with retry
// classification:
//   - not-found: infinite retry with BlockNotFoundRetryDelay (block may not be mined yet)
//   - other errors: up to MaxRPCRetries with linear backoff (RPCErrorRetryBaseDelay * attempt)
func (p *ConcurrentBlockProcessor) fetchBlockWithRetry(ctx context.Context, blockNum int64) (any, error) {
	otherErrors := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		raw, err := p.fetcher.FetchBlock(ctx, blockNum)
		if err == nil {
			return raw, nil
		}

		if errors.IsErrNotFound(err) {
			logger.Sugar.Infof("Block %d not available yet, waiting...", blockNum)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(BlockNotFoundRetryDelay):
			}
			continue
		}

		otherErrors++
		if otherErrors >= MaxRPCRetries {
			return nil, fmt.Errorf("failed to fetch block %d: %w", blockNum, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(otherErrors) * RPCErrorRetryBaseDelay):
		}
	}
}

// fetchHeaderByHashWithRetry fetches a block header by its content hash for
// the walk-back descent, with the same retry classification as
// fetchBlockWithRetry: a hash the node does not (yet) know is a not-found
// and retries indefinitely. Header-only by design: the descent reads the
// parent hash directly, performing no conversion and no receipt fetches,
// so a full bundle's by-number receipt fetch can never mix branches into
// the parent chain the descent follows.
func (p *ConcurrentBlockProcessor) fetchHeaderByHashWithRetry(ctx context.Context, hash string) (chains.BlockHeader, error) {
	otherErrors := 0
	for {
		if ctx.Err() != nil {
			return chains.BlockHeader{}, ctx.Err()
		}

		header, err := p.fetcher.FetchBlockHeaderByHash(ctx, hash)
		if err == nil {
			return header, nil
		}

		if errors.IsErrNotFound(err) {
			logger.Sugar.Infof("Block %s not available yet, waiting...", hash)
			select {
			case <-ctx.Done():
				return chains.BlockHeader{}, ctx.Err()
			case <-time.After(BlockNotFoundRetryDelay):
			}
			continue
		}

		otherErrors++
		if otherErrors >= MaxRPCRetries {
			return chains.BlockHeader{}, fmt.Errorf("failed to fetch block header by hash %s: %w", hash, err)
		}
		select {
		case <-ctx.Done():
			return chains.BlockHeader{}, ctx.Err()
		case <-time.After(time.Duration(otherErrors) * RPCErrorRetryBaseDelay):
		}
	}
}

// resolveForkPoint walks the new chain back by header until a stored block
// hash matches the chain's hash at that height (the common ancestor, i.e.
// the fork point). Along the way it records every visited height's verified
// new-chain hash (the descent's hash chain), which the rollback verification
// consumes. The descent never stores: it fetches each ancestor's header by
// hash (retry classification matches by-height fetches) and follows the
// header's parent hash directly, with no conversion and no receipt fetches -
// receipts are fetched by block number and therefore always land on the
// canonical branch, so a full bundle fetch would poison the descent on
// orphan ancestors. Empty descended heights do not stop the
// walk-back: failed stores and interrupted rollbacks leave holes with stored
// content above and below them, so the descent continues through the empties
// to find the verifiable ancestor, and the restart above the fork point
// re-fetches the range and re-fills those holes while it re-indexes. The
// walk-back descends below the finalized height: finality is not a walk
// bound, and an indexer that was down across a pre-finality reorg restarts
// with its whole stored range below current finality, while its true common
// ancestor still sits below the floor, reachable, and safe to verify. Deletion
// safety never relies on the floor either: the roller deletes only heights
// whose stored hash disagrees with the verified new-chain hash, so walking
// or rolling below finality never touches finalized content. The only bound
// the floor controls is the fallback depth cap, which applies when the chain
// cannot report a finalized height. Bounds: the common-ancestor match
// anywhere on the descended chain (including below the finalized height),
// the configured start height - nothing can be stored under it, so the first
// descended height with nothing stored below the start of indexing marks the
// verification boundary: the walk-back treats that height as the fork point
// bound (everything stored above it is unverifiable suspect content and gets
// rolled back) - genesis, and the depth cap when no finalized height is
// available. No common ancestor within bounds is a fatal error.
func (p *ConcurrentBlockProcessor) resolveForkPoint(ctx context.Context, startHeight int64, startChainHash string) (int64, map[int64]string, error) {
	if startHeight < 0 {
		return 0, nil, fmt.Errorf("reorg walk-back: walked past genesis without a common ancestor") //nolint:err113
	}
	if startChainHash == "" {
		return 0, nil, fmt.Errorf("reorg walk-back from height %d: the chain hash at the conflict height is unknown", startHeight) //nolint:err113
	}

	floor, err := p.fetcher.FetchFinalizedBlockNumber(ctx)
	if err != nil {
		logger.Sugar.Warnf("Reorg walk-back: finalized height unavailable (%v); falling back to the %d-level depth cap",
			err, maxWalkBackDepth)
		floor = -1
	}

	chainHashes := make(map[int64]string)
	cur, curHash := startHeight, startChainHash
	for depth := 0; ; depth++ {
		if cur < 0 {
			return 0, nil, fmt.Errorf("reorg walk-back from height %d: walked past genesis without a common ancestor", startHeight) //nolint:err113
		}
		chainHashes[cur] = curHash

		stored, err := p.reorgHandler.GetStoredBlockHash(ctx, cur)
		if err != nil {
			return 0, nil, fmt.Errorf("reorg walk-back: query stored hash at %d: %w", cur, err) //nolint:err113
		}
		if stored != "" {
			if stored == curHash {
				logger.Sugar.Infof("Reorg walk-back: common ancestor found at height %d (hash %s)", cur, curHash)
				return cur, chainHashes, nil
			}
		} else if cur < p.configuredStart {
			// Nothing stored at a descended height below the configured
			// start: no content will ever exist lower, so no common ancestor
			// can be confirmed below this height. Empties above it are holes
			// (failed stores, interrupted rollbacks) and were descended past,
			// so everything stored higher is suspect and gets rolled back;
			// restarting above this height re-fetches the chain's content and
			// re-fills the holes on the way. A restarted processor that
			// resumed above pre-existing stored content learns no lower
			// bound from that content, so a hole inside that older range
			// still stops the descent here: content stored under a previous
			// run at or below this height is then left unverified, which is
			// why the stop is surfaced as a warning rather than an info line.
			logger.Sugar.Warnf("Reorg walk-back: reached the verification boundary at %d (hash %s); blocks stored at or below this height under a previous run were not re-verified", cur, curHash)
			return cur, chainHashes, nil
		}

		if floor < 0 && depth >= maxWalkBackDepth {
			// The cap is a fallback bound only: it applies when the chain
			// cannot report a finalized height. Finality is not a walk bound,
			// so the descent below a reported finalized height continues and
			// may still verify a pre-finality ancestor.
			return 0, nil, fmt.Errorf("reorg walk-back from height %d: exceeded the %d-level depth cap without a common ancestor (no finalized height available)",
				startHeight, maxWalkBackDepth) //nolint:err113
		}

		header, err := p.fetchHeaderByHashWithRetry(ctx, curHash)
		if err != nil {
			return 0, nil, fmt.Errorf("reorg walk-back from height %d: %w", startHeight, err) //nolint:err113
		}
		if header.ParentHash == "" {
			return 0, nil, fmt.Errorf("reorg walk-back: block %s carries no parent hash", curHash) //nolint:err113
		}
		cur--
		curHash = header.ParentHash
	}
}

// storeWithRetry persists a ConversionResult via the block handler from the
// bounded store pool. Transaction conflicts are retried up to MaxRPCRetries
// times with transactionConflictRetryBaseDelay backoff; ErrAlreadyExists is
// the routine idempotent path - a fire-and-forget SignExisting goroutine and
// success - since content-derived dockeys can only collide on byte-identical
// content (the differing-content stale-stored case the old backstop targeted
// is covered by the seed and gap-fallback continuity rules instead).
func (p *ConcurrentBlockProcessor) storeWithRetry(ctx context.Context, blockNum int64, result chains.ConversionResult) *BlockResult {
	blockHash := extractBlockHash(result.Groups)

	for attempt := range MaxRPCRetries {
		if ctx.Err() != nil {
			return &BlockResult{BlockNum: blockNum, Hash: blockHash, Error: ctx.Err()}
		}

		start := time.Now()
		res, err := p.blockHandler.Store(ctx, result)
		outcome := p.metrics.success
		if err != nil {
			outcome = p.metrics.failure
		}
		p.metrics.storeDuration.Record(ctx, time.Since(start).Seconds(), outcome)
		if err == nil {
			return &BlockResult{BlockNum: blockNum, BlockID: res.BlockID, Hash: blockHash, Success: true}
		}

		if errors.IsErrAlreadyExists(err) {
			p.signWg.Go(func() {
				if _, sErr := p.blockHandler.SignExisting(ctx, result, blockHash, blockNum); sErr != nil {
					logger.Sugar.Warnf("Block %d: failed to create block signature for existing block: %v", blockNum, sErr)
				}
			})
			return &BlockResult{BlockNum: blockNum, Hash: blockHash, Success: true}
		}

		if errors.IsErrTransactionConflict(err) && attempt < MaxRPCRetries-1 {
			logger.Sugar.Infof("Block %d transaction conflict, retrying (attempt %d/%d)", blockNum, attempt+1, MaxRPCRetries)
			select {
			case <-ctx.Done():
				return &BlockResult{BlockNum: blockNum, Hash: blockHash, Error: ctx.Err()}
			case <-time.After(time.Duration(attempt+1) * transactionConflictRetryBaseDelay):
			}
			continue
		}

		return &BlockResult{BlockNum: blockNum, Hash: blockHash, Error: fmt.Errorf("failed to store block: %w", err)}
	}
	return &BlockResult{BlockNum: blockNum, Hash: blockHash, Error: fmt.Errorf("failed to store block %d: exhausted retries", blockNum)}
}

// extractBlockHash finds the block group (the one with BlockHashField != "")
// and returns its block hash value. Returns "" if no block group is found.
func extractBlockHash(groups []chains.DocumentGroup) string {
	for _, g := range groups {
		if g.BlockHashField != "" && len(g.Docs) > 0 {
			if hash, ok := g.Docs[0][g.BlockHashField].(string); ok {
				return hash
			}
		}
	}
	return ""
}

// extractBlockParentHash finds the block group (the one with
// ParentHashField != "") and returns its parent hash value. Returns "" if no
// block group is found. Mirrors extractBlockHash.
func extractBlockParentHash(groups []chains.DocumentGroup) string {
	for _, g := range groups {
		if g.ParentHashField != "" && len(g.Docs) > 0 {
			if hash, ok := g.Docs[0][g.ParentHashField].(string); ok {
				return hash
			}
		}
	}
	return ""
}
