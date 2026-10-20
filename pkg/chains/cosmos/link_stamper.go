package cosmos

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// linkStamper implements chains.LinkStamper for cosmos-evm blocks. EVM
// collections are delegated to the EVM stamper; the Cosmos collections are
// linked here: CosmosTransaction → Block, and CosmosMessage/CosmosEvent →
// Block and (when tx-scoped) → CosmosTransaction.
//
// Groups are written in order Block → EVM groups → CosmosTransaction →
// CosmosMessage → CosmosEvent, so the block docID and the txHash→docID map
// are recorded before any doc that needs them is stamped.
type linkStamper struct {
	evm        chains.LinkStamper
	cols       *CollectionNames
	blockID    string
	txHashToID map[string]string
}

var _ chains.LinkStamper = (*linkStamper)(nil)

func newLinkStamper(evmStamper chains.LinkStamper, cols *CollectionNames) *linkStamper {
	return &linkStamper{
		evm:        evmStamper,
		cols:       cols,
		txHashToID: make(map[string]string),
	}
}

// StampBeforeWrite implements chains.LinkStamper.
func (s *linkStamper) StampBeforeWrite(collection string, docs []map[string]any) error {
	switch collection {
	case s.cols.CosmosTransaction:
		for j := range docs {
			if hash, ok := docs[j][constants.HashFieldName].(string); !ok || hash == "" {
				return fmt.Errorf("link stamper: cosmos transaction doc %d has missing, non-string, or empty %q", //nolint:err113
					j, constants.HashFieldName)
			}
			if err := s.stampBlock(collection, j, docs[j]); err != nil {
				return err
			}
		}

	case s.cols.CosmosMessage, s.cols.CosmosEvent:
		for j := range docs {
			if err := s.stampBlock(collection, j, docs[j]); err != nil {
				return err
			}
			txHash, scoped := docs[j][transactionHashFieldName].(string)
			if !scoped || txHash == "" {
				continue // block-level (FinalizeBlock) event: no parent transaction
			}
			if txID, ok := s.txHashToID[txHash]; ok {
				docs[j]["_transactionID"] = txID
			} else {
				logger.Sugar.Debugf("link stamper: %s doc %d references unregistered cosmos transaction %s; leaving _transactionID absent",
					collection, j, txHash)
			}
		}

	default:
		return s.evm.StampBeforeWrite(collection, docs)
	}
	return nil
}

// RecordDocIDs implements chains.LinkStamper. The block docID is recorded
// here and forwarded to the EVM stamper, which links its own groups to it.
func (s *linkStamper) RecordDocIDs(collection string, docs []map[string]any, ids []string) error {
	switch collection {
	case s.cols.Block:
		if len(ids) > 0 {
			s.blockID = ids[0]
		}
		return s.evm.RecordDocIDs(collection, docs, ids)

	case s.cols.CosmosTransaction:
		for j := range docs {
			hash, ok := docs[j][constants.HashFieldName].(string)
			if !ok || hash == "" {
				return fmt.Errorf("link stamper: cosmos transaction doc %d has missing, non-string, or empty %q", //nolint:err113
					j, constants.HashFieldName)
			}
			if j < len(ids) {
				s.txHashToID[hash] = ids[j]
			} else if len(ids) > 0 {
				logger.Sugar.Debugf("link stamper: partial write: cosmos transaction doc %d (%s) has no docID", j, hash)
			}
		}

	case s.cols.CosmosMessage, s.cols.CosmosEvent:
		// Nothing to record: no later group consumes message or event docIDs.

	default:
		return s.evm.RecordDocIDs(collection, docs, ids)
	}
	return nil
}

func (s *linkStamper) stampBlock(collection string, j int, doc map[string]any) error {
	if s.blockID == "" {
		return fmt.Errorf("link stamper: no block docID registered before stamping %s doc %d", collection, j) //nolint:err113
	}
	doc["_blockID"] = s.blockID
	return nil
}
