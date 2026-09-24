//go:build bench

package solanabench

import (
	"context"
	stderrors "errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	solana "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
)

// ---------------------------------------------------------------------------
// Synthetic hot-slot generator and throughput benchmarks.
//
// Sizing instrument: measures doc counts/sec into DefraDB on hot
// mainnet-like slots and pins the duplicate-content behaviour of the
// Instruction-as-doc design. Identical memo transfers inside one slot
// produce byte-identical instruction and token-balance docs, which collide
// on DefraDB content-hash docIDs; the transactionSignature join field
// resolves that collision. The tests below pin the resolved behaviour and
// the benchmarks size the write throughput.
// ---------------------------------------------------------------------------

// syntheticProfile describes one synthetic slot shape. Doc volumes are
// derived from mainnet statistics: slots arrive at ~2.5/sec, average slots
// carry a few thousand documents and hot slots (bundle/airdrop bursts) tens
// of thousands, dominated by vote transactions with CPI inner instructions.
type syntheticProfile struct {
	name           string
	txCount        int // total transactions in the slot
	innerGroups    int // inner CPI instruction groups per transaction
	innersPerGroup int // CPI instructions per inner group
	tbcEvery       int // every Nth transaction carries token balances (0 = none)
	rewardCount    int // per-block reward entries
	dupPairs       int // transaction pairs sharing byte-identical instruction/token-balance content
}

var (
	// syntheticAverage approximates an average mainnet slot (~3.7k docs).
	syntheticAverage = syntheticProfile{
		name: "average", txCount: 600, innerGroups: 1, innersPerGroup: 2,
		tbcEvery: 4, rewardCount: 1200,
	}
	// syntheticHot approximates a hot mainnet slot (~32k docs, inner-
	// instruction dominated — the worst case for the Instruction-as-doc
	// design).
	syntheticHot = syntheticProfile{
		name: "hot", txCount: 3000, innerGroups: 2, innersPerGroup: 4,
		tbcEvery: 4, rewardCount: 1200,
	}
	// syntheticHotDup is syntheticHot with 100 duplicate-content transaction
	// pairs injected (the memo-transfer pattern).
	syntheticHotDup = syntheticProfile{
		name: "hot-dup", txCount: 3000, innerGroups: 2, innersPerGroup: 4,
		tbcEvery: 4, rewardCount: 1200, dupPairs: 100,
	}
	// syntheticDupStore is a CI-sized duplicate profile for the full
	// store+sign test: 100 duplicate pairs in a ~1k-doc slot.
	syntheticDupStore = syntheticProfile{
		name: "dup-store", txCount: 220, innerGroups: 1, innersPerGroup: 2,
		tbcEvery: 2, rewardCount: 100, dupPairs: 100,
	}
)

// syntheticBlock builds a deterministic block matching the profile. The
// first 2*dupPairs transactions form dupPairs pairs that share
// byte-identical outer/inner instruction and token-balance content while
// keeping distinct signatures and indexes — exactly the duplicate-content
// pattern the transactionSignature join field must absorb.
func syntheticBlock(slot uint64, p syntheticProfile) *solana.Block {
	block := fakeBlock(slot)
	blockHeight := slot
	block.BlockHeight = &blockHeight
	blockTime := int64(slot) //nolint:gosec // synthetic slots stay small
	block.BlockTime = &blockTime

	block.Transactions = make([]solana.Transaction, 0, p.txCount)
	for i := range p.txCount {
		block.Transactions = append(block.Transactions,
			syntheticTx(slot, i, fmt.Sprintf("synthetic-%d-%d", slot, i), p))
	}

	seedPrefix := fmt.Sprintf("dup-%d", slot)
	for k := range p.dupPairs {
		a, b := 2*k, 2*k+1
		if b >= len(block.Transactions) {
			break
		}
		block.Transactions[a].Instructions, block.Transactions[a].InnerInstructions = syntheticDupInstructions(seedPrefix, k)
		block.Transactions[a].PreTokenBalances, block.Transactions[a].PostTokenBalances = syntheticDupTokenBalances(seedPrefix, k)

		// Same payload content for the partner tx, but its identity stays
		// its own: memo-transfer duplicates differ by signature in the
		// wild, and colliding signatures would contrive a
		// transaction-level collision outside the duplicate-content
		// pattern's scope.
		block.Transactions[b] = block.Transactions[a]
		block.Transactions[b].Signature = fakeSignature(fmt.Sprintf("synthetic-%d-%d", slot, b))
		block.Transactions[b].TransactionIndex = b
	}

	block.Rewards = make([]solana.Reward, 0, p.rewardCount)
	for i := range p.rewardCount {
		seed := fmt.Sprintf("synthetic-r-%d-%d", slot, i)
		if i%8 == 0 {
			block.Rewards = append(block.Rewards, fakeReward("fee-"+seed, -25000, nil))
			block.Rewards[len(block.Rewards)-1].RewardType = "Fee"
			block.Rewards[len(block.Rewards)-1].PostBalance = 10_000_000 + uint64(i)
		} else {
			commission := uint8(i % 10)
			block.Rewards = append(block.Rewards, fakeReward(seed, 400+int64(i%50), &commission)) //nolint:gosec,mnd
			block.Rewards[len(block.Rewards)-1].PostBalance = 500_000_000 + uint64(i)*1000
		}
	}
	return block
}

