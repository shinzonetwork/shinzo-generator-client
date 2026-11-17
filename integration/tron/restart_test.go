//go:build integration
// +build integration

package integration

import (
	"testing"
	"time"
)

// Restart phase: stop the first indexer (which drains the block processor
// and closes its embedded node), grow branch B, and let a second indexer
// reopen the same store. The resume must come from the DB's highest stored
// block plus one; nothing below that height may be fetched again, and the
// surviving documents must keep their hashes and dockeys.

func suitePhaseRestart(t *testing.T) {
	t.Helper()
	if tronFirstIndexer == nil {
		t.Fatal("restart phase must run after the reorg phase")
	}

	// Snapshot every surviving block before the stop: hashes and dockeys
	// both, since a re-index would keep the hash but rewrite the doc.
	before := tronBlockRows(t, tronStartHeight, tronHeadBInitial)

	tronFirstIndexer.indexer.StopIndexing()

	// Marker: from here on, no wallet call may request a height below the
	// resume point. The second indexer's belt: resume at 115+1 = 116 once
	// branch B grows to 118, while the 3-block gap stays under the
	// configured StartBuffer.
	marker := tronChain.byNumLen()

	tronChain.extendBranchB(tronHeadBFinal)

	if err, settled := tronFirstIndexer.startError(); settled && err != nil {
		t.Fatalf("first indexer's StartIndexing exited with %v during the restart phase", err)
	}

	tronNextIndexer = startTronIndexer(t)
	pollTronBlockCount(t, tronNextIndexer, 19, 120*time.Second)

	// No gaps, no re-index: every surviving block keeps its hash and docID.
	after := tronBlockRows(t, tronStartHeight, tronHeadBInitial)
	for num := tronStartHeight; num <= tronHeadBInitial; num++ {
		was := before[num]
		now := after[num]
		if was.hash != now.hash {
			t.Errorf("height %d changed hash across the restart: got %s was %s", num, now.hash, was.hash)
		}
		if was.docID != now.docID {
			t.Errorf("height %d was re-indexed across the restart: docID %s was %s", num, now.docID, was.docID)
		}
	}

	// The extended head must be visible: heights 116..118 stored.
	tail := tronBlockRows(t, tronHeadBInitial+1, tronHeadBFinal)
	for num := tronHeadBInitial + 1; num <= tronHeadBFinal; num++ {
		want := tronChain.blockHashAt(tronBranchB, num)
		if got := tail[num].hash; got != want {
			t.Errorf("height %d hash after restart: got %s want branch-B %s", num, got, want)
		}
	}

	// No wallet call after the marker requested a height below the resume
	// point: resume must be DB-driven, not a replay of the stored range.
	for _, height := range tronChain.byNumSince(marker) {
		if height < tronHeadBInitial+1 {
			t.Errorf("second indexer refetched height %d after the restart (marker %d, resume must be %d)",
				height, marker, tronHeadBInitial+1)
		}
	}

	tronNextIndexer.indexer.StopIndexing()
	tronNextIndexer = nil
}
