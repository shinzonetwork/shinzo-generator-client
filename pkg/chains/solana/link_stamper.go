package solana

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// instructionKey identifies one outer instruction document in the stamper's
// lookup state: the parent transaction's signature plus the instruction's
// index within that transaction. Inner instructions carry this same
// parentage as a parallel ref so _parentInstructionID can be resolved after
// the outer group's docIDs are recorded.
type instructionKey struct {
	txSignature string
	outerIndex  int
}

// innerParentRef carries one inner instruction's parentage for the
// LinkStamper: the parent transaction's signature and the parent outer
// instruction's index within that transaction. Instruction docs carry the
// tx signature as a data field (the duplicate-content join field), so the
// stamper resolves _transactionID from the doc's own field, and only the
// outer-instruction link comes from this parallel array (one entry per doc,
// in doc order).
type innerParentRef struct {
	txSignature string
	outerIndex  int
}

// solanaLinkStamper implements chains.LinkStamper for Solana. It resolves
// cross-document link fields (_blockID, _transactionID,
// _parentInstructionID) around document writes.
//
// Mutable state: blockID, sigToTxID, and outerIDs are populated by
// RecordDocIDs as groups are written in group order (block -> transaction ->
// outer instructions -> inner instructions -> token balance changes ->
// rewards); StampBeforeWrite consumes that state to stamp link fields into
// docs before they are written.
//
// _transactionID comes from each doc's own transactionSignature join field
// (the contract that keeps duplicate-content docs on distinct docIDs), not
// from a second parallel ref, so the stamper stays independent of a
// converter-side list for that link. _parentInstructionID uses the
// converter's parallel innerRefs because the parent's docID is keyed by
// (txSignature, outerIndex), information no doc carries after the index
// fields were dropped from the schema.
type solanaLinkStamper struct {
	outerKeys []instructionKey // per outer instruction doc: parent tx sig + outer index
	innerRefs []innerParentRef // per inner instruction doc: parent tx sig + outer index

	cols      *CollectionNames
	blockID   string
	sigToTxID map[string]string
	outerIDs  map[instructionKey]string
}

var _ chains.LinkStamper = (*solanaLinkStamper)(nil)

// newSolanaLinkStamper creates a solanaLinkStamper with the given collection
// names and the per-doc parent reference arrays produced by the converter.
func newSolanaLinkStamper(
	cols *CollectionNames, outerKeys []instructionKey, innerRefs []innerParentRef,
) *solanaLinkStamper {
	return &solanaLinkStamper{
		cols:      cols,
		outerKeys: outerKeys,
		innerRefs: innerRefs,
		sigToTxID: make(map[string]string),
		outerIDs:  make(map[instructionKey]string),
	}
}

// StampBeforeWrite implements chains.LinkStamper. It stamps link fields
// derivable from already-recorded state into the docs, in-place, before
// they are written.
//
// Per-doc validation happens before that doc is mutated, so a failing doc is
// never stamped. Error cases:
//   - a transaction doc whose signature is missing, non-string, or empty (a
//     failed registration would otherwise poison sigToTxID with an empty key);
//   - an instruction or token-balance-change doc whose transactionSignature
//     join field is missing, non-string, or empty (both collections declare
//     the block/transaction relations, so an unjoinable doc must fail);
//   - stamping transaction, instruction, token-balance-change, or reward
//     docs before any block docID was registered (every derived collection
//     declares the block relation, and _blockID is never written empty);
//   - an unknown collection.
//
// A well-formed join field with no registered counterpart (an instruction
// referencing a transaction that was never recorded here) leaves
// _transactionID absent with a debug log. The two instruction groups share
// one collection case: per doc, the stackHeight key-presence invariant
// (outer docs never carry the key, inner docs always do) routes outer
// stamping (only _transactionID) apart from inner stamping (_transactionID
// plus _parentInstructionID).
func (s *solanaLinkStamper) StampBeforeWrite(collection string, docs []map[string]any) error {
	switch collection {
	case s.cols.Block:
		// The block doc carries no link fields; its docID is harvested by
		// RecordDocIDs.

	case s.cols.Transaction:
		for j := range docs {
			sig, ok := docs[j][SignatureFieldName].(string)
			if !ok || sig == "" {
				return fmt.Errorf("link stamper: transaction doc %d has missing, non-string, or empty %q", //nolint:err113
					j, SignatureFieldName)
			}
			if err := s.requireBlockID("transaction doc %d", j); err != nil {
				return err
			}
			docs[j]["_blockID"] = s.blockID
		}

	case s.cols.Instruction:
		for j := range docs {
			txSig, err := s.joinField(docs[j], j)
			if err != nil {
				return err
			}
			if err := s.requireBlockID("instruction doc %d", j); err != nil {
				return err
			}
			docs[j]["_blockID"] = s.blockID
			if _, inner := docs[j][StackHeightFieldName]; inner {
				s.stampParentInstruction(j, docs[j])
			}
			s.stampTxLink(j, docs[j], txSig)
		}

	case s.cols.TokenBalanceChange:
		for j := range docs {
			txSig, err := s.joinField(docs[j], j)
			if err != nil {
				return err
			}
			if err := s.requireBlockID("token balance change doc %d", j); err != nil {
				return err
			}
			docs[j]["_blockID"] = s.blockID
			s.stampTxLink(j, docs[j], txSig)
		}

	case s.cols.Reward:
		for j := range docs {
			if err := s.requireBlockID("reward doc %d", j); err != nil {
				return err
			}
			docs[j]["_blockID"] = s.blockID
		}

	default:
		return fmt.Errorf("link stamper: unknown collection %q", collection) //nolint:err113
	}

	return nil
}

