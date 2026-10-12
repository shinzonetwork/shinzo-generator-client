package indexer

// reorg_defra_test.go is the real-DefraDB counterpart of reorg_test.go: the
// same continuity/rollback scenarios driven through the real evm fetcher
// (against a scripted JSON-RPC mock), the real evm converter, the real
// BlockHandler store, and the real ReorgRoller over testutils.SetupTestDefraDB.
//
// These tests are the acceptance bar for the reorg rework (check-before-store,
// orphan-only rollback): they encode what MUST happen and, on the branch's
// current code, demonstrably do not. The scenario chain is defined locally
// over mockChainHeader: because scenario blocks carry a transaction, their
// headers must carry a non-empty transactionsRoot (ethclient cross-checks the
// root against the tx list), so the canonical chain and its keccak-RLP hashes
// are scenario-local (reorgChainHeader/reorgChainHash) rather than the shared
// mockChainHash harness. Fork headers keep the scenario parent hash but
// differ in content (gasUsed), so go-ethereum's re-derived hash is a distinct,
// chain-consistent orphan hash per height.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/pruner"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// reorgHeight is the fork height for the real-DefraDB scenarios: the chain
// forks so that the first fetch of this height (and the forkDepth-1 heights
// above it) serves an orphan block, and every later fetch serves the
// canonical one.
const reorgHeight = int64(200)

// forkGasUsed is the header-field tweak that makes a fork block's
// keccak-RLP hash differ from the canonical hash at the same height, while
// its parentHash stays the canonical parent (chain-consistent fork).
const forkGasUsed = 0x5310

// reorgTxRootJSON is the non-empty transactionsRoot served on every scenario
// block response: scenario blocks carry one transaction, and go-ethereum's
// ethclient rejects a block whose header carries the empty trie root while
// the transaction list is non-empty. The root is part of the header, so it
// must align with the scenario header generator below.
const reorgTxRootJSON = "0x000000000000000000000000000000000000000000000000000000000000002a"

// reorgChainHeader builds the scenario chain's header at a height: the shared
// mock header structure with the scenario transactionsRoot, so the served
// JSON decodes into exactly this header and its keccak-RLP hash stays
// consistent with the served response fields.
func reorgChainHeader(n int64, parentHash common.Hash) *ethtypes.Header {
	hdr := mockChainHeader(n, parentHash)
	hdr.TxHash = common.HexToHash(reorgTxRootJSON)
	return hdr
}

var reorgChainHashes sync.Map

// reorgChainHash returns the scenario chain's keccak-RLP hash at a height.
// hash(n) embeds hash(n-1) through ParentHash, so a height is computed by
// walking down to the lowest missing ancestor and filling the prefix
// iteratively, in the same pattern as mockChainHash. Content is a pure
// function of the height, so the memo is safely shared across the parallel
// scenario tests.
func reorgChainHash(n int64) common.Hash {
	if n < 0 {
		return common.Hash{}
	}
	if v, ok := reorgChainHashes.Load(n); ok {
		return v.(common.Hash)
	}
	low := n
	for low > 0 {
		if _, ok := reorgChainHashes.Load(low - 1); ok {
			break
		}
		low--
	}
	for h := low; h <= n; h++ {
		parent := common.Hash{}
		if h > 0 {
			parent = reorgChainHashAt(h - 1)
		}
		reorgChainHashes.Store(h, reorgChainHeader(h, parent).Hash())
	}
	return reorgChainHashAt(n)
}

// reorgChainHashAt reads a memoized scenario hash; the caller must have filled
// the entry.
func reorgChainHashAt(n int64) common.Hash {
	v, _ := reorgChainHashes.Load(n)
	return v.(common.Hash)
}

// mockForkHeader builds the orphan variant of the scenario chain's header at
// a height: identical structure to the scenario canonical header except
// gasUsed, so its derived hash differs from reorgChainHash(height) at the
// same height.
func mockForkHeader(n int64, parentHash common.Hash) *ethtypes.Header {
	hdr := reorgChainHeader(n, parentHash)
	hdr.GasUsed = forkGasUsed
	return hdr
}

