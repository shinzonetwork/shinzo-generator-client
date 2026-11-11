package defra

import (
	"context"
	"encoding/hex"
	stderrors "errors"
	"fmt"

	codecsig "github.com/shinzonetwork/shinzo-evidence-codec/sig"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/node"
)

// What Store returns for a block stored block-last when a header is already stored at its height.
// Either way the stored block is left untouched.
var (
	// ErrHeaderStored means the stored header has the block's hash: an earlier attempt committed the
	// block whole, with its attestation.
	ErrHeaderStored = stderrors.New("header already stored at the height")
	// ErrStoredHeaderDiffers means the stored header has another hash.
	ErrStoredHeaderDiffers = stderrors.New("another header is stored at the height")
)

// ErrAttestation wraps a failure to sign a block's statement, or a signature that does not verify.
// The block is not stored.
var ErrAttestation = stderrors.New("block attestation failed")

// signStatement signs statement with the node's key and returns the public key and the DER
// signature in hex, the ES256K form an attestation carries. A key that is not secp256k1 gives a
// signature commitBlock rejects, so the block is not stored and the height is retried.
func (h *BlockHandler) signStatement(ctx context.Context, statement []byte) (string, string, error) {
	fullIdent, err := h.fullIdentity(ctx)
	if err != nil {
		return "", "", err
	}
	signature, err := fullIdent.PrivateKey().Sign(statement)
	if err != nil {
		return "", "", fmt.Errorf("sign statement: %w", err)
	}
	return fullIdent.PublicKey().String(), hex.EncodeToString(signature), nil
}

// storeBlockLast writes the other groups, then the block document and its attestation in one
// transaction, so a stored block document means a complete, attested block. A height that already
// has a block document is left untouched, with ErrHeaderStored when its hash is the block's and
// ErrStoredHeaderDiffers otherwise.
func (h *BlockHandler) storeBlockLast(ctx context.Context, result chains.ConversionResult) (*BlockCreationResult, error) {
	blockInt, blockHash, err := blockNumberAndHash(result)
	if err != nil {
		return nil, err
	}
	blockGroup := result.Groups[0]
	stored, err := h.storedBlockHashes(ctx, blockGroup, blockInt)
	if err != nil {
		return nil, err
	}
	for _, hash := range stored {
		if hash != blockHash {
			return nil, fmt.Errorf("%w: block %d has %s stored, %s fetched", ErrStoredHeaderDiffers, blockInt, hash, blockHash)
		}
	}
	if len(stored) > 0 {
		return nil, ErrHeaderStored
	}
	if err := h.purgeLeftovers(ctx, result.Groups[1:], blockInt); err != nil {
		return nil, err
	}

	// A batch CID collector in ctx stops defradb from signing each block with the node's key. A
	// document's ID is the CID of the block that adds it, so these documents get the same IDs on
	// every node.
	ctx = node.ContextWithBatchSigning(ctx, node.NewBatchCIDCollector())
	creation := &BlockCreationResult{
		BlockNumber:              blockInt,
		BlockSignatureCollection: result.SignatureCollection,
		OtherDocIDs:              map[string][]string{},
	}
	docCount := 1
	for _, g := range result.Groups[1:] {
		ids, err := h.writeGroup(ctx, blockInt, g)
		if err != nil {
			return nil, err
		}
		creation.OtherDocIDs[g.Collection] = append(creation.OtherDocIDs[g.Collection], ids...)
		docCount += len(ids)
	}

	if err := h.commitBlock(ctx, result, blockInt, blockHash, docCount, creation); err != nil {
		return nil, err
	}
	if h.docIDTracker != nil {
		if err := h.docIDTracker.TrackBlock(ctx, blockInt, creation); err != nil {
			logger.Sugar.Warnf("Failed to track docIDs for block %d: %v", blockInt, err)
		}
	}
	return creation, nil
}

