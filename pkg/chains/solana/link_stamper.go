package solana

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// instructionKey identifies one outer instruction document: the parent
// transaction's signature plus the instruction's index within that
// transaction. It keys the outer-instruction docID lookup the stamper
// builds while the outer instruction group is written.
type instructionKey struct {
	txSignature string
	outerIndex  int
}

// solanaLinkStamper implements chains.LinkStamper for Solana. It resolves
// cross-document link fields (_blockID, _transactionID,
// _parentInstructionID) around document writes.
//
// Mutable state: blockID, sigToTxID, and outerIDs are populated by
// RecordDocIDs as groups are written in group order (block → transaction →
// outer instructions → inner instructions → token balance changes →
// rewards); StampBeforeWrite consumes that state to stamp link fields into
// docs before they are written.
//
// Instruction docs do carry the parent transaction's signature as a data
// field (transactionSignature, the duplicate-content join field), but
// parentage still comes from the parallel ref arrays the converter passes at
// construction (one entry per doc, in doc order) — the EVM aleParentRefs
// pattern extended with a second link level for CPI instructions. The refs
// avoid re-parsing doc maps for stamping and keep the stamper independent of
// any future join-field rename.
type solanaLinkStamper struct {
	outerRefs []string         // per outer instruction doc: parent tx signature
	innerRefs []innerParentRef // per inner instruction doc: parent tx sig + outer index
	tbcRefs   []string         // per token-balance-change doc: parent tx signature

	cols      *CollectionNames
	blockID   string
	sigToTxID map[string]string
	outerIDs  map[instructionKey]string
}

var _ chains.LinkStamper = (*solanaLinkStamper)(nil)

// newSolanaLinkStamper creates a solanaLinkStamper with the given collection
// names and the per-doc parent reference arrays produced by the converter.
func newSolanaLinkStamper(
	cols *CollectionNames, outerRefs []string, innerRefs []innerParentRef, tbcRefs []string,
) *solanaLinkStamper {
	return &solanaLinkStamper{
		cols:      cols,
		outerRefs: outerRefs,
		innerRefs: innerRefs,
		tbcRefs:   tbcRefs,
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
//   - stamping transaction or reward docs before any block docID was
//     registered (_blockID is never written as an empty string);
//   - an unknown collection.
//
// A well-formed parent ref with no registered counterpart (an instruction
// referencing a transaction that was never recorded here) leaves the link
// field absent with a debug log. The two instruction groups share one
// collection case: per doc, the stackHeight key-presence invariant (outer
// docs never carry the key, inner docs always do) routes outer stamping
// (only _transactionID) apart from inner stamping (_transactionID plus
// _parentInstructionID).
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
			if s.blockID == "" {
				return fmt.Errorf("link stamper: no block docID registered before stamping transaction doc %d", j) //nolint:err113
			}
			docs[j]["_blockID"] = s.blockID
		}

	case s.cols.Instruction:
		for j := range docs {
			if _, inner := docs[j][StackHeightFieldName]; inner {
				if err := s.stampInnerInstruction(j, docs[j]); err != nil {
					return err
				}
			} else if err := s.stampOuterInstruction(j, docs[j]); err != nil {
				return err
			}
		}

	case s.cols.TokenBalanceChange:
		for j := range docs {
			txSig, ok := s.tbcRef(j)
			s.stampTxLink(j, docs[j], txSig, ok)
		}

	case s.cols.Reward:
		for j := range docs {
			if s.blockID == "" {
				return fmt.Errorf("link stamper: no block docID registered before stamping reward doc %d", j) //nolint:err113
			}
			docs[j]["_blockID"] = s.blockID
		}

	default:
		return fmt.Errorf("link stamper: unknown collection %q", collection) //nolint:err113
	}

	return nil
}

// stampOuterInstruction stamps an outer instruction doc's _transactionID from
// its parent ref. A well-formed but unregistered signature leaves the link
// absent with a debug log; a ref beyond the parallel array skips stamping.
func (s *solanaLinkStamper) stampOuterInstruction(j int, doc map[string]any) error {
	if _, ok := doc[InstructionIndexFieldName].(int); !ok {
		return fmt.Errorf("link stamper: outer instruction doc %d has missing or non-int %q", //nolint:err113
			j, InstructionIndexFieldName)
	}
	txSig, ok := s.outerRef(j)
	s.stampTxLink(j, doc, txSig, ok)
	return nil
}