// forkChainHashes derives the orphan chain's keccak-RLP hashes for depths
// [0, depth) above the fork anchor: orphan(anchor) parents on the canonical
// hash of anchor-1, and every deeper orphan parents on the previous orphan.
// Content is a pure function of (anchor, depth), so the map is built per
// scenario without shared mutable state.
func forkChainHashes(anchor, depth int64) map[int64]common.Hash {
	hashes := make(map[int64]common.Hash, depth)
	for h := anchor; h < anchor+depth; h++ {
		parent := common.Hash{}
		if h > 0 {
			if prev, ok := hashes[h-1]; ok {
				parent = prev
			} else {
				parent = reorgChainHash(h - 1)
			}
		}
		hashes[h] = mockForkHeader(h, parent).Hash()
	}
	return hashes
}

// mockTxJSON is one legacy transaction served inside every block response.
// go-ethereum re-derives the tx hash from the RLP content, so the same JSON
// carries the same tx hash in every block; the transaction DOCUMENT content
// still differs per branch because the converter stamps the containing
// block's hash into it, exactly the re-included-transaction shape.
func mockTxJSON() map[string]any {
	return map[string]any{
		"nonce":    "0x1",
		"gasPrice": "0x1",
		"gas":      "0x5208",
		"to":       "0x0000000000000000000000000000000000000002",
		"value":    "0x0",
		"input":    "0x",
		"v":        "0x1b",
		// r/s must be canonical hexutil.Big quantities: no leading zero
		// digits. The dummy signature fails sender recovery deterministically
		// and the converter falls back to the zero address.
		"r": "0x1",
		"s": "0x2",
	}
}

// canonicalBlockResponse serves the canonical full block for a height: one
// transaction and no receipts, so no log documents exist. The header fields
// are overridden so the JSON decodes into exactly reorgChainHeader: the
// scenario transactions root, the scenario hash, and the scenario parent
// hash.
func canonicalBlockResponse(n int64) map[string]any {
	resp := fullBlockResponse(fmt.Sprintf("0x%x", n), []any{mockTxJSON()})
	resp["transactionsRoot"] = reorgTxRootJSON
	resp["hash"] = reorgChainHash(n).Hex()
	resp["parentHash"] = reorgChainHash(n - 1).Hex()
	return resp
}

// forkBlockResponse serves the orphan full block at a height: the canonical
// response with the orphan's parent hash, the tweaked gasUsed, and the orphan
// hash, so the JSON decodes into exactly mockForkHeader and the orphan chain
// stays parent-consistent: the anchor parents on the canonical parent, and
// deeper orphans parent on the previous orphan.
func forkBlockResponse(n int64, parentHashHex, forkHashHex string) map[string]any {
	resp := canonicalBlockResponse(n)
	resp["parentHash"] = parentHashHex
	resp["gasUsed"] = fmt.Sprintf("0x%x", forkGasUsed)
	resp["hash"] = forkHashHex
	return resp
}

// blockTagOf extracts the first param of a block RPC call as a string tag
// ("latest", "finalized", "safe"); ok is false for numeric/absent params.
func blockTagOf(params json.RawMessage) (string, bool) {
	var rawParams []json.RawMessage
	if err := json.Unmarshal(params, &rawParams); err != nil || len(rawParams) == 0 {
		return "", false
	}
	var tag string
	if err := json.Unmarshal(rawParams[0], &tag); err != nil {
		return "", false
	}
	return tag, true
}

// forkScript is the scripted chain RPC for the real-DefraDB reorg tests. When
// armed, the FIRST fetch of every orphan height serves the orphan block;
// every other fetch (and every re-fetch after a rollback) serves canonical.
// eth_getBlockByHash is served for both chains to support the walk-back
// header fetches the episode procedure relies on.
type forkScript struct {
	tip        int64
	forkHashes map[int64]common.Hash

	armed        atomic.Bool
	forkFetches  sync.Map // int64 height → *atomic.Int64
	hashToNumber sync.Map // lowercase hex hash → height
}

