package indexer

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"

	stderrors "errors"
)

const (
	// BlockNotFoundRetryDelay is the delay before retrying when a block is not yet available on chains.
	BlockNotFoundRetryDelay = 3 * time.Second

	// DispatchThrottleDelay is the delay when the processor is too far ahead of committed blocks.
	DispatchThrottleDelay = 100 * time.Millisecond

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
	// chain cannot report a finalized height. With a finalized floor the
	// walk-back is bounded by it instead.
	maxWalkBackDepth = 64
)

// errReorgDetected is the cancel cause recorded on the run-scoped context
// when the episode procedure resolves a reorg. Nothing branches on it - the
// rollbackPoint sentinel on the processor drives the restart - but it keeps
// the cancellation reason inspectable.
var errReorgDetected = stderrors.New("reorg detected")

// rollbackPoint marks a resolved reorg: the walk-back found the common
// ancestor at forkPoint, so the orphan range [forkPoint+1, oldTip] must be
// rolled back and indexing restarted from forkPoint+1. oldTip is the highest
// height dispatched when the run drained; heights above the last stored
// block hold nothing to delete, so it acts as an upper bound. chainHashes
// carries the verified new-chain hash per height for the walked segment -
// it travels to the rollback handler through the rollback context
// (defra.WithRollbackChainHashes) so the roller deletes only heights whose
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
}

// BlockResult holds the result of processing a block.
type BlockResult struct {
	BlockNum   int64
	BlockID    string
	Hash       string
	ParentHash string
	Success    bool
	Error      error
	// Converted carries the conversion result the ordered commit path
	// stores - but only after the parentHash continuity check passed.
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

	// episodeRollbacks increments per executed rollback in the current
	// episode sequence - consecutive rollbacks without an intervening
	// successful commit, since every commit resets it - and backs the
	// rollback-loop guards.
	episodeRollbacks atomic.Int64

	// Run-scoped continuity state for the parentHash check, guarded by
	// pendingMu and reset by processBlocksOnce at each run start.
	runStartBlock     int64
	seedHash          string
	lastCommittedNum  int64
	lastCommittedHash string
	committedHashes   map[int64]string
	rollbackPoint     *rollbackPoint
	episode           *reorgEpisode
	episodeErr        error
	// heightRollbacks counts consecutive rollbacks per restart height; any
	// successful commit resets it. Guarded by pendingMu.
	heightRollbacks map[int64]int
	dispatchedUpTo  int64
	runCancel       context.CancelCauseFunc
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
	return &ConcurrentBlockProcessor{
		fetcher:         fetcher,
		converter:       converter,
		blockHandler:    blockHandler,
		reorgHandler:    reorgHandler,
		workers:         workers,
		blocksPerMinute: blocksPerMinute,
		resultChan:      make(chan *BlockResult, workers*DefaultWorkersAhead),
		pending:         make(map[int64]*BlockResult),
	}
}