// syntheticTx builds one transaction for the synthetic slot: one outer
// instruction, the profile's inner-CPI shape, and optionally a
// token-balance pair.
func syntheticTx(slot uint64, index int, seed string, p syntheticProfile) solana.Transaction {
	outer := fakeInstruction(0, 0, seed+"-outer")
	outer.StackHeight = nil

	groups := make([]solana.InnerInstructionGroup, 0, p.innerGroups)
	for g := range p.innerGroups {
		inners := make([]solana.Instruction, 0, p.innersPerGroup)
		for inner := range p.innersPerGroup {
			instr := fakeInstruction(0, inner, fmt.Sprintf("%s-g%d-i%d", seed, g, inner))
			stackHeight := uint16(1 + inner%4)
			instr.StackHeight = &stackHeight
			inners = append(inners, instr)
		}
		groups = append(groups, solana.InnerInstructionGroup{Index: uint16(g), Instructions: inners})
	}

	tx := solana.Transaction{
		Signature:         fakeSignature(seed),
		Slot:              slot,
		TransactionIndex:  index,
		Version:           "legacy",
		Fee:               5000,
		LogMessages:       []string{"Program log: entrypoint exited"},
		PreBalances:       []uint64{100_000, 200_000},
		PostBalances:      []uint64{95_000, 200_000},
		RecentBlockhash:   fakePubkey(fmt.Sprintf("recent-%s", seed)),
		AccountKeys:       []string{fakePubkey("acct-0-" + seed), fakePubkey("acct-1-" + seed)},
		Instructions:      []solana.Instruction{outer},
		InnerInstructions: groups,
	}
	if p.tbcEvery > 0 && index%p.tbcEvery == 0 {
		tx.PreTokenBalances = []solana.TokenBalance{{
			AccountIndex: 1, Mint: fakePubkey("mint-" + seed), Owner: fakePubkey("owner-" + seed),
			ProgramID: fakePubkey("tokenprog-" + seed), Amount: "100", Decimals: 6,
		}}
		tx.PostTokenBalances = []solana.TokenBalance{{
			AccountIndex: 1, Mint: fakePubkey("mint-" + seed), Owner: fakePubkey("owner-" + seed),
			ProgramID: fakePubkey("tokenprog-" + seed), Amount: "150", Decimals: 6,
		}}
	}
	return tx
}

// syntheticDupInstructions builds the byte-identical outer+inner instruction
// content shared by one duplicate pair (shareable on the wire only after the
// transactionSignature join field differentiates the documents).
func syntheticDupInstructions(seed string, k int) (outers []solana.Instruction, groups []solana.InnerInstructionGroup) {
	outer := fakeInstruction(0, 0, fmt.Sprintf("%s-%d-shared", seed, k))
	outer.StackHeight = nil
	inner := fakeInstruction(0, 0, fmt.Sprintf("%s-%d-shared", seed, k))
	stackHeight := uint16(2)
	inner.StackHeight = &stackHeight
	return []solana.Instruction{outer}, []solana.InnerInstructionGroup{{Index: 0, Instructions: []solana.Instruction{inner}}}
}

func syntheticDupTokenBalances(seed string, k int) (pre, post []solana.TokenBalance) {
	mint := fakePubkey(fmt.Sprintf("%s-%d-mint", seed, k))
	return []solana.TokenBalance{{
			AccountIndex: 1, Mint: mint, Owner: fakePubkey("dup-owner"),
			ProgramID: fakePubkey("tokenprog"), Amount: "100",
		}}, []solana.TokenBalance{{
			AccountIndex: 1, Mint: mint, Owner: fakePubkey("dup-owner"),
			ProgramID: fakePubkey("tokenprog"), Amount: "150",
		}}
}

// ---------------------------------------------------------------------------
// Synthetic harness helpers.
// ---------------------------------------------------------------------------

// syntheticDocCount converts a synthetic block once and sums the document
// counts across all groups (block included) — the per-slot doc volume.
func syntheticDocCount(conv *solana.Converter, p syntheticProfile) (int, error) {
	result, err := conv.Convert(context.Background(), syntheticBlock(1, p))
	if err != nil {
		return 0, err
	}
	total := 0
	for _, g := range result.Groups {
		total += len(g.Docs)
	}
	return total, nil
}