func (s *forkScript) responseFor(n int64, countFork bool) map[string]any {
	resp := canonicalBlockResponse(n)
	s.hashToNumber.Store(strings.ToLower(reorgChainHash(n).Hex()), n)
	if countFork && s.armed.Load() {
		forkHash, isForkHeight := s.forkHashes[n]
		if isForkHeight {
			counterAny, _ := s.forkFetches.LoadOrStore(n, new(atomic.Int64))
			if counterAny.(*atomic.Int64).Add(1) == 1 {
				parent := reorgChainHash(n - 1)
				if prev, ok := s.forkHashes[n-1]; ok {
					parent = prev
				}
				resp = forkBlockResponse(n, parent.Hex(), forkHash.Hex())
				s.hashToNumber.Store(strings.ToLower(forkHash.Hex()), n)
			}
		}
	}
	return resp
}

func (s *forkScript) handle(method string, params json.RawMessage) (any, error) {
	switch method {
	case ethGetBlockByNumber:
		if n, ok := requestedBlockNumber(params); ok {
			return s.responseFor(n, true), nil
		}
		tag, _ := blockTagOf(params)
		switch tag {
		case "finalized", "safe":
			// The safe/finalized floor sits at genesis in these scenarios so
			// the walk-back is bounded by the stored blocks instead.
			return s.responseFor(0, false), nil
		default:
			return s.responseFor(s.tip, false), nil
		}
	case "eth_getBlockByHash":
		var rawParams []json.RawMessage
		if err := json.Unmarshal(params, &rawParams); err != nil || len(rawParams) == 0 {
			return nil, fmt.Errorf("bad eth_getBlockByHash params")
		}
		var hashStr string
		if err := json.Unmarshal(rawParams[0], &hashStr); err != nil {
			return nil, fmt.Errorf("bad eth_getBlockByHash hash param")
		}
		heightAny, ok := s.hashToNumber.Load(strings.ToLower(hashStr))
		if !ok {
			return nil, fmt.Errorf("block not found")
		}
		return s.responseFor(heightAny.(int64), false), nil
	case ethGetBlockReceipts:
		return []any{}, nil
	case ethBlockNumber:
		return fmt.Sprintf("0x%x", s.tip), nil
	case ethChainID, netVersion:
		return "0x1", nil
	default:
		return "0x1", nil
	}
}

// canonicalBlockHash returns the scenario chain's canonical hash at a height:
// the keccak-RLP hash the mock RPC serves for it.
func canonicalBlockHash(n int64) common.Hash {
	return reorgChainHash(n)
}

// watchedReorgHandler wraps the real ReorgRoller and records every rollback
// call together with the stored block hash per height before and after the
// deletion, so the tests can prove deletes are hash-verified: heights whose
// stored hash matches the new chain must survive, orphan heights must not.
type watchedReorgHandler struct {
	inner defra.ReorgHandler

	mu            sync.Mutex
	calls         []rollbackCall
	visibleBefore []map[int64]string
	visibleAfter  []map[int64]string
}

// Compile-time guarantee that the watcher still satisfies the ReorgHandler
// contract the processor consumes.
var _ defra.ReorgHandler = (*watchedReorgHandler)(nil)

func (w *watchedReorgHandler) RollbackBlocks(ctx context.Context, from, to int64) error {
	before := w.snapshotHashes(ctx, from, to)
	err := w.inner.RollbackBlocks(ctx, from, to)
	after := w.snapshotHashes(ctx, from, to)
	w.mu.Lock()
	w.calls = append(w.calls, rollbackCall{from: from, to: to})
	w.visibleBefore = append(w.visibleBefore, before)
	w.visibleAfter = append(w.visibleAfter, after)
	w.mu.Unlock()
	return err
}

func (w *watchedReorgHandler) GetStoredBlockHash(ctx context.Context, blockNumber int64) (string, error) {
	return w.inner.GetStoredBlockHash(ctx, blockNumber)
}

