package evm

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// evmLinkStamper implements chains.LinkStamper for EVM chains. It resolves
// cross-document link fields (_blockID, _transactionID) after AddDocument
// assigns persistent docIDs.
//
// Mutable state: txHashToID and blockID are populated as StampLinks is called
// in group order (block → tx → log → ALE). This order is naturally enforced by
// the group slice returned by Convert.
type evmLinkStamper struct {
	aleParentRefs []string
	cols          *CollectionNames
	txHashToID    map[string]string
	blockID       string
}

// newEvmLinkStamper creates an evmLinkStamper with the given collection names
// and ALE parent references (one per ALE doc, holding the parent tx hash).
func newEvmLinkStamper(cols *CollectionNames, aleParentRefs []string) *evmLinkStamper {
	return &evmLinkStamper{
		aleParentRefs: aleParentRefs,
		cols:          cols,
		txHashToID:    make(map[string]string),
	}
}

// StampLinks implements chains.LinkStamper. It dispatches by writtenCollection
// and mutates the groups' doc maps in-place.
//
// Per-doc validation happens before that doc is mutated, so a failing doc is
// never stamped and never registered. Error cases:
//   - a transaction doc whose hash is missing, non-string, or empty (a failed
//     registration would otherwise poison txHashToID with an empty key);
//   - a log doc whose transactionHash is missing, non-string, or empty;
//   - stamping tx/log docs before any block docID was registered (_blockID is
//     never written as an empty string);
//   - an unknown writtenCollection.
//
// A writtenDocIDs slice shorter than writtenDocs is a routine partial write
// (e.g. createDocBatch returning partial IDs, or a re-indexed block whose
// docs already exist): the stamping proceeds and the registration is skipped;
// the post-write pass logs it as a partial write, while the pre-write pass
// (no docIDs by contract) stays silent. A well-formed hash with no registered
// counterpart (log referencing a tx that was never stamped here) leaves
// _transactionID absent with a debug log.
func (s *evmLinkStamper) StampLinks(
	_ []chains.DocumentGroup,
	writtenCollection string,
	writtenDocs []map[string]any,
	writtenDocIDs []string,
) error {
	switch writtenCollection {
	case s.cols.Block:
		if len(writtenDocIDs) > 0 {
			s.blockID = writtenDocIDs[0]
		} else {
			logger.Sugar.Debugf("link stamper: block group written without docIDs; blockID stays %q", s.blockID)
		}

	case s.cols.Transaction:
		for j := range writtenDocs {
			txHash, ok := writtenDocs[j][constants.HashKeyValue].(string)
			if !ok || txHash == "" {
				return fmt.Errorf("link stamper: transaction doc %d has missing, non-string, or empty %q", //nolint:err113
					j, constants.HashKeyValue)
			}
			if s.blockID == "" {
				return fmt.Errorf("link stamper: no block docID registered before stamping transaction doc %d", j) //nolint:err113
			}
			writtenDocs[j]["_blockID"] = s.blockID
			if j < len(writtenDocIDs) {
				s.txHashToID[txHash] = writtenDocIDs[j]
			} else if len(writtenDocIDs) > 0 {
				// Post-write partial registration (some docIDs, fewer than docs):
				// a genuine partial write worth one debug line. The pre-write pass
				// arrives with no docIDs by contract and stays silent.
				logger.Sugar.Debugf("link stamper: partial write: transaction doc %d (%s) has no docID; skipping txHashToID registration", j, txHash)
			}
		}

	case s.cols.Log:
		for j := range writtenDocs {
			txHash, ok := writtenDocs[j][constants.TransactionHashKeyValue].(string)
			if !ok || txHash == "" {
				return fmt.Errorf("link stamper: log doc %d has missing, non-string, or empty %q", //nolint:err113
					j, constants.TransactionHashKeyValue)
			}
			if s.blockID == "" {
				return fmt.Errorf("link stamper: no block docID registered before stamping log doc %d", j) //nolint:err113
			}
			writtenDocs[j]["_blockID"] = s.blockID
			if txID, ok := s.txHashToID[txHash]; ok {
				writtenDocs[j]["_transactionID"] = txID
			} else {
				logger.Sugar.Debugf("link stamper: log doc %d references unregistered transaction hash %s; leaving _transactionID absent", j, txHash)
			}
		}

	case s.cols.AccessListEntry:
		for j := range writtenDocs {
			if j >= len(s.aleParentRefs) {
				logger.Sugar.Debugf("link stamper: ALE doc %d has no parent ref; skipping _transactionID", j)
				continue
			}
			if txID, ok := s.txHashToID[s.aleParentRefs[j]]; ok {
				writtenDocs[j]["_transactionID"] = txID
			} else {
				logger.Sugar.Debugf("link stamper: ALE doc %d references unregistered parent tx hash %s; leaving _transactionID absent",
					j, s.aleParentRefs[j])
			}
		}

	default:
		return fmt.Errorf("link stamper: unknown collection %q", writtenCollection) //nolint:err113
	}

	return nil
}