// mapCloneWithoutJoinField copies a doc map omitting the duplicate-content
// join field, used to compare duplicate-pair payloads.
func mapCloneWithoutJoinField(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if k == solana.TransactionSignatureFieldName {
			continue
		}
		out[k] = v
	}
	return out
}

// requireSyntheticProfileShape asserts the synthetic shape before it is fed
// into the store: the doc volume matches the profile math, and every
// duplicate pair's payload is content-identical (modulo the join field).
func requireSyntheticProfileShape(t *testing.T, conv *solana.Converter, p syntheticProfile, wantDocs int) {
	t.Helper()

	slot := uint64(len(p.name)) //nolint:gosec // deterministic small slot
	result, err := conv.Convert(context.Background(), syntheticBlock(slot, p))
	require.NoError(t, err)

	total := 0
	for _, g := range result.Groups {
		total += len(g.Docs)
	}
	assert.Equal(t, wantDocs, total, "profile %s must yield the documented doc volume", p.name)
	if p.dupPairs == 0 {
		return
	}

	outer, inner := result.Groups[2], result.Groups[3]
	require.Len(t, outer.Docs, p.txCount)
	for k := range p.dupPairs {
		a, b := 2*k, 2*k+1
		assert.Equal(t,
			mapCloneWithoutJoinField(outer.Docs[a]),
			mapCloneWithoutJoinField(outer.Docs[b]),
			"dup pair %d outer payload", k)
		assert.Equal(t,
			mapCloneWithoutJoinField(inner.Docs[a]),
			mapCloneWithoutJoinField(inner.Docs[b]),
			"dup pair %d inner payload", k)
	}
}

// ---------------------------------------------------------------------------
// Shape tests.
// ---------------------------------------------------------------------------

func TestSyntheticAverageProfileShape(t *testing.T) {
	t.Parallel()
	// 1 block + 600 txs + 600 outer + 600*2 inner + 150 tbc + 1200 rewards.
	requireSyntheticProfileShape(t, solana.NewConverter(benchConfig()), syntheticAverage, 3751)
}

func TestSyntheticHotProfileShape(t *testing.T) {
	t.Parallel()
	// 1 + 3000 txs + 3000 outer + 3000*8 inner + 750 tbc + 1200 rewards.
	requireSyntheticProfileShape(t, solana.NewConverter(benchConfig()), syntheticHot, 31951)
}

func TestSyntheticHotDupProfileShape(t *testing.T) {
	t.Parallel()
	// Dup pairs overwrite their transactions' shape (1 outer + 1 inner + a
	// token-balance pair regardless of tbcEvery), so the volume differs from
	// hot: 1 block + 3000 txs + 3000 outer + (200*1 + 2800*8) inner + 900 tbc
	// + 1200 rewards = 30701.
	requireSyntheticProfileShape(t, solana.NewConverter(benchConfig()), syntheticHotDup, 30701)
}

// TestStoreSlotWithDuplicateContentInstructions exercises the full
// production store path (Convert → LinkStamper → batched AddManyDocuments →
// sign) on a slot containing 100 duplicate-content transaction pairs.
// Identical instruction and token-balance docs collide on their content-hash
// docID: AddManyDocuments reports ErrAlreadyExists for the whole batch,
// BlockHandler drops the batch's docIDs (createDocsInTxn returns nil), the
// collected CID count fails to match the submitted doc count, and the block
// is silently left unsigned. With the transactionSignature join field every
// doc's content is unique, so the store succeeds and the block signs.
func TestStoreSlotWithDuplicateContentInstructions(t *testing.T) {
	store := newBenchStore(t, 1000)

	block := syntheticBlock(uint64(len("dup-store")), syntheticDupStore)
	result, err := store.conv.Convert(store.ctx, block)
	require.NoError(t, err)

	outerCount := len(result.Groups[2].Docs)
	innerCount := len(result.Groups[3].Docs)
	tbcCount := len(result.Groups[4].Docs)
	rewardCount := len(result.Groups[5].Docs)
	require.Equal(t, syntheticDupStore.txCount, outerCount)

	creation, err := store.handler.Store(store.ctx, result)
	require.NoError(t, err)

	assert.NotEmpty(t, creation.BlockSignatureID,
		"the block must sign despite duplicate instruction content")
	assert.Len(t, creation.OtherDocIDs[solana.CollectionInstruction], outerCount+innerCount)
	assert.Len(t, creation.OtherDocIDs[solana.CollectionTokenBalanceChange], tbcCount)
	assert.Len(t, creation.OtherDocIDs[solana.CollectionReward], rewardCount)

	// The signature doc is queryable through the production range query.
	slot := int64(len("dup-store"))
	docIDs, err := store.conv.GetDocIDsByBlockRange(store.ctx, store.td.Node, slot, slot)
	require.NoError(t, err)
	assert.NotEmpty(t, docIDs[solana.CollectionBlockSignature],
		"BlockSignature doc must be queryable through the production range query")
}