// snapshotHashes records the stored hash per height in [from, to]; heights
// with a query error are omitted rather than guessed at.
func (w *watchedReorgHandler) snapshotHashes(ctx context.Context, from, to int64) map[int64]string {
	m := make(map[int64]string)
	for h := from; h <= to; h++ {
		if hash, err := w.inner.GetStoredBlockHash(ctx, h); err == nil {
			m[h] = hash
		}
	}
	return m
}

// snapshots returns the recorded rollback calls with their pre/post stored
// hash maps, thread-safely.
func (w *watchedReorgHandler) snapshots() (calls []rollbackCall, before, after []map[int64]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	calls = append([]rollbackCall(nil), w.calls...)
	before = append([]map[int64]string(nil), w.visibleBefore...)
	after = append([]map[int64]string(nil), w.visibleAfter...)
	return calls, before, after
}

// watchedQueueTracker wraps the real indexerQueueTracker wiring and records
// the first tracked block docID per height, so the tests can identify the
// orphan's queue entry (tracked at the orphan's pre-rollback store) and
// assert the rollback retained it.
type watchedQueueTracker struct {
	*indexerQueueTracker

	mu            sync.Mutex
	firstBlockIDs map[int64]string
}

func (t *watchedQueueTracker) TrackBlock(ctx context.Context, blockNumber int64, result *defra.BlockCreationResult) error {
	t.mu.Lock()
	if _, ok := t.firstBlockIDs[blockNumber]; !ok {
		t.firstBlockIDs[blockNumber] = result.BlockID
	}
	t.mu.Unlock()
	return t.indexerQueueTracker.TrackBlock(ctx, blockNumber, result)
}

// firstTrackedBlockID returns the block docID first tracked for a height.
func (t *watchedQueueTracker) firstTrackedBlockID(blockNumber int64) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.firstBlockIDs[blockNumber]
}

// realDefraReorgHarness wires the full real stack for the processor-level
// reorg tests: real evm fetcher against the scripted RPC, real converter,
// real BlockHandler, and the real ReorgRoller wrapped in the watcher, all
// over one test DefraDB node. The prune queue is wired the way StartIndexing
// wires it (handler tracks into it, roller cleans it) via the watched
// tracker.
type realDefraReorgHarness struct {
	td        *testutils.TestDefraDB
	script    *forkScript
	fetcher   chains.Fetcher
	converter chains.Converter
	handler   *defra.BlockHandler
	processor *ConcurrentBlockProcessor
	watched   *watchedReorgHandler
	queue     *pruner.IndexerQueue
	tracker   *watchedQueueTracker
}

func newRealDefraReorgHarness(t *testing.T, forkDepth int64) *realDefraReorgHarness {
	t.Helper()

	td := testutils.SetupTestDefraDB(t)

	script := &forkScript{tip: reorgHeight + 100, forkHashes: forkChainHashes(reorgHeight, forkDepth)}
	rpcServer := newMockRPCServer(script.handle)
	t.Cleanup(rpcServer.Close)

	fetcher, converter, handler := newTestProcessor(t, td, rpcServer.URL, 2)

	roller, err := defra.NewReorgRoller(td.Node, converter)
	require.NoError(t, err)
	watched := &watchedReorgHandler{inner: roller}

	queue := pruner.NewIndexerQueue()
	tracker := &watchedQueueTracker{
		indexerQueueTracker: &indexerQueueTracker{queue: queue, collections: converter.Collections()},
		firstBlockIDs:       make(map[int64]string),
	}
	handler.SetDocIDTracker(tracker)
	roller.SetDocIDTracker(tracker)

	processor := NewConcurrentBlockProcessor(fetcher, converter, handler, watched, 1, 0)

	return &realDefraReorgHarness{
		td:        td,
		script:    script,
		fetcher:   fetcher,
		converter: converter,
		handler:   handler,
		processor: processor,
		watched:   watched,
		queue:     queue,
		tracker:   tracker,
	}
}

