//go:build integration
// +build integration

package integration

import (
	"fmt"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/tron"
)

// Reorg phase: the branches flip while the indexer waits on the not-found
// retry of height 110. B[110] then arrives with a parent hash that
// disagrees with the stored branch-A hash at 109, the machinery walks back
// by hash to the common ancestor at 103 (the mock serves any branch's
// block by ID), rolls the orphan range away, and re-indexes the
// diverged suffix on branch B. Every wait rides the processor's own 3s
// retry cadence; the suite only polls with a generous deadline.

// tronBlockRow is one Block document reduced to what the reorg and restart
// phases compare.
type tronBlockRow struct {
	number int64
	hash   string
	docID  string
}

// tronBlockRows loads the hashed rows for a height range, keyed by
// height. A missing height is a hard failure: the caller's assertions
// index the map directly.
func tronBlockRows(t *testing.T, from, to int64) map[int64]tronBlockRow {
	t.Helper()
	rows := tronQueryRows(t, tron.DefaultCollectionBlock,
		tronRangeFilter("number", from, to),
		"number hash _docID")
	if len(rows) != int(to-from+1) {
		t.Fatalf("expected %d Block docs in [%d, %d], got %d", to-from+1, from, to, len(rows))
	}
	result := make(map[int64]tronBlockRow, len(rows))
	for _, row := range rows {
		got := tronBlockRow{
			number: tronFieldInt(row, "number"),
			hash:   tronString(row["hash"]),
			docID:  tronString(row["_docID"]),
		}
		result[got.number] = got
	}
	return result
}

// tronRangeFilter builds a GraphQL filter for an inclusive numeric range on
// an Int field, shared by every per-height assertion.
func tronRangeFilter(field string, from, to int64) string {
	return fmt.Sprintf("(filter: {%s: {_geq: %d, _leq: %d}})", field, from, to)
}

// tronNumStr renders one height as a GraphQL literal, keeping the per-height
// filters terse.
func tronNumStr(v int64) string {
	return fmt.Sprintf("%d", v)
}

// suitePhaseReorg flips the branch selector and waits for the processor to
// resolve the reorg, then asserts the post-reorg shape end to end.
func suitePhaseReorg(t *testing.T) {
	t.Helper()
	if tronFirstIndexer == nil {
		t.Fatal("reorg phase must run after the startup phase")
	}

	// Flip: since the branch head stays 109 for A, the walk into 110 is
	// the empty-object not-found the processor retries; B serves real
	// content at 110 with the diverged parent hash.
	tronChain.setActiveBranch(tronBranchB)

	// Branch A held 100..109 (10 rows) and B spans 100..115: after the
	// rollback of the orphaned 104..109 and the re-index of B's
	// 104..115 the collection holds exactly 16 rows.
	pollTronBlockCount(t, tronFirstIndexer, 16, 120*time.Second)

	rows := tronBlockRows(t, tronStartHeight, tronHeadBInitial)
	for num := tronForkHeight + 1; num <= tronHeadBInitial; num++ {
		want := tronChain.blockHashAt(tronBranchB, num)
		if got := rows[num].hash; got != want {
			t.Errorf("height %d hash after reorg: got %s want branch-B %s", num, got, want)
		}
	}

	// The orphaned branch-A block 105 must be gone entirely, not merely
	// shadowed at its height.
	a105 := tronChain.blockHashAt(tronBranchA, 105)
	orphans := tronQueryRows(t, tron.DefaultCollectionBlock,
		"(filter: {hash: {_eq: \""+a105+"\"}})", "number")
	if len(orphans) != 0 {
		t.Errorf("branch-A block 105 (%s) is still queryable after the reorg; %d rows remain", a105, len(orphans))
	}

	// The signature docs of the rolled-back range must now attest the
	// branch-B blocks: the rollback deletes the old ones and the re-index
	// re-signs the new content in the same pass.
	for num := tronForkHeight + 1; num <= tronHeadA; num++ {
		want := tronChain.blockHashAt(tronBranchB, num)
		sigs := tronQueryRows(t, tron.DefaultCollectionBlockSignature,
			"(filter: {blockNumber: {_eq: "+tronNumStr(num)+"}})",
			"blockNumber blockHash")
		if len(sigs) != 1 {
			t.Errorf("expected exactly 1 BlockSignature doc for re-indexed block %d, got %d", num, len(sigs))
			continue
		}
		if got := tronString(sigs[0]["blockHash"]); got != want {
			t.Errorf("block %d signature after reorg: got blockHash %s want branch-B %s", num, got, want)
		}
	}
}