// requireBlockID reports the start-of-group failure the shared block link
// would otherwise hit: every derived collection declares the block relation,
// so stamping before the block group recorded its docID is a hard error.
func (s *solanaLinkStamper) requireBlockID(docDesc string, j int) error {
	if s.blockID == "" {
		return fmt.Errorf("link stamper: no block docID registered before stamping "+docDesc, j) //nolint:err113
	}
	return nil
}

// joinField validates a doc's transactionSignature join field: missing,
// non-string, or empty values error out, since the doc would be unjoinable
// and content-collision-prone.
func (s *solanaLinkStamper) joinField(doc map[string]any, j int) (string, error) {
	txSig, ok := doc[TransactionSignatureFieldName].(string)
	if !ok || txSig == "" {
		return "", fmt.Errorf("link stamper: doc %d has missing, non-string, or empty %q", //nolint:err113
			j, TransactionSignatureFieldName)
	}
	return txSig, nil
}

// stampParentInstruction stamps _parentInstructionID on an inner instruction
// doc. The parent instruction link resolves only when the outer instruction
// group was written and recorded; otherwise it stays absent with a debug
// log.
func (s *solanaLinkStamper) stampParentInstruction(j int, doc map[string]any) {
	ref, ok := s.innerRef(j)
	if !ok {
		logger.Sugar.Debugf("link stamper: inner instruction doc %d has no parent ref; skipping _parentInstructionID", j)
		return
	}
	if parentID, ok := s.outerIDs[instructionKey(ref)]; ok {
		doc["_parentInstructionID"] = parentID
	} else {
		logger.Sugar.Debugf(
			"link stamper: inner instruction doc %d references unregistered outer instruction (%s#%d); leaving _parentInstructionID absent",
			j, ref.txSignature, ref.outerIndex)
	}
}

// stampTxLink stamps _transactionID on doc from the given parent tx
// signature. A well-formed but unregistered signature leaves the link
// absent with a debug log.
func (s *solanaLinkStamper) stampTxLink(j int, doc map[string]any, txSignature string) {
	if txID, found := s.sigToTxID[txSignature]; found {
		doc["_transactionID"] = txID
	} else {
		logger.Sugar.Debugf("link stamper: doc %d references unregistered transaction signature %s; leaving _transactionID absent",
			j, txSignature)
	}
}

// innerRef returns the inner instruction doc's parent reference, false when
// the parallel array is shorter than the docs (tolerated partial input).
func (s *solanaLinkStamper) innerRef(j int) (innerParentRef, bool) {
	if j >= len(s.innerRefs) {
		return innerParentRef{}, false
	}
	return s.innerRefs[j], true
}

// RecordDocIDs implements chains.LinkStamper. It records the write's
// assigned docIDs as lookup state consumed by later groups' StampBeforeWrite.
// It never mutates docs.
//
// Partial-write contract: an ids slice shorter than docs is a routine
// partial write, not an error. Batches that fail partway return the
// partial IDs, and re-indexed blocks return none at all (the docs already
// exist). Registration is skipped for uncovered docs: a debug log per
// skipped doc when some IDs are present, fully silent when none are. Short
// input never errors and never panics.
//
// Error cases (same input contract as StampBeforeWrite, enforced so an
// invalid signature can never be registered):
//   - a transaction doc whose signature is missing, non-string, or empty;
//   - an unknown collection.
func (s *solanaLinkStamper) RecordDocIDs(collection string, docs []map[string]any, ids []string) error {
	switch collection {
	case s.cols.Block:
		if len(ids) > 0 {
			s.blockID = ids[0]
		} else {
			logger.Sugar.Debugf("link stamper: block group written without docIDs; blockID stays %q", s.blockID)
		}

	case s.cols.Transaction:
		for j := range docs {
			sig, ok := docs[j][SignatureFieldName].(string)
			if !ok || sig == "" {
				return fmt.Errorf("link stamper: transaction doc %d has missing, non-string, or empty %q", //nolint:err113
					j, SignatureFieldName)
			}
			if j < len(ids) {
				s.sigToTxID[sig] = ids[j]
			} else if len(ids) > 0 {
				// Partial write (some docIDs, fewer than docs): a genuine
				// partial write worth one debug line. With no docIDs at all
				// (routine re-index) the whole call stays silent.
				logger.Sugar.Debugf("link stamper: partial write: transaction doc %d (%s) has no docID; skipping sigToTxID registration", j, sig)
			}
		}

	case s.cols.Instruction:
		for j := range docs {
			if _, inner := docs[j][StackHeightFieldName]; inner {
				// Nothing to record: no later group consumes inner
				// instruction docIDs.
				continue
			}
			s.recordOuterInstruction(j, ids)
		}

	case s.cols.TokenBalanceChange, s.cols.Reward:
		// Nothing to record: no later group consumes these docIDs.

	default:
		return fmt.Errorf("link stamper: unknown collection %q", collection) //nolint:err113
	}

	return nil
}

// recordOuterInstruction registers an outer instruction doc's docID under
// its (parent signature, instruction index) key from the converter's
// parallel array, applying the shared partial-write contract.
func (s *solanaLinkStamper) recordOuterInstruction(j int, ids []string) {
	if j >= len(s.outerKeys) {
		logger.Sugar.Debugf("link stamper: outer instruction doc %d has no parent ref; skipping registration", j)
		return
	}
	key := s.outerKeys[j]
	if j < len(ids) {
		s.outerIDs[key] = ids[j]
	} else if len(ids) > 0 {
		logger.Sugar.Debugf("link stamper: partial write: outer instruction doc %d (%s#%d) has no docID; skipping registration",
			j, key.txSignature, key.outerIndex)
	}
}