// preStoreCanonical stores canonical blocks through the real pipeline
// (fetch → convert → store), simulating a previous run's stored state. It
// must run BEFORE the fork script is armed so the pre-stores receive the
// canonical responses.
func (h *realDefraReorgHarness) preStoreCanonical(t *testing.T, heights ...int64) {
	t.Helper()
	ctx := context.Background()
	for _, height := range heights {
		raw, err := h.fetcher.FetchBlock(ctx, height)
		require.NoError(t, err)
		result, err := h.converter.Convert(ctx, raw)
		require.NoError(t, err)
		_, err = h.handler.Store(ctx, result)
		require.NoError(t, err)
	}
}

// storedHashAt queries the stored block hash at a height through the real
// roller.
func (h *realDefraReorgHarness) storedHashAt(ctx context.Context, t *testing.T, height int64) string {
	t.Helper()
	hash, err := h.watched.GetStoredBlockHash(ctx, height)
	require.NoError(t, err)
	return hash
}

// txRowsAt queries the transaction collection for one height, returning the
// rows' hash and blockHash fields.
func (h *realDefraReorgHarness) txRowsAt(ctx context.Context, t *testing.T, height int64) []map[string]any {
	t.Helper()
	txCol, err := h.converter.Collections().GetCollection(chains.TypeTransaction)
	require.NoError(t, err)
	query := fmt.Sprintf("query { %s(filter: {%s: {_eq: %d}}) { %s %s } }",
		txCol, constants.BlockNumberFieldName, height, constants.HashFieldName, constants.BlockHashFieldName)
	result := h.td.Node.DB.ExecRequest(ctx, query)
	require.Empty(t, result.GQL.Errors, "tx query at height %d failed", height)
	data, ok := result.GQL.Data.(map[string]any)
	require.True(t, ok, "unexpected GQL result shape")
	return gqlRowsOf(data[txCol])
}

// gqlRowsOf flattens a GraphQL result field into row maps; the rows arrive as
// []any or []map[string]any depending on the driver path.
func gqlRowsOf(raw any) []map[string]any {
	var rows []map[string]any
	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				rows = append(rows, m)
			}
		}
	case []map[string]any:
		rows = typed
	}
	return rows
}

// waitForCommitsOrDeadline waits until want commits have been recorded or the
// deadline passes. Unlike waitForCommits it does not fail on timeout: the
// caller asserts afterwards so a stalled run fails with the DB-state
// assertions instead of a bare timeout message.
func waitForCommitsOrDeadline(t *testing.T, rec *commitRecorder, want int, errCh <-chan error, deadline time.Duration) bool {
	t.Helper()
	timer := time.After(deadline)
	for {
		if len(rec.snapshot()) >= want {
			return true
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-timer:
			return false
		case err := <-errCh:
			t.Fatalf("ProcessBlocks exited early: %v", err)
		}
	}
}

// assertCanonicalBlocksStored requires every height in [from, to] to be
// stored exactly once with the canonical (new-chain) hash: the re-included
// block at the reorg height is present and queryable, and no swept-then-lost
// gap exists anywhere in the indexed range.
func assertCanonicalBlocksStored(ctx context.Context, t *testing.T, h *realDefraReorgHarness, from, to int64) {
	t.Helper()
	for height := from; height <= to; height++ {
		stored := h.storedHashAt(ctx, t, height)
		require.Equal(t, canonicalBlockHash(height).Hex(), stored,
			"block %d must be stored exactly once with the canonical (new-chain) hash; got %q",
			height, stored)
		assert.NotEqual(t, h.script.forkHashes[height].Hex(), stored,
			"the orphan block at height %d must not be queryable", height)
	}
}

// assertOnlyMismatchingHeightsDeleted checks every recorded rollback call
// against the pre/post visibility snapshots: heights whose stored hash
// matched the new chain must survive the rollback untouched, and heights
// holding an orphan hash must end up deleted.
func assertOnlyMismatchingHeightsDeleted(t *testing.T, w *watchedReorgHandler) {
	t.Helper()
	calls, before, after := w.snapshots()
	for i, call := range calls {
		for height, pre := range before[i] {
			if pre == "" {
				continue
			}
			if pre == canonicalBlockHash(height).Hex() {
				assert.Equal(t, pre, after[i][height],
					"rollback [%d, %d] deleted the canonical block doc at height %d; matching heights must be skipped",
					call.from, call.to, height)
			} else {
				assert.Empty(t, after[i][height],
					"rollback [%d, %d] left the orphan block doc at height %d queryable",
					call.from, call.to, height)
			}
		}
	}
}