// ProcessBlocks dispatches blocks to workers and commits results in order.
// Nothing is stored before the parentHash continuity check passes: the
// ordered commit path holds a detected mismatch (and everything the chain
// serves past it) back, waits for the new chain to confirm during the
// episode procedure, walks back by hash to the fork point, and hands the
// orphan range to the rollback below. Rollback failure, walk-back failure,
// and rollback-loop guard trips are fatal.
func (p *ConcurrentBlockProcessor) ProcessBlocks(
	ctx context.Context,
	startBlock int64,
	onBlockProcessed func(blockNum int64),
) error {
	from := startBlock
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
		if episodeErr := p.episodeFailure(); episodeErr != nil {
			return episodeErr
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
		// The verified new-chain hashes ride the rollback context: the
		// handler signature is a stable two-integer contract, and the
		// wrappers around it forward the context untouched, so the roller
		// can still delete strictly by height and hash.
		if err := p.reorgHandler.RollbackBlocks(defra.WithRollbackChainHashes(ctx, rb.chainHashes), restart, rb.oldTip); err != nil {
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

// episodeFailure returns the fatal error the episode procedure recorded, if
// any. It is only meaningful after a run has fully drained (the collector
// writes it before cancelling).
func (p *ConcurrentBlockProcessor) episodeFailure() error {
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	return p.episodeErr
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
// rollbackPoint the run ended by cancellation - an episode failure is
// returned through the processor's episodeErr instead.
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
	p.lastCommittedNum = 0
	p.lastCommittedHash = ""
	p.committedHashes = make(map[int64]string)
	p.rollbackPoint = nil
	p.episode = nil
	p.episodeErr = nil
	p.dispatchedUpTo = startBlock - 1
	p.pending = make(map[int64]*BlockResult)
	// A fresh result channel per run: shutdown closes the previous one, and
	// a rollback restart runs processBlocksOnce again.
	p.resultChan = make(chan *BlockResult, p.workers*DefaultWorkersAhead)
	p.runCancel = runCancel
	p.pendingMu.Unlock()

	workChan, wg, collectWg := p.startWorkers(runCtx, onBlockProcessed)

	shutdown := func() {
		close(workChan)
		wg.Wait()
		// Sign goroutines are spawned by the collector since the store moved
		// into the commit path, so they must be drained only after the
		// collector has exited (they would otherwise race signWg).
		close(p.resultChan)
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

// startWorkers launches fetch/convert workers (the store moved into the
// ordered commit path) and the result-collection goroutine.
func (p *ConcurrentBlockProcessor) startWorkers(ctx context.Context, onBlockProcessed func(blockNum int64)) (chan int64, *sync.WaitGroup, *sync.WaitGroup) {
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

	var collectWg sync.WaitGroup
	collectWg.Go(func() {
		p.collectResults(ctx, onBlockProcessed)
	})

	return workChan, &wg, &collectWg
}

// collectResults reads from resultChan and commits blocks in order. Nothing
// is stored before the parentHash continuity check passes for the block: a
// matching block is stored on the commit path; a mismatching block is held
// back and the confirmation wait starts (the episode procedure). After a
// rollback point is recorded, or the run is cancelled for any other reason,
// remaining results are drained without committing so workers never block.
func (p *ConcurrentBlockProcessor) collectResults(ctx context.Context, onBlockProcessed func(blockNum int64)) {
	for result := range p.resultChan {
		p.pendingMu.Lock()
		p.pending[result.BlockNum] = result
		p.pendingMu.Unlock()

		p.drainPending(ctx, onBlockProcessed)
	}
}

// drainPending commits as many ordered pending results as are available.
// Only the collector pops p.pending[p.nextToCommit] and advances it
// (dispatch merely reads the field), so each popped result can pass through
// a pop → slow work → apply sequence without holding pendingMu across the
// slow parts (store, stored-hash queries, walk-back, gap re-fill).
func (p *ConcurrentBlockProcessor) drainPending(ctx context.Context, onBlockProcessed func(blockNum int64)) {
	for {
		next, ok := p.popPending(ctx)
		if !ok {
			return
		}
		if next == nil {
			continue
		}
		if p.episodeActive() {
			// Confirmation wait: the popped item is held back (never
			// stored) and resolved by the episode rules in resolveEpisodeItem.
			p.resolveEpisodeItem(ctx, next)
			continue
		}
		p.commitOrdered(ctx, next, onBlockProcessed)
	}
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

// commitOrdered runs the ordered commit path for one popped result: the
// parentHash continuity check must pass before anything is stored; a
// detected mismatch opens the confirmation-wait episode instead. The store
// and the stored-hash query run without holding pendingMu.
func (p *ConcurrentBlockProcessor) commitOrdered(ctx context.Context, next *BlockResult, onBlockProcessed func(blockNum int64)) {
	if !next.Success {
		logger.Sugar.Warnf("Block %d failed: %v", next.BlockNum, next.Error)
		return
	}

	if signal, mismatch := continuitySignal(p.runStartBlock, p.seedHash, p.lastCommittedNum, p.lastCommittedHash, next); mismatch {
		p.startReorgEpisode(signal, next)
		return
	}

	if signal, mismatch := p.gapSignal(ctx, next); mismatch {
		p.startReorgEpisode(signal, next)
		return
	}

	item := *next
	computed := p.storeWithRetry(ctx, item.BlockNum, item.Converted)
	if computed.Error != nil {
		logger.Sugar.Warnf("Block %d failed: %v", item.BlockNum, computed.Error)
		return
	}
	p.applyCommit(&item, computed, onBlockProcessed)
}

// gapSignal resolves the gap-fallback rule for a result whose predecessor
// was not committed in this run (escaped by a failed block or a fresh
// restart beyond the seed): the stored hash at N-1 is compared against the
// result's parentHash. The stored-hash query runs without pendingMu; a
// query failure leaves the height unverifiable (warn logged), an empty
// stored or parent hash skips.
func (p *ConcurrentBlockProcessor) gapSignal(ctx context.Context, next *BlockResult) (reorgSignal, bool) {
	p.pendingMu.Lock()
	needGap := next.ParentHash != "" && p.reorgHandler != nil &&
		next.BlockNum != p.runStartBlock && p.lastCommittedNum != next.BlockNum-1
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

// applyCommit applies a successful store to the ordered continuity state and
// fires the completion callback: commit bookkeeping, history trim, and the
// guard reset that ends a consecutive-rollback sequence on progress. The
// caller runs it after the store completed; nothing mutates the continuity
// state between pop and apply.
func (p *ConcurrentBlockProcessor) applyCommit(item *BlockResult, computed *BlockResult, onBlockProcessed func(blockNum int64)) {
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()

	if computed.BlockID != "" {
		logger.Sugar.Infof("Committed block %d (ID: %s)", item.BlockNum, computed.BlockID)
	} else {
		logger.Sugar.Infof("Committed block %d", item.BlockNum)
	}
	p.lastCommittedNum = item.BlockNum
	p.lastCommittedHash = item.Hash
	p.committedHashes[item.BlockNum] = item.Hash
	for height := range p.committedHashes {
		if height < p.nextToCommit-2 {
			delete(p.committedHashes, height)
		}
	}
	p.episodeRollbacks.Store(0)
	p.heightRollbacks = make(map[int64]int)
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
func (p *ConcurrentBlockProcessor) resolveEpisodeItem(ctx context.Context, item *BlockResult) {
	for {
		p.pendingMu.Lock()
		ep := p.episode
		if ep == nil || p.rollbackPoint != nil || ctx.Err() != nil {
			p.pendingMu.Unlock()
			return
		}

		if item.Success && item.Hash != "" && item.ParentHash != "" &&
			item.ParentHash == ep.lastParkedHash {
			confirmed := item.Hash
			p.pendingMu.Unlock()
			p.finalizeEpisode(ctx, ep, confirmed)
			return
		}

		if item.Success && item.Hash != "" && item.ParentHash != "" {
			// Second disagreement: the chain flipped again while nothing has
			// been deleted yet - re-anchor on the newest observation and
			// keep waiting.
			p.reanchorEpisodeLocked(ep, item)
			fatal := p.episodeErr != nil
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
		p.episodeErr = fmt.Errorf("reorg detected at height %d: the confirmation wait re-anchored %d times; the RPC node keeps flip-flopping between chains",
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
		p.episodeErr = fmt.Errorf("reorg detected at height %d: the confirmation wait re-fetched %d unusable verification points",
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
		p.episodeErr = fmt.Errorf("reorg detected at height %d: confirmation wait failed at %d: %w",
			baseHeight, target, refetched.Error) //nolint:err113
		p.pendingMu.Unlock()
		return nil, true, true
	}
	return refetched, false, false
}

// finalizeEpisode runs the walk-back for a confirmed episode and records the
// rollback point the outer ProcessBlocks loop acts on. The verified
// new-chain hashes (the walk-back descent, the parked mismatch and the
// confirming successor) travel inside the rollback point, so the rollback
// handler can delete strictly by height and hash. A walk-back failure
// is fatal unless the run context is already cancelled (user shutdown:
// nothing was stored or deleted, so the episode can simply be abandoned -
// the next run's seed and gap checks re-detect the mismatch).
func (p *ConcurrentBlockProcessor) finalizeEpisode(ctx context.Context, ep *reorgEpisode, confirmedHash string) {
	logger.Sugar.Infof("Reorg episode confirmed: walking back the new chain from height %d (hash %s)",
		ep.baseHeight-1, ep.baseParentHash)
	forkPoint, chainHashes, err := p.resolveForkPoint(ctx, ep.baseHeight-1, ep.baseParentHash)

	if err != nil && ctx.Err() != nil {
		logger.Sugar.Warnf("Reorg episode abandoned during shutdown: %v", err)
		p.pendingMu.Lock()
		p.episode = nil
		p.pendingMu.Unlock()
		return
	}

	if err == nil {
		// The confirmation wait verified the parked mismatch height and the
		// chained successor above it; the walk-back itself never saw
		// that segment.
		chainHashes[ep.baseHeight] = ep.lastParkedHash
		chainHashes[ep.baseHeight+1] = confirmedHash
	}

	p.pendingMu.Lock()
	p.episode = nil
	if err != nil {
		p.episodeErr = fmt.Errorf("reorg detected at height %d: %w", ep.baseHeight, err) //nolint:err113
	} else {
		p.rollbackPoint = &rollbackPoint{forkPoint: forkPoint, chainHashes: chainHashes}
	}
	p.pendingMu.Unlock()
	p.runCancel(errReorgDetected)
}

// continuitySignal applies the pure parentHash detection rules to a
// successful result N, in spec order: the seed check at the run's first
// block (restart seam) and the contiguous-predecessor check. The gap
// fallback - when the predecessor was not committed in this run, the stored
// hash at N-1 is compared against N's parentHash - needs a stored-hash
// query and is resolved separately by gapFallbackSignal.
func continuitySignal(runStartBlock int64, seedHash string, lastCommittedNum int64, lastCommittedHash string, next *BlockResult) (reorgSignal, bool) {
	n := next.BlockNum

	if n == runStartBlock && seedHash != "" &&
		next.ParentHash != "" && next.ParentHash != seedHash {
		return reorgSignal{
			rollbackFrom: n - 1,
			orphanHash:   seedHash,
			chainHash:    next.ParentHash,
		}, true
	}

	if lastCommittedNum == n-1 && lastCommittedHash != "" &&
		next.ParentHash != "" && next.ParentHash != lastCommittedHash {
		return reorgSignal{
			rollbackFrom: n - 1,
			orphanHash:   lastCommittedHash,
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
// path since the store moved into the ordered commit path. The block hash
// and parent hash are extracted from the conversion result: the parent hash
// feeds the continuity check, and the conversion result itself is stored by
// the commit path only after that check passed.
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
		// Success marks a fetch+convert pass: the ordered commit path and
		// the episode's confirmation rules treat the content as verified
		// input, not as a completed store.
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

// fetchByHashWithRetry fetches a block by its content hash for the walk-back
// descent, with the same retry classification as fetchBlockWithRetry: a
// hash the node does not (yet) know is a not-found and retries indefinitely.
func (p *ConcurrentBlockProcessor) fetchByHashWithRetry(ctx context.Context, hash string) (any, error) {
	otherErrors := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		raw, err := p.fetcher.FetchBlockByHash(ctx, hash)
		if err == nil {
			return raw, nil
		}

		if errors.IsErrNotFound(err) {
			logger.Sugar.Infof("Block %s not available yet, waiting...", hash)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(BlockNotFoundRetryDelay):
			}
			continue
		}

		otherErrors++
		if otherErrors >= MaxRPCRetries {
			return nil, fmt.Errorf("failed to fetch block by hash %s: %w", hash, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(otherErrors) * RPCErrorRetryBaseDelay):
		}
	}
}

// resolveForkPoint walks the new chain back by hash until a stored block
// hash matches the chain's hash at that height (the common ancestor, i.e.
// the fork point). Along the way it records every visited height's verified
// new-chain hash (the descent's hash chain), which the rollback verification
// consumes. The descent never stores: it fetches each ancestor by
// hash (retry classification matches by-height fetches), converts it, and
// follows the extracted parent hash. Bounds: never below the finalized
// height (safe as the implementation-level fallback; when neither tag is
// available the walk-back is bounded by the hard depth cap instead), and
// never below the lowest stored block - the first descended height with
// nothing stored marks the verification boundary, so the walk-back treats
// that height as the fork point bound (everything stored above it is
// unverifiable suspect content and gets rolled back). No common ancestor
// within bounds is a fatal error.
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
		} else if cur < startHeight {
			// Nothing stored at a descended height: the store's verified
			// content ended above, so a lower common ancestor can never be
			// confirmed. Everything stored above is suspect and gets rolled
			// back; restart from here re-fetches the chain's content.
			logger.Sugar.Infof("Reorg walk-back: reached the lowest stored height at %d (hash %s)", cur, curHash)
			return cur, chainHashes, nil
		}

		if floor >= 0 && cur <= floor {
			return 0, nil, fmt.Errorf("reorg walk-back from height %d: no common ancestor at or above the finalized height %d",
				startHeight, floor) //nolint:err113
		}
		if depth >= maxWalkBackDepth {
			return 0, nil, fmt.Errorf("reorg walk-back from height %d: exceeded the %d-level depth cap without a common ancestor",
				startHeight, maxWalkBackDepth) //nolint:err113
		}

		raw, err := p.fetchByHashWithRetry(ctx, curHash)
		if err != nil {
			return 0, nil, fmt.Errorf("reorg walk-back from height %d: %w", startHeight, err) //nolint:err113
		}
		converted, err := p.converter.Convert(ctx, raw)
		if err != nil {
			return 0, nil, fmt.Errorf("reorg walk-back: convert block %s: %w", curHash, err) //nolint:err113
		}
		parent := extractBlockParentHash(converted.Groups)
		if parent == "" {
			return 0, nil, fmt.Errorf("reorg walk-back: block %s carries no parent hash", curHash) //nolint:err113
		}
		cur--
		curHash = parent
	}
}

// storeWithRetry persists a ConversionResult via the block handler from the
// ordered commit path. Transaction conflicts are retried up to MaxRPCRetries
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

		res, err := p.blockHandler.Store(ctx, result)
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