// ---------------------------------------------------------------------------
// Store-path benchmarks: docs/sec through Convert → Store → sign.
// ---------------------------------------------------------------------------

// BenchmarkSolanaStoreSlot reports doc counts/sec into DefraDB per profile.
// Run with
//
//	go test -tags bench ./benchmarking/solana -run '^$' -bench BenchmarkSolanaStoreSlot -benchtime 5x -count 3
func BenchmarkSolanaStoreSlot(b *testing.B) {
	for _, p := range []syntheticProfile{syntheticAverage, syntheticHot, syntheticHotDup} {
		b.Run(p.name, func(b *testing.B) {
			benchmarkStoreSlot(b, p, 1000)
		})
	}
}

// BenchmarkSolanaStoreHotSlotBatch sweeps MaxDocsPerTxn on the hot profile —
// the tuning dimension the BlockHandler per-group chunking consumes.
func BenchmarkSolanaStoreHotSlotBatch(b *testing.B) {
	for _, batch := range []int{100, 500, 1000, 2000} {
		b.Run(fmt.Sprintf("maxDocsPerTxn=%d", batch), func(b *testing.B) {
			benchmarkStoreSlot(b, syntheticHot, batch)
		})
	}
}

// benchmarkStoreSlot stores one distinct synthetic slot per iteration
// (identical slots would already-exist and abort the store — docIDs are
// content hashes), reporting docs/sec and slots/sec.
func benchmarkStoreSlot(b *testing.B, p syntheticProfile, maxDocsPerTxn int) {
	store := newBenchStore(b, maxDocsPerTxn)
	docsPerSlot, err := syntheticDocCount(store.conv, p)
	require.NoError(b, err)
	require.Positive(b, docsPerSlot)

	b.ReportMetric(float64(docsPerSlot), "docs/slot")
	slot := uint64(1_000_000_000)
	b.ResetTimer()
	for b.Loop() {
		block := syntheticBlock(slot, p)
		result, err := store.conv.Convert(store.ctx, block)
		if err != nil {
			b.Fatalf("convert slot %d: %v", slot, err)
		}
		if _, err := store.handler.Store(store.ctx, result); err != nil {
			b.Fatalf("store slot %d: %v", slot, err)
		}
		slot++
	}
	b.StopTimer()
	b.ReportMetric(float64(docsPerSlot)*float64(b.N)/b.Elapsed().Seconds(), "docs/sec")
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "slots/sec")
}

// ---------------------------------------------------------------------------
// End-to-end rate benchmark: fetch → convert → store → ordered commit
// through the real ConcurrentBlockProcessor, sweeping the worker
// (concurrent_blocks) dimension.
// ---------------------------------------------------------------------------

func BenchmarkSolanaIndexSlots(b *testing.B) {
	for _, workers := range []int{1, 4, 8} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			benchmarkIndexSlots(b, workers)
		})
	}
}

func benchmarkIndexSlots(b *testing.B, workers int) {
	store := newBenchStore(b, 1000)
	docsPerSlot, err := syntheticDocCount(store.conv, syntheticHot)
	require.NoError(b, err)
	require.Positive(b, docsPerSlot)

	// Tip far beyond any fetched slot: the fake serves a block for every
	// slot, so no skipped-height classification enters the loop.
	fetcher := solana.NewFetcher(&fakeSlotClient{
		tip: 1 << 62,
		blockFn: func(_ context.Context, slot uint64) (*solana.Block, error) {
			return syntheticBlock(slot, syntheticHot), nil
		},
	})

	proc := indexer.NewConcurrentBlockProcessor(fetcher, store.conv, store.handler, workers, 0)
	b.ReportMetric(float64(docsPerSlot), "docs/slot")

	ctx, cancel := context.WithCancel(store.ctx)
	defer cancel()

	var indexed atomic.Int64

	b.ResetTimer()
	// The b.N-th ordered commit cancels the run; ProcessBlocks then returns
	// the resulting context.Canceled, which is the expected exit.
	err = proc.ProcessBlocks(ctx, 2_000_000_000, func(_ int64) {
		if indexed.Add(1) == int64(b.N) {
			cancel()
		}
	})
	b.StopTimer()
	if err != nil && !stderrors.Is(err, context.Canceled) {
		b.Fatalf("process blocks: %v", err)
	}
	require.GreaterOrEqual(b, indexed.Load(), int64(b.N),
		"the processor must commit all %d benchmark blocks", b.N)

	b.ReportMetric(float64(docsPerSlot)*float64(b.N)/b.Elapsed().Seconds(), "docs/sec")
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "slots/sec")
}