// TestReorgRealDefraDB_OneBlockReorg is the incident scenario end to end
// against real DefraDB: canonical blocks 199 and 201..205 are stored from a
// previous run; the live RPC then serves ONE orphan block at 200 on the first
// fetch (the reorg lands between the fetches). The processor must:
//
//   - commit the orphan 200 once its parentHash continuity check passes,
//   - detect the reorg at 201 (parentHash canonical 200 vs committed orphan),
//   - roll back ONLY the orphan (hash-verified per height: the pre-stored
//     canonical 201..204 must survive; today's sweep soft-deletes them),
//   - keep the prune-queue entry of the soft-deleted orphan (today's cleanup
//     drops it, leaking the soft-deleted docs from the pruner forever),
//   - converge: re-commit canonical 200 (fresh content, so no deleted-dockey
//     re-create), then 201..205 (idempotent already-exists path), and keep
//     indexing forward,
//   - never leave the re-included transaction or the orphan queryable.
//
// On the current branch this fails: the rollback sweep deletes the
// pre-stored canonical blocks, their identical re-fetches are refused by
// DefraDB (soft-deleted dockey re-create), and heights 201+ stay missing.
func TestReorgRealDefraDB_OneBlockReorg(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	h := newRealDefraReorgHarness(t, 1)
	h.preStoreCanonical(t, 199, 201, 202, 203, 204, 205)
	h.script.armed.Store(true)

	rec := &commitRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.processor.ProcessBlocks(ctx, reorgHeight, rec.record)
	}()

	converged := waitForCommitsOrDeadline(t, rec, 7, errCh, 30*time.Second)
	cancel()
	err := <-errCh
	require.ErrorIs(t, err, context.Canceled)

	// Commit shape: the orphan 200 commits once (its parent matched the
	// stored seed), then the canonical 200 re-commits after the rollback,
	// and canonical indexing resumes 201.. without gaps.
	require.GreaterOrEqual(t, len(rec.snapshot()), 7,
		"indexing must converge past the reorg (converged=%v)", converged)
	got := rec.snapshot()
	assert.Equal(t, []int64{200, 200, 201, 202, 203, 204, 205}, got[:7],
		"canonical indexing must resume at the fork point (200) and continue contiguously")

	// The re-included block at the reorg height is present and queryable,
	// exactly once, with the canonical hash; every height 199..205 stays
	// stored (no swept-and-lost gap).
	assertCanonicalBlocksStored(context.Background(), t, h, 199, 205)

	// The re-included transaction (same tx hash served on both branches) is
	// stored exactly once at the reorg height, joined to the canonical block.
	txRows := h.txRowsAt(context.Background(), t, 200)
	require.Len(t, txRows, 1, "the re-included transaction must be stored exactly once at the reorg height")
	assert.Equal(t, canonicalBlockHash(200).Hex(), txRows[0][constants.BlockHashFieldName],
		"the surviving transaction doc must reference the canonical block")

	// Exactly one rollback episode, starting at the fork point's orphan.
	calls, _, _ := h.watched.snapshots()
	require.Len(t, calls, 1, "a one-block reorg is one rollback episode")
	assert.Equal(t, reorgHeight, calls[0].from)

	// Deletes are hash-verified: only mismatching heights were deleted.
	assertOnlyMismatchingHeightsDeleted(t, h.watched)

	// The prune-queue entry of the soft-deleted orphan is retained so the
	// pruner can hard-purge the doc later.
	orphanBlockID := h.tracker.firstTrackedBlockID(200)
	require.NotEmpty(t, orphanBlockID, "the orphan must have been stored and tracked during run 1")
	blockCol, err := h.converter.Collections().GetCollection(chains.TypeBlock)
	require.NoError(t, err)
	sigCol, err := h.converter.Collections().GetCollection(chains.TypeBlockSignature)
	require.NoError(t, err)
	drained := h.queue.Drain(0, blockCol, sigCol)
	require.NotNil(t, drained)
	require.Contains(t, drained.DocIDsByCollection[blockCol], orphanBlockID,
		"the rollback must retain the orphan's prune-queue entry so the soft-deleted doc stays purgeable")
}

