package evm

import (
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
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
func (s *evmLinkStamper) StampLinks(
	_ []chains.DocumentGroup,
	writtenCollection string,
	writtenDocs []map[string]any,
	writtenDocIDs []string,
) {
	switch writtenCollection {
	case s.cols.Block:
		if len(writtenDocIDs) > 0 {
			s.blockID = writtenDocIDs[0]
		}

	case s.cols.Transaction:
		for j := range writtenDocs {
			writtenDocs[j]["_blockID"] = s.blockID
			txHash, _ := writtenDocs[j][constants.HashKeyValue].(string)
			if j < len(writtenDocIDs) {
				s.txHashToID[txHash] = writtenDocIDs[j]
			}
		}

	case s.cols.Log:
		for j := range writtenDocs {
			writtenDocs[j]["_blockID"] = s.blockID
			txHash, _ := writtenDocs[j][constants.TransactionHashKeyValue].(string)
			if txID, ok := s.txHashToID[txHash]; ok {
				writtenDocs[j]["_transactionID"] = txID
			}
		}

	case s.cols.AccessListEntry:
		for j := range writtenDocs {
			if j < len(s.aleParentRefs) {
				if txID, ok := s.txHashToID[s.aleParentRefs[j]]; ok {
					writtenDocs[j]["_transactionID"] = txID
				}
			}
		}
	}
}
