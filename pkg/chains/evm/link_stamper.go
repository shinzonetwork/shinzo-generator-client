package evm

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// evmLinkStamper implements chains.LinkStamper for EVM chains. It resolves
// cross-document link fields (_blockID, _transactionID) around document
// writes.
//
// Mutable state: txHashToID and blockID are populated by RecordDocIDs as
// groups are written in group order (block → tx → log → ALE); StampBeforeWrite
// consumes that state to stamp link fields into docs before they are written.
type evmLinkStamper struct {
	aleParentRefs []string
	cols          *CollectionNames
	txHashToID    map[string]string
	blockID       string
}

var _ chains.LinkStamper = (*evmLinkStamper)(nil)

// newEvmLinkStamper creates an evmLinkStamper with the given collection names
// and ALE parent references (one per ALE doc, holding the parent tx hash).
func newEvmLinkStamper(cols *CollectionNames, aleParentRefs []string) *evmLinkStamper {
	return &evmLinkStamper{
		aleParentRefs: aleParentRefs,
		cols:          cols,
		txHashToID:    make(map[string]string),
	}
}

// StampBeforeWrite implements chains.LinkStamper. It stamps link fields
// derivable from already-recorded state into the docs, in-place, before they
// are written.
//
// Per-doc validation happens before that doc is mutated, so a failing doc is
// never stamped. Error cases:
//   - a transaction doc whose hash is missing, non-string, or empty (a failed
//     registration would otherwise poison txHashToID with an empty key);
//   - a log doc whose transactionHash is missing, non-string, or empty;
//   - stamping tx/log docs before any block docID was registered (_blockID is
//     never written as an empty string);
//   - an unknown collection.
//
// A well-formed hash with no registered counterpart (a log referencing a tx
// that was never recorded here) leaves _transactionID absent with a debug log.
func (s *evmLinkStamper) StampBeforeWrite(collection string, docs []map[string]any) error {
	switch collection {
	case s.cols.Block:
		// The block doc carries no link fields; its docID is harvested by
		// RecordDocIDs.

	case s.cols.Transaction:
		for j := range docs {
			txHash, ok := docs[j][constants.HashKeyValue].(string)
			if !ok || txHash == "" {
				return fmt.Errorf("link stamper: transaction doc %d has missing, non-string, or empty %q", //nolint:err113
					j, constants.HashKeyValue)
			}
			if s.blockID == "" {
				return fmt.Errorf("link stamper: no block docID registered before stamping transaction doc %d", j) //nolint:err113
			}
			docs[j]["_blockID"] = s.blockID
		}

	case s.cols.Log:
		for j := range docs {
			txHash, ok := docs[j][constants.TransactionHashKeyValue].(string)
			if !ok || txHash == "" {
				return fmt.Errorf("link stamper: log doc %d has missing, non-string, or empty %q", //nolint:err113
					j, constants.TransactionHashKeyValue)
			}
			if s.blockID == "" {
				return fmt.Errorf("link stamper: no block docID registered before stamping log doc %d", j) //nolint:err113
			}
			docs[j]["_blockID"] = s.blockID
			if txID, ok := s.txHashToID[txHash]; ok {
				docs[j]["_transactionID"] = txID
			} else {
				logger.Sugar.Debugf("link stamper: log doc %d references unregistered transaction hash %s; leaving _transactionID absent", j, txHash)
			}
		}

	case s.cols.AccessListEntry:
		for j := range docs {
			if j >= len(s.aleParentRefs) {
				logger.Sugar.Debugf("link stamper: ALE doc %d has no parent ref; skipping _transactionID", j)
				continue
			}
			if txID, ok := s.txHashToID[s.aleParentRefs[j]]; ok {
				docs[j]["_transactionID"] = txID
			} else {
				logger.Sugar.Debugf("link stamper: ALE doc %d references unregistered parent tx hash %s; leaving _transactionID absent",
					j, s.aleParentRefs[j])
			}
		}

	default:
		return fmt.Errorf("link stamper: unknown collection %q", collection) //nolint:err113
	}

	return nil
}

// RecordDocIDs implements chains.LinkStamper. It records the write's assigned
// docIDs as lookup state consumed by later groups' StampBeforeWrite. It never
// mutates docs.
//
// Partial-write contract: an ids slice shorter than docs is a routine partial
// write — not an error. createDocBatch returns partial IDs on batch failure
// and re-indexed blocks return none at all (the docs already exist).
// Registration is skipped for uncovered docs; when some IDs are present but
// fewer than docs, each skipped doc logs a debug line, while a call with no
// docIDs at all (routine re-index) stays silent. Short input never errors and
// never panics.
//
// Error cases (same input contract as StampBeforeWrite, enforced so an
// invalid hash can never be registered):
//   - a transaction doc whose hash is missing, non-string, or empty;
//   - an unknown collection.
func (s *evmLinkStamper) RecordDocIDs(collection string, docs []map[string]any, ids []string) error {
	switch collection {
	case s.cols.Block:
		if len(ids) > 0 {
			s.blockID = ids[0]
		} else {
			logger.Sugar.Debugf("link stamper: block group written without docIDs; blockID stays %q", s.blockID)
		}

	case s.cols.Transaction:
		for j := range docs {
			txHash, ok := docs[j][constants.HashKeyValue].(string)
			if !ok || txHash == "" {
				return fmt.Errorf("link stamper: transaction doc %d has missing, non-string, or empty %q", //nolint:err113
					j, constants.HashKeyValue)
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

	case s.cols.Log, s.cols.AccessListEntry:
		// Nothing to record: no later group consumes log or ALE docIDs.

	default:
		return fmt.Errorf("link stamper: unknown collection %q", collection) //nolint:err113
	}

	return nil
}