// commitBlock adds the block document and the block attestation, which signs the result's
// statement, in one transaction. The signature is checked with the evidence codec before it is
// stored. It records the two document IDs in creation.
func (h *BlockHandler) commitBlock(ctx context.Context, result chains.ConversionResult, blockInt int64, blockHash string, docCount int, creation *BlockCreationResult) error {
	txn, err := h.db.NewTxn(false)
	if err != nil {
		return fmt.Errorf("create txn for block %d: %w", blockInt, err)
	}
	defer txn.Discard()

	blockGroup := result.Groups[0]
	blockCol, err := txnCollection(ctx, txn, blockGroup.Collection)
	if err != nil {
		return err
	}
	blockDoc, err := client.NewDocFromMap(ctx, blockGroup.Docs[0], blockCol.Version())
	if err != nil {
		return fmt.Errorf("build block document %d: %w", blockInt, err)
	}
	if err := blockCol.AddDocument(ctx, blockDoc); err != nil {
		return fmt.Errorf("add block document %d: %w", blockInt, err)
	}

	signer, signature, err := h.signStatement(ctx, result.Statement)
	if err != nil {
		return fmt.Errorf("block %d: %w: %w", blockInt, ErrAttestation, err)
	}
	if err := codecsig.VerifyStatement(result.Statement, signer, codecsig.TypeES256K, signature); err != nil {
		return fmt.Errorf("block %d: %w: own signature: %w", blockInt, ErrAttestation, err)
	}
	attestationCol, err := txnCollection(ctx, txn, result.SignatureCollection)
	if err != nil {
		return err
	}
	attestationDoc, err := client.NewDocFromMap(ctx, map[string]any{
		constants.BlockNumberFieldName:   blockInt,
		constants.BlockHashFieldName:     blockHash,
		constants.StatementFieldName:     hex.EncodeToString(result.Statement),
		constants.SignatureTypeFieldName: codecsig.TypeES256K,
		constants.SignerFieldName:        signer,
		constants.SignatureFieldName:     signature,
	}, attestationCol.Version())
	if err != nil {
		return fmt.Errorf("build attestation document %d: %w", blockInt, err)
	}
	if err := attestationCol.AddDocument(ctx, attestationDoc); err != nil {
		return fmt.Errorf("add attestation document %d: %w", blockInt, err)
	}
	if err := txn.Commit(); err != nil {
		return fmt.Errorf("commit block %d: %w", blockInt, err)
	}

	creation.BlockID = blockDoc.ID().String()
	creation.BlockSignatureID = attestationDoc.ID().String()
	logger.Sugar.Infof("Block %d: attested (%d docs, signer %s...)", blockInt, docCount, truncate(signer, 16)) //nolint:mnd
	return nil
}

// purgeLeftovers hard-purges the documents the groups' collections hold at blockNumber. It runs
// only for a height with no block document, where they can only be what a failed attempt left. A
// soft-deleted document cannot be added again, so they are purged.
func (h *BlockHandler) purgeLeftovers(ctx context.Context, groups []chains.DocumentGroup, blockNumber int64) error {
	for _, g := range groups {
		ids, err := h.queryCollectionDocIDs(ctx, g.Collection, g.BlockNumField, blockNumber, blockNumber)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			continue
		}
		docIDs := make([]client.DocID, 0, len(ids))
		for _, id := range ids {
			docID, err := client.NewDocIDFromString(id)
			if err != nil {
				return fmt.Errorf("parse docID %s: %w", id, err)
			}
			docIDs = append(docIDs, docID)
		}
		txn, err := h.db.NewTxn(false)
		if err != nil {
			return fmt.Errorf("create txn to purge block %d: %w", blockNumber, err)
		}
		col, err := txnCollection(ctx, txn, g.Collection)
		if err != nil {
			txn.Discard()
			return err
		}
		if err := col.PurgeByDocIDs(ctx, docIDs, false); err != nil {
			txn.Discard()
			return fmt.Errorf("purge %d documents a failed attempt left in %s at block %d: %w", len(docIDs), g.Collection, blockNumber, err)
		}
		if err := txn.Commit(); err != nil {
			return fmt.Errorf("commit purge at block %d: %w", blockNumber, err)
		}
		logger.Sugar.Warnf("Block %d: purged %d documents a failed attempt left in %s", blockNumber, len(docIDs), g.Collection)
	}
	return nil
}

// txnCollection returns the collection named name bound to txn. A collection from
// txn.GetCollectionByName writes outside the transaction; one from GetCollections writes inside it.
func txnCollection(ctx context.Context, txn client.Txn, name string) (client.Collection, error) {
	cols, err := txn.GetCollections(ctx, options.GetCollections().SetCollectionName(name))
	if err != nil {
		return nil, fmt.Errorf("get collection %s: %w", name, err)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("get collection %s: %w", name, client.ErrCollectionNotFound)
	}
	return cols[0], nil
}

// storedBlockHashes returns the hash of each block document stored at height in g's collection.
func (h *BlockHandler) storedBlockHashes(ctx context.Context, g chains.DocumentGroup, height int64) ([]string, error) {
	query := fmt.Sprintf(`query { %s(filter: {%s: {_eq: %d}}) { %s } }`, g.Collection, g.BlockNumField, height, g.BlockHashField)
	result := h.db.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return nil, fmt.Errorf("query %s at %d: %w", g.Collection, height, result.GQL.Errors[0])
	}
	data, _ := result.GQL.Data.(map[string]any)
	docs, ok := data[g.Collection].([]map[string]any)
	if !ok {
		return nil, fmt.Errorf("query %s at %d: unexpected result %T", g.Collection, height, data[g.Collection]) //nolint:err113
	}
	hashes := make([]string, len(docs))
	for i, doc := range docs {
		hashes[i], _ = doc[g.BlockHashField].(string)
	}
	return hashes, nil
}
