//go:build integration
// +build integration

package integration

import (
	"testing"
	"time"
)

// Restart phase: stop the first indexer (which drains the block processor
// and closes its embedded node), grow the scripted chain, and let a second
// indexer reopen the same store. The resume must come from the DB's highest
// stored slot plus one; nothing below that height may be fetched again, and
// the surviving documents must keep their hashes and dockeys.
//
// The stored branch reaches slot 109 (with the skip at 107), the head grows
// to 112, and the 3-slot gap stays under the configured StartBuffer, so
// resume_from = 110.

func suitePhaseRestart(t *testing.T) {
	t.Helper()
	if solanaFirstIndexer == nil {
		t.Fatal("restart phase must run after the duplicate-docs phase")
	}

	// Snapshot every stored slot before the stop: hashes and dockeys both,
	// since a re-index would keep the hash but rewrite the doc.
	before := solanaBlockRows(t, solanaStartSlot, solanaHeadInitial)

	solanaFirstIndexer.indexer.StopIndexing()

	// Marker: from here on, no RPC call may request a slot below the
	// resume point.
	marker := solanaChain.byGetBlockLen()

	solanaChain.extend(solanaHeadExtended)

	if err, settled := solanaFirstIndexer.startError(); settled && err != nil {
		t.Fatalf("first indexer's StartIndexing exited with %v during the restart phase", err)
	}

	solanaNextIndexer = startSolanaIndexer(t)
	pollSolanaBlockCount(t, solanaNextIndexer, 12, 120*time.Second)

	// No gaps, no re-index: every surviving slot keeps its hash and docID.
	after := solanaBlockRows(t, solanaStartSlot, solanaHeadInitial)
	for slot := solanaStartSlot; slot <= solanaHeadInitial; slot++ {
		was, existed := before[slot]
		now, stillThere := after[slot]
		if !existed {
			// The skipped slot has no row on either side.
			if stillThere {
				t.Fatalf("slot %d gained a row across the restart", slot)
			}
			continue
		}
		if was.hash != now.hash {
			t.Errorf("slot %d changed hash across the restart: got %s was %s", slot, now.hash, was.hash)
		}
		if was.docID != now.docID {
			t.Errorf("slot %d was re-indexed across the restart: docID %s was %s", slot, now.docID, was.docID)
		}
	}

	// The extended head must be visible: heights 110..112 stored with their
	// scripted hashes.
	tail := solanaBlockRows(t, solanaHeadInitial+1, solanaHeadExtended)
	for slot := solanaHeadInitial + 1; slot <= solanaHeadExtended; slot++ {
		want := solanaChain.blockHashAt(slot)
		if got := tail[slot].hash; got != want {
			t.Errorf("slot %d hash after restart: got %s want scripted %s", slot, got, want)
		}
	}

	// No getBlock call after the marker requested a slot below the resume
	// point: resume must be DB-driven, not a replay of the stored range.
	for _, slot := range solanaChain.byGetBlockSince(marker) {
		if slot < solanaHeadInitial+1 {
			t.Errorf("second indexer refetched slot %d after the restart (marker %d, resume must be %d)",
				slot, marker, solanaHeadInitial+1)
		}
	}

	solanaNextIndexer.indexer.StopIndexing()
	solanaNextIndexer = nil
}