// stampInnerInstruction stamps an inner instruction doc's _transactionID and
// _parentInstructionID. The parent instruction link resolves only when the
// outer instruction group was written and recorded; otherwise it stays
// absent with a debug log.
func (s *solanaLinkStamper) stampInnerInstruction(j int, doc map[string]any) error {
	if _, ok := doc[InstructionIndexFieldName].(int); !ok {
		return fmt.Errorf("link stamper: inner instruction doc %d has missing or non-int %q", //nolint:err113
			j, InstructionIndexFieldName)
	}

	ref, ok := s.innerRef(j)
	if !ok {
		logger.Sugar.Debugf("link stamper: inner instruction doc %d has no parent ref; skipping links", j)
		return nil
	}
	s.stampTxLink(j, doc, ref.txSignature, true)

	if parentID, ok := s.outerIDs[instructionKey(ref)]; ok {
		doc["_parentInstructionID"] = parentID
	} else {
		logger.Sugar.Debugf(
			"link stamper: inner instruction doc %d references unregistered outer instruction (%s#%d); leaving _parentInstructionID absent",
			j, ref.txSignature, ref.outerIndex)
	}
	return nil
}

// stampTxLink stamps _transactionID on doc from the given parent tx
// signature, applying the shared tolerated-absence contract: missing ref →
// skip with a debug log; well-formed but unregistered signature → link left
// absent with a debug log.
func (s *solanaLinkStamper) stampTxLink(j int, doc map[string]any, txSignature string, ok bool) {
	if !ok {
		logger.Sugar.Debugf("link stamper: doc %d has no parent ref; skipping _transactionID", j)
		return
	}
	if txID, found := s.sigToTxID[txSignature]; found {
		doc["_transactionID"] = txID
	} else {
		logger.Sugar.Debugf("link stamper: doc %d references unregistered transaction signature %s; leaving _transactionID absent",
			j, txSignature)
	}
}

// outerRef returns the outer instruction doc's parent tx signature.
func (s *solanaLinkStamper) outerRef(j int) (string, bool) {
	if j >= len(s.outerRefs) {
		return "", false
	}
	return s.outerRefs[j], true
}

// innerRef returns the inner instruction doc's parent reference.
func (s *solanaLinkStamper) innerRef(j int) (innerParentRef, bool) {
	if j >= len(s.innerRefs) {
		return innerParentRef{}, false
	}
	return s.innerRefs[j], true
}

// tbcRef returns the token-balance-change doc's parent tx signature.
func (s *solanaLinkStamper) tbcRef(j int) (string, bool) {
	if j >= len(s.tbcRefs) {
		return "", false
	}
	return s.tbcRefs[j], true
}

// RecordDocIDs implements chains.LinkStamper. It records the write's
// assigned docIDs as lookup state consumed by later groups' StampBeforeWrite.
// It never mutates docs.
//
// Partial-write contract: an ids slice shorter than docs is a routine
// partial write — not an error. Batches that fail partway return the
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
			if err := s.recordOuterInstruction(j, docs[j], ids); err != nil {
				return err
			}
		}

	case s.cols.TokenBalanceChange, s.cols.Reward:
		// Nothing to record: no later group consumes these docIDs.

	default:
		return fmt.Errorf("link stamper: unknown collection %q", collection) //nolint:err113
	}

	return nil
}

// recordOuterInstruction registers an outer instruction doc's docID under its
// (parent signature, instruction index) key, applying the shared
// partial-write contract.
func (s *solanaLinkStamper) recordOuterInstruction(j int, doc map[string]any, ids []string) error {
	outerIndex, ok := doc[InstructionIndexFieldName].(int)
	if !ok {
		return fmt.Errorf("link stamper: outer instruction doc %d has missing or non-int %q", //nolint:err113
			j, InstructionIndexFieldName)
	}
	txSignature, ok := s.outerRef(j)
	if !ok {
		logger.Sugar.Debugf("link stamper: outer instruction doc %d has no parent ref; skipping registration", j)
		return nil
	}
	if j < len(ids) {
		s.outerIDs[instructionKey{txSignature: txSignature, outerIndex: outerIndex}] = ids[j]
	} else if len(ids) > 0 {
		logger.Sugar.Debugf("link stamper: partial write: outer instruction doc %d (%s#%d) has no docID; skipping registration",
			j, txSignature, outerIndex)
	}
	return nil
}
