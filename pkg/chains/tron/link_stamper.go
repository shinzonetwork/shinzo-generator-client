package tron

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// tronLinkStamper implements chains.LinkStamper for Tron chains. It
// resolves cross-document link fields (_blockID, _transactionID) around
// document writes.
//
// Mutable state: txHashToID and blockID are populated by RecordDocIDs as
// groups are written in group order (block, transaction, log, internal
// transfer); StampBeforeWrite consumes that state to stamp link fields
// into docs before they are written. Log and internal-transfer documents
// carry their own transactionHash, so no parallel parent-ref list exists.
type tronLinkStamper struct {
	cols       *CollectionNames
	txHashToID map[string]string
	blockID    string
}

var _ chains.LinkStamper = (*tronLinkStamper)(nil)

// newTronLinkStamper creates a tronLinkStamper for the given collection
// names.
func newTronLinkStamper(cols *CollectionNames) *tronLinkStamper {
	return &tronLinkStamper{
		cols:       cols,
		txHashToID: make(map[string]string),
	}
}

// StampBeforeWrite implements chains.LinkStamper. It stamps link fields
// derivable from already-recorded state into the docs, in-place, before
// they are written.
//
// Per-doc validation happens before that doc is mutated, so a failing doc
// is never stamped. Error cases:
//   - a transaction doc whose hash is missing, non-string, or empty (a
//     failed registration would otherwise poison txHashToID with an empty
//     key);
//   - a log or internal-transfer doc whose transactionHash is missing,
//     non-string, or empty;
//   - stamping derived docs before any block docID was registered (_blockID
//     is never written as an empty string);
//   - an unknown collection.
//
// A well-formed transactionHash with no registered counterpart (a log
// referencing a transaction that was never recorded here) leaves
// _transactionID absent with a debug log.
func (s *tronLinkStamper) StampBeforeWrite(collection string, docs []map[string]any) error {
	switch collection {
	case s.cols.Block:
		// The block doc carries no link fields; its docID is harvested by
		// RecordDocIDs.

	case s.cols.Transaction:
		for j := range docs {
			txHash, ok := docs[j][constants.HashFieldName].(string)
			if !ok || txHash == "" {
				return fmt.Errorf("link stamper: transaction doc %d has missing, non-string, or empty %q", //nolint:err113
					j, constants.HashFieldName)
			}
			if s.blockID == "" {
				return fmt.Errorf("link stamper: no block docID registered before stamping transaction doc %d", j) //nolint:err113
			}
			docs[j]["_blockID"] = s.blockID
		}

	case s.cols.Log, s.cols.InternalTransfer:
		for j := range docs {
			txHash, ok := docs[j][TransactionHashFieldName].(string)
			if !ok || txHash == "" {
				return fmt.Errorf("link stamper: doc %d of %s has missing, non-string, or empty %q", //nolint:err113
					j, collection, TransactionHashFieldName)
			}
			if s.blockID == "" {
				return fmt.Errorf("link stamper: no block docID registered before stamping %s doc %d", collection, j) //nolint:err113
			}
			docs[j]["_blockID"] = s.blockID
			if txID, ok := s.txHashToID[txHash]; ok {
				docs[j]["_transactionID"] = txID
			} else {
				logger.Sugar.Debugf("link stamper: doc %d of %s references unregistered transaction hash %s; leaving _transactionID absent",
					j, collection, txHash)
			}
		}

	default:
		return fmt.Errorf("link stamper: unknown collection %q", collection) //nolint:err113
	}

	return nil
}

// RecordDocIDs implements chains.LinkStamper. It records the write's
// assigned docIDs as lookup state consumed by later groups'
// StampBeforeWrite. It never mutates docs.
//
// Partial-write contract: an ids slice shorter than docs is a routine
// partial write, not an error. createDocBatch returns partial IDs on batch
// failure, and a partially-stored batch returns none at all alongside a
// distinct "previously stored" error (Store then stops the remaining
// groups). Registration is skipped for uncovered docs; when some IDs are
// present but fewer than docs, each skipped doc logs a debug line, while a
// call with no docIDs at all stays silent. Short input never errors and
// never panics.
//
// Error cases (same input contract as StampBeforeWrite, enforced so an
// invalid hash can never be registered):
//   - a transaction doc whose hash is missing, non-string, or empty;
//   - an unknown collection.
func (s *tronLinkStamper) RecordDocIDs(collection string, docs []map[string]any, ids []string) error {
	switch collection {
	case s.cols.Block:
		if len(ids) > 0 {
			s.blockID = ids[0]
		} else {
			logger.Sugar.Debugf("link stamper: block group written without docIDs; blockID stays %q", s.blockID)
		}

	case s.cols.Transaction:
		for j := range docs {
			txHash, ok := docs[j][constants.HashFieldName].(string)
			if !ok || txHash == "" {
				return fmt.Errorf("link stamper: transaction doc %d has missing, non-string, or empty %q", //nolint:err113
					j, constants.HashFieldName)
			}
			if j < len(ids) {
				s.txHashToID[txHash] = ids[j]
			} else if len(ids) > 0 {
				// Partial write (some docIDs, fewer than docs): a genuine
				// partial write worth one debug line. With no docIDs at all
				// (routine re-index) the whole call stays silent.
				logger.Sugar.Debugf("link stamper: partial write: transaction doc %d (%s) has no docID; skipping txHashToID registration", j, txHash)
			}
		}

	case s.cols.Log, s.cols.InternalTransfer:
		// Nothing to record: no later group consumes log or internal
		// transfer docIDs.

	default:
		return fmt.Errorf("link stamper: unknown collection %q", collection) //nolint:err113
	}

	return nil
}
