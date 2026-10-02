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

	// maxRollbacksPerRun bounds the total rollbacks within one ProcessBlocks
	// call. A healthy chain needs one rollback per reorg; exceeding this many
	// means the RPC node is persistently serving inconsistent data.
	maxRollbacksPerRun = 64

	// maxRollbacksPerHeight bounds rollbacks restarting from the same height.
	// Deeper reorgs converge by peeling one level per rollback (each restart
	// moves the rollback point down); repeating the same height means the
	// node keeps serving forked data for that height.
	maxRollbacksPerHeight = 3
)

// errReorgDetected is the cancel cause recorded on the run-scoped context
// when the continuity check detects a reorg. Nothing branches on it — the
// rollbackPoint sentinel on the processor drives the restart — but it keeps
// the cancellation reason inspectable.
var errReorgDetected = stderrors.New("reorg detected")

// rollbackPoint marks a detected reorg: indexing must restart from height,
// and every height dispatched up to dispatchedUpTo must be rolled back
// before the restart, because workers store blocks the moment the RPC
// returns them and buffered dispatch items are processed even after close.
type rollbackPoint struct {
	height         int64
	dispatchedUpTo int64
}

// reorgSignal describes a detected continuity break: the stored (orphan)
// hash and the chain-side hash it disagrees with, for logging.
type reorgSignal struct {
	rollbackFrom int64
	orphanHash   string
	chainHash    string
}

// BlockResult holds the result of processing a block.
type BlockResult struct {
	BlockNum   int64
	BlockID    string
	Hash       string
	ParentHash string
	Success    bool
	Error      error
	// StaleStored marks a block whose store reported "already exists" while
	// the stored block hash differs from the fetched hash — a stale block
	// left by a previous run whose tail reorged while the indexer was down.
	StaleStored bool
	// StoredHash carries the stored block hash at BlockNum, resolved by the
	// already-exists backstop; only set alongside StaleStored.
	StoredHash string
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

	// rollbackCount increments per executed rollback in the current
	// ProcessBlocks call; it backs the rollback-loop guards and is reset on
	// every ProcessBlocks entry.
	rollbackCount atomic.Int64

	// Run-scoped continuity state for the parentHash check, guarded by
	// pendingMu and reset by processBlocksOnce at each run start.
	runStartBlock     int64
	seedHash          string
	lastCommittedNum  int64
	lastCommittedHash string
	committedHashes   map[int64]string
	rollbackPoint     *rollbackPoint
	dispatchedUpTo    int64
	runCancel         context.CancelCauseFunc
}

// NewConcurrentBlockProcessor creates a new concurrent processor. The
// reorgHandler is the rollback primitive consumed by the continuity check
// and rollback orchestration; it may be nil, in which case a detected reorg
// stops indexing with an error instead of rolling back (no seed query and no
// already-exists backstop comparison are possible without it).
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
// The continuity check (collectResults) may detect a reorg; on detection the
// run is drained, the dispatched range is rolled back via the reorgHandler,
// and indexing restarts from the rollback height with clean state. Rollback
// failure and rollback-loop guard trips are fatal.
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

	p.rollbackCount.Store(0)
	heightRollbacks := make(map[int64]int)

	for {
		rb, err := p.processBlocksOnce(ctx, from, seed, onBlockProcessed)
		if rb == nil {
			// A nil rollbackPoint means the run was cancelled for a reason
			// other than reorg detection; the error is always non-nil then.
			return err
		}

		rollbacks := p.rollbackCount.Add(1)
		heightRollbacks[rb.height]++
		if rollbacks > maxRollbacksPerRun {
			return fmt.Errorf("rollback guard: %d rollbacks exceed the per-run limit of %d",
				rollbacks, maxRollbacksPerRun) //nolint:err113
		}
		if heightRollbacks[rb.height] > maxRollbacksPerHeight {
			return fmt.Errorf("rollback guard: height %d rolled back %d times, exceeding the limit of %d — the RPC node may persistently serve forked data",
				rb.height, heightRollbacks[rb.height], maxRollbacksPerHeight) //nolint:err113
		}

		if p.reorgHandler == nil {
			return fmt.Errorf("reorg detected at height %d but no rollback handler is configured", rb.height) //nolint:err113
		}

		logger.Sugar.Warnf("Rolling back blocks [%d, %d] after reorg detection",
			rb.height, rb.dispatchedUpTo)
		if err := p.reorgHandler.RollbackBlocks(ctx, rb.height, rb.dispatchedUpTo); err != nil {
			return fmt.Errorf("rollback blocks [%d, %d]: %w (partial rollback followed by re-indexing would leave duplicate heights; indexing stopped)",
				rb.height, rb.dispatchedUpTo, err) //nolint:err113
		}

		from = rb.height
		seed, err = p.resolveSeed(ctx, from)
		if err != nil {
			return err
		}
	}
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
// returns a rollbackPoint when the continuity check detected a reorg (the
// run-cause error is then the reorg sentinel and may be ignored); with a nil
// rollbackPoint the run ended by cancellation and the error is always
// non-nil.
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
		p.signWg.Wait()
		close(p.resultChan)
		collectWg.Wait()
	}

	err := p.dispatchLoop(runCtx, startBlock, workChan, shutdown)

	// dispatchLoop only exits by run-context cancellation, so err is always
	// non-nil here; the rollbackPoint (set by the continuity check before
	// cancellation) tells the caller whether the cancellation was a reorg.
	p.pendingMu.Lock()
	rb := p.rollbackPoint
	if rb != nil {
		rb.dispatchedUpTo = p.dispatchedUpTo
	}
	p.pendingMu.Unlock()
	return rb, err
}