// TestReorgRealDefraDB_TwoBlockReorg is the deeper variant: orphans at 200
// and 201 (the orphan 101-level chain continues one block), canonical 202 is
// the first disagreement. The processor must converge in a single rollback
// episode (walk-back to the common ancestor at 199) instead of peeling one
// orphan level per rollback, and canonical 202 (which the current code
// stores in-run, store-before-check, and then sweeps) must survive.
//
// On the current branch this fails: each rollback peels one level, the sweep
// deletes the stored canonical 202+ (identical re-fetches then refused), and
// heights 201+ stay missing.
func TestReorgRealDefraDB_TwoBlockReorg(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	h := newRealDefraReorgHarness(t, 2)
	h.preStoreCanonical(t, 199, 202, 203, 204, 205)
	h.script.armed.Store(true)

	rec := &commitRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.processor.ProcessBlocks(ctx, reorgHeight, rec.record)
	}()

	converged := waitForCommitsOrDeadline(t, rec, 8, errCh, 30*time.Second)
	cancel()
	err := <-errCh
	require.ErrorIs(t, err, context.Canceled)

	require.GreaterOrEqual(t, len(rec.snapshot()), 8,
		"indexing must converge past the two-block reorg (converged=%v)", converged)
	got := rec.snapshot()
	assert.Equal(t, []int64{200, 201, 200, 201, 202, 203, 204, 205}, got[:8],
		"both orphans commit before detection; the restart must land at the fork point 200 and continue contiguously")

	assertCanonicalBlocksStored(context.Background(), t, h, 199, 205)

	// The re-included transaction at the first swept canonical height must
	// survive exactly once.
	txRows := h.txRowsAt(context.Background(), t, 202)
	require.Len(t, txRows, 1, "the transaction at the first swept canonical height must be stored exactly once")
	assert.Equal(t, canonicalBlockHash(202).Hex(), txRows[0][constants.BlockHashFieldName])

	// One rollback episode, starting at the deepest orphan.
	calls, _, _ := h.watched.snapshots()
	require.Len(t, calls, 1, "the walk-back must find the fork point in a single episode")
	assert.Equal(t, reorgHeight, calls[0].from)
	assert.GreaterOrEqual(t, calls[0].to, reorgHeight+1,
		"the rollback must cover the orphan chain above the fork point")

	assertOnlyMismatchingHeightsDeleted(t, h.watched)

	// Prune-queue entries of both soft-deleted orphans retained.
	orphanBlockID200 := h.tracker.firstTrackedBlockID(200)
	require.NotEmpty(t, orphanBlockID200, "orphan 200 must have been stored and tracked during run 1")
	orphanBlockID201 := h.tracker.firstTrackedBlockID(201)
	require.NotEmpty(t, orphanBlockID201, "orphan 201 must have been stored and tracked during run 1")
	blockCol, err := h.converter.Collections().GetCollection(chains.TypeBlock)
	require.NoError(t, err)
	sigCol, err := h.converter.Collections().GetCollection(chains.TypeBlockSignature)
	require.NoError(t, err)
	drained := h.queue.Drain(0, blockCol, sigCol)
	require.NotNil(t, drained)
	require.Contains(t, drained.DocIDsByCollection[blockCol], orphanBlockID200,
		"the rollback must retain orphan 200's prune-queue entry so the soft-deleted doc stays purgeable")
	require.Contains(t, drained.DocIDsByCollection[blockCol], orphanBlockID201,
		"the rollback must retain orphan 201's prune-queue entry so the soft-deleted doc stays purgeable")
}
