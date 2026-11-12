package bitcoin

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// bitcoinLinkStamper implements chains.LinkStamper for the Bitcoin adapter.
// It resolves cross-document relation links (_blockID on Transaction docs,
// _transactionID on TxInput and TxOutput docs) around document writes.
//
// Mutable state: txidToID and blockID are populated by RecordDocIDs as groups
// are written in group order (block, transaction, txInput, txOutput);
// StampBeforeWrite consumes that state to stamp link fields into docs before
// they are written. Arrays and maps are keyed by the txid join field the
// schema declares on all three derived collections, so no parallel parent-ref
// bookkeeping is needed the way the evm access-list path requires.
type bitcoinLinkStamper struct {
	cols     *CollectionNames
	txidToID map[string]string
	blockID  string
}

var _ chains.LinkStamper = (*bitcoinLinkStamper)(nil)

// newBitcoinLinkStamper creates a stamper bound to the given collection
// names. A fresh instance is built per Convert call, so the state lifetime is
// exactly one block's write protocol.
func newBitcoinLinkStamper(cols *CollectionNames) *bitcoinLinkStamper {
	return &bitcoinLinkStamper{
		cols:     cols,
		txidToID: make(map[string]string),
	}
}

// StampBeforeWrite implements chains.LinkStamper. It stamps link fields
// derivable from already-recorded state into the docs, in-place, before they
// are written.
//
// Per-doc validation happens before that doc is mutated, so a failing doc is
// never stamped. Error cases:
//   - a doc whose txid join field is missing, non-string, or empty (a failed
//     registration would otherwise poison txidToID with an empty key);
//   - stamping any derived doc before a block docID was registered (the link
//     is never written as an empty string, and every block write precedes its
//     derived groups in the store's uniform protocol);
//   - an unknown collection.
//
// A well-formed txid with no registered transaction (the owning transaction
// doc was not stored, a routine partial write) leaves _transactionID absent
// with a debug log, matching the evm adapter's tolerated-absence contract.
// DefraDB relation fields are nullable, so an absent link stays legal.
func (s *bitcoinLinkStamper) StampBeforeWrite(collection string, docs []map[string]any) error {
	switch collection {
	case s.cols.Block:
		// The block doc carries no link fields; its docID is harvested by
		// RecordDocIDs.

	case s.cols.Transaction:
		for j := range docs {
			if v, ok := docs[j][TxIDFieldName].(string); !ok || v == "" {
				return fmt.Errorf("link stamper: transaction doc %d has missing, non-string, or empty %q", //nolint:err113
					j, TxIDFieldName)
			}
			blocked, err := s.requireBlockID(j)
			if err != nil {
				return err
			}
			docs[j]["_blockID"] = blocked
		}

	case s.cols.TxInput, s.cols.TxOutput:
		for j := range docs {
			txid, ok := docs[j][TxIDFieldName].(string)
			if !ok || txid == "" {
				return fmt.Errorf("link stamper: doc %d has missing, non-string, or empty %q", //nolint:err113
					j, TxIDFieldName)
			}
			if _, err := s.requireBlockID(j); err != nil {
				return err
			}
			if txDocID, known := s.txidToID[txid]; known {
				docs[j]["_transactionID"] = txDocID
			} else {
				logger.Sugar.Debugf("link stamper: doc %d references unregistered txid %s; leaving _transactionID absent", j, txid)
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
// write, not an error. The block-write stage returns partial IDs on batch
// failure, and a partially-stored batch returns none at all alongside a
// distinct "previously stored" error (Store then stops the remaining groups).
// Registration is skipped for uncovered docs; when some IDs are present but
// fewer than docs, each skipped doc logs a debug line, while a call with no
// docIDs at all stays silent. Short input never errors and never panics.
//
// Error cases (same input contract as StampBeforeWrite, enforced so an
// invalid txid can never be registered):
//   - a transaction doc whose txid is missing, non-string, or empty;
//   - an unknown collection.
func (s *bitcoinLinkStamper) RecordDocIDs(collection string, docs []map[string]any, ids []string) error {
	switch collection {
	case s.cols.Block:
		if len(ids) > 0 {
			s.blockID = ids[0]
		} else {
			logger.Sugar.Debugf("link stamper: block group written without docIDs; blockID stays %q", s.blockID)
		}

	case s.cols.Transaction:
		for j := range docs {
			txid, ok := docs[j][TxIDFieldName].(string)
			if !ok || txid == "" {
				return fmt.Errorf("link stamper: transaction doc %d has missing, non-string, or empty %q", //nolint:err113
					j, TxIDFieldName)
			}
			if j < len(ids) {
				s.txidToID[txid] = ids[j]
			} else if len(ids) > 0 {
				// Partial write (some docIDs, fewer than docs): a genuine
				// partial write worth one debug line. With no docIDs at all
				// (routine re-index) the whole call stays silent.
				logger.Sugar.Debugf("link stamper: partial write: transaction doc %d (%s) has no docID; skipping txid registration", j, txid)
			}
		}

	case s.cols.TxInput, s.cols.TxOutput:
		// Nothing to record: no later group consumes input or output docIDs.

	default:
		return fmt.Errorf("link stamper: unknown collection %q", collection) //nolint:err113
	}

	return nil
}

// requireBlockID returns the registered block docID, failing when stamping
// runs ahead of the block group's write. BlockHandler writes the block group
// first and records its docID before any derived group is reached, so this
// fires only when the protocol itself is misused.
func (s *bitcoinLinkStamper) requireBlockID(j int) (string, error) {
	if s.blockID == "" {
		return "", fmt.Errorf("link stamper: no block docID registered before stamping doc %d", j) //nolint:err113
	}
	return s.blockID, nil
}