// startWorkers launches processing and result-collection goroutines.
func (p *ConcurrentBlockProcessor) startWorkers(ctx context.Context, onBlockProcessed func(blockNum int64)) (chan int64, *sync.WaitGroup, *sync.WaitGroup) {
	workChan := make(chan int64, p.workers*DefaultWorkersAhead)

	var wg sync.WaitGroup
	for range p.workers {
		wg.Go(func() {
			for blockNum := range workChan {
				result := p.fetchAndProcessBlock(ctx, blockNum)
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
		p.collectResults(onBlockProcessed)
	})

	return workChan, &wg, &collectWg
}

// collectResults reads from resultChan and commits blocks in order. Before
// committing a successful block it runs the parentHash continuity check;
// on mismatch it records the rollback point and cancels the run-scoped
// context, after which remaining results are drained without committing so
// workers never block.
func (p *ConcurrentBlockProcessor) collectResults(onBlockProcessed func(blockNum int64)) {
	for result := range p.resultChan {
		p.pendingMu.Lock()
		p.pending[result.BlockNum] = result

		for {
			next, ok := p.pending[p.nextToCommit]
			if !ok {
				break
			}
			delete(p.pending, p.nextToCommit)

			if p.rollbackPoint != nil {
				// Rollback recorded: drain only — nothing commits, and no
				// continuity state advances past the rollback point.
				p.nextToCommit++
				continue
			}

			if next.Success {
				if signal, mismatch := p.continuityMismatch(next); mismatch {
					p.rollbackPoint = &rollbackPoint{height: signal.rollbackFrom}
					logger.Sugar.Warnf(
						"Reorg detected: stored block %d (hash %s) disagrees with chain-side block %d (hash %s); rolling back from %d",
						signal.rollbackFrom, signal.orphanHash, next.BlockNum, signal.chainHash, signal.rollbackFrom,
					)
					p.runCancel(errReorgDetected)
					p.nextToCommit++
					continue
				}

				if next.BlockID != "" {
					logger.Sugar.Infof("Committed block %d (ID: %s)", next.BlockNum, next.BlockID)
				} else {
					logger.Sugar.Infof("Committed block %d", next.BlockNum)
				}
				p.lastCommittedNum = next.BlockNum
				p.lastCommittedHash = next.Hash
				p.committedHashes[next.BlockNum] = next.Hash
				for height := range p.committedHashes {
					if height < p.nextToCommit-2 {
						delete(p.committedHashes, height)
					}
				}
				if onBlockProcessed != nil {
					onBlockProcessed(next.BlockNum)
				}
			} else {
				logger.Sugar.Warnf("Block %d failed: %v", next.BlockNum, next.Error)
			}
			p.nextToCommit++
		}
		p.pendingMu.Unlock()
	}
}

// continuityMismatch applies the parentHash detection rules to a successful
// result N, in spec order: the seed check at the run's first block (restart
// seam), the contiguous-predecessor check, and finally the already-exists
// backstop (stale stored block at N itself). A gap — the predecessor failed
// — matches the routine gap behavior and checks nothing; likewise a missing
// parent hash on either side of a comparison is unverifiable and skipped.
func (p *ConcurrentBlockProcessor) continuityMismatch(next *BlockResult) (reorgSignal, bool) {
	n := next.BlockNum

	if n == p.runStartBlock && p.seedHash != "" &&
		next.ParentHash != "" && next.ParentHash != p.seedHash {
		return reorgSignal{
			rollbackFrom: n - 1,
			orphanHash:   p.seedHash,
			chainHash:    next.ParentHash,
		}, true
	}

	if p.lastCommittedNum == n-1 && p.lastCommittedHash != "" &&
		next.ParentHash != "" && next.ParentHash != p.lastCommittedHash {
		return reorgSignal{
			rollbackFrom: n - 1,
			orphanHash:   p.lastCommittedHash,
			chainHash:    next.ParentHash,
		}, true
	}

	if next.StaleStored {
		return reorgSignal{
			rollbackFrom: n,
			orphanHash:   next.StoredHash,
			chainHash:    next.Hash,
		}, true
	}

	return reorgSignal{}, false
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

// fetchAndProcessBlock fetches, converts, and stores a block with retry
// classification:
//   - fetch not-found: infinite retry with BlockNotFoundRetryDelay
//   - fetch other errors: up to MaxRPCRetries with linear backoff
//   - convert: no retry (pure computation)
//   - store: up to MaxRPCRetries on transaction conflicts; ErrAlreadyExists
//     resolves the already-exists backstop (stale stored hash → StaleStored
//     result driving a rollback; otherwise a fire-and-forget SignExisting
//     goroutine and success)
func (p *ConcurrentBlockProcessor) fetchAndProcessBlock(ctx context.Context, blockNum int64) *BlockResult {
	raw, err := p.fetchBlockWithRetry(ctx, blockNum)
	if err != nil {
		return &BlockResult{BlockNum: blockNum, Error: err}
	}

	result, err := p.converter.Convert(ctx, raw)
	if err != nil {
		return &BlockResult{BlockNum: blockNum, Error: fmt.Errorf("convert block: %w", err)}
	}

	return p.storeWithRetry(ctx, blockNum, result)
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

// storeWithRetry persists a ConversionResult via the block handler. On
// ErrAlreadyExists it compares the fetched block hash against the stored
// hash at this height (already-exists backstop): equal means the routine
// idempotent path — a fire-and-forget SignExisting goroutine and success;
// different means a stale stored block from a previous run whose tail
// reorged while the indexer was down, and the result is marked StaleStored
// so the collector triggers a rollback from this height. When the stored
// hash cannot be compared (no reorgHandler, nothing stored, query error)
// the routine path applies. Transaction conflicts are retried up to
// MaxRPCRetries times with transactionConflictRetryBaseDelay backoff.
func (p *ConcurrentBlockProcessor) storeWithRetry(ctx context.Context, blockNum int64, result chains.ConversionResult) *BlockResult {
	blockHash := extractBlockHash(result.Groups)
	parentHash := extractBlockParentHash(result.Groups)

	for attempt := range MaxRPCRetries {
		if ctx.Err() != nil {
			return &BlockResult{BlockNum: blockNum, Hash: blockHash, ParentHash: parentHash, Error: ctx.Err()}
		}

		res, err := p.blockHandler.Store(ctx, result)
		if err == nil {
			return &BlockResult{BlockNum: blockNum, BlockID: res.BlockID, Hash: blockHash, ParentHash: parentHash, Success: true}
		}

		if errors.IsErrAlreadyExists(err) {
			stale, storedHash := p.staleStoredHash(ctx, blockNum, blockHash)
			if stale {
				return &BlockResult{
					BlockNum:    blockNum,
					Hash:        blockHash,
					ParentHash:  parentHash,
					StaleStored: true,
					StoredHash:  storedHash,
					Success:     true,
				}
			}
			p.signWg.Go(func() {
				if _, sErr := p.blockHandler.SignExisting(ctx, result, blockHash, blockNum); sErr != nil {
					logger.Sugar.Warnf("Block %d: failed to create block signature for existing block: %v", blockNum, sErr)
				}
			})
			return &BlockResult{BlockNum: blockNum, Hash: blockHash, ParentHash: parentHash, Success: true}
		}

		if errors.IsErrTransactionConflict(err) && attempt < MaxRPCRetries-1 {
			logger.Sugar.Infof("Block %d transaction conflict, retrying (attempt %d/%d)", blockNum, attempt+1, MaxRPCRetries)
			select {
			case <-ctx.Done():
				return &BlockResult{BlockNum: blockNum, Hash: blockHash, ParentHash: parentHash, Error: ctx.Err()}
			case <-time.After(time.Duration(attempt+1) * transactionConflictRetryBaseDelay):
			}
			continue
		}

		return &BlockResult{BlockNum: blockNum, Hash: blockHash, ParentHash: parentHash, Error: fmt.Errorf("failed to store block: %w", err)}
	}
	return &BlockResult{BlockNum: blockNum, Hash: blockHash, ParentHash: parentHash, Error: fmt.Errorf("failed to store block %d: exhausted retries", blockNum)}
}

// staleStoredHash decides the already-exists backstop: it reports whether the
// stored block hash at blockNum differs from the fetched blockHash. Only a
// non-empty stored hash with an empty (no-op) query error can differ; an
// unusable comparison (no reorgHandler, query failure, nothing stored)
// reports false, keeping the routine idempotent path.
func (p *ConcurrentBlockProcessor) staleStoredHash(ctx context.Context, blockNum int64, blockHash string) (bool, string) {
	if p.reorgHandler == nil || blockHash == "" {
		return false, ""
	}

	stored, err := p.reorgHandler.GetStoredBlockHash(ctx, blockNum)
	if err != nil {
		logger.Sugar.Warnf("Block %d: stored-hash query failed (%v); falling back to the idempotent sign-existing path", blockNum, err)
		return false, ""
	}
	if stored == "" {
		return false, ""
	}
	return stored != blockHash, stored
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
