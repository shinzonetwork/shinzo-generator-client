package defra

import (
	"context"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"sort"
	"strings"
	"time"

	cid "github.com/ipfs/go-cid"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defracontext"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/node"
)

var errNoIdentity = stderrors.New("no identity available for signing") //nolint:gochecknoglobals

// ErrBlockNumberCorrupt indicates that a block document exists in the store
// but its "number" field is missing or has an unparseable type. It must NOT
// contain "not found" so pkgerrors.IsErrNotFound returns false — this ensures
// the pruner distinguishes corruption (hard error) from an empty DB (no-op).
var ErrBlockNumberCorrupt = stderrors.New("block exists but has invalid or unparseable number field") //nolint:gochecknoglobals

// blockDB abstracts the DB operations used by BlockHandler for testability.
type blockDB interface {
	NewTxn(readOnly bool) (client.Txn, error)
	ExecRequest(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult
}

// retryBackoff returns an exponential backoff duration capped at 8 seconds.
func retryBackoff(attempt int) time.Duration {
	d := 500 * time.Millisecond //nolint:mnd
	for range attempt {
		d *= 2 //nolint:mnd
	}
	if d > 8*time.Second { //nolint:mnd
		d = 8 * time.Second //nolint:mnd
	}
	return d
}

// BlockCreationResult holds the result of creating a block, including all docIDs.
type BlockCreationResult struct {
	BlockNumber              int64
	BlockID                  string
	BlockSignatureID         string
	BlockSignatureCollection string
	OtherDocIDs              map[string][]string // collection name → docIDs
}

// DocIDTrackerInterface defines the interface for tracking docIDs.
type DocIDTrackerInterface interface {
	TrackBlock(ctx context.Context, blockNumber int64, result *BlockCreationResult) error
}

// BlockHandler manages the creation and storage of blocks, transactions, and logs in DefraDB.
type BlockHandler struct {
	db            blockDB               // DB interface (from defraNode.DB).
	maxDocsPerTxn int                   // Default per-group batch size when group BatchSize is 0.
	docIDTracker  DocIDTrackerInterface // Optional tracker for docIDs.
	nodeIdentity  identity.Identity     // Node identity for signing.

	// Injectable functions for testability (set to defaults in NewBlockHandler).
	signBatchFn      func(ctx context.Context, collector *node.BatchCIDCollector) (*node.BatchSignature, error)
	verifyBatchSigFn func(sig *node.BatchSignature, cids []cid.Cid) (bool, error)
	collectDocCIDsFn func(ctx context.Context, docIDs []string, collectionNames []string) ([]cid.Cid, error)
	maxCIDRetries    int
	retryBackoffFn   func(int) time.Duration
}

// NewBlockHandler creates a BlockHandler that uses direct DB calls.
// maxDocsPerTxn is the default per-group batch size.
func NewBlockHandler(defraNode *node.Node, maxDocsPerTxn int) (*BlockHandler, error) {
	if defraNode == nil {
		return nil, errors.NewConfigurationError("defra", "NewBlockHandler",
			"defraNode is nil", "", nil)
	}
	if maxDocsPerTxn <= 0 {
		maxDocsPerTxn = 1000 //nolint:mnd
	}
	h := &BlockHandler{
		db:             defraNode.DB,
		maxDocsPerTxn:  maxDocsPerTxn,
		maxCIDRetries:  15, //nolint:mnd
		retryBackoffFn: retryBackoff,
	}
	h.signBatchFn = h.defaultSignBatch
	h.verifyBatchSigFn = node.VerifyBatchSignature
	h.collectDocCIDsFn = h.defaultCollectDocCIDs
	return h, nil
}

// SetNodeIdentity sets the node identity used for block signing.
func (h *BlockHandler) SetNodeIdentity(id identity.Identity) {
	h.nodeIdentity = id
}

// defaultSignBatch signs the collected CIDs using the node identity.
func (h *BlockHandler) defaultSignBatch(ctx context.Context, collector *node.BatchCIDCollector) (*node.BatchSignature, error) {
	nodeIdent := h.nodeIdentity
	if nodeIdent == nil {
		id, ok := defracontext.IdentityFrom(ctx)
		if !ok {
			return nil, errNoIdentity
		}
		nodeIdent = id
	}
	fullIdent, ok := nodeIdent.(identity.FullIdentity)
	if !ok {
		return nil, fmt.Errorf("identity is not a FullIdentity") //nolint:err113
	}

	cids := collector.GetCIDs()
	merkleRoot := node.ComputeMerkleRoot(cids)

	sigValue, err := fullIdent.PrivateKey().Sign(merkleRoot)
	if err != nil {
		return nil, fmt.Errorf("sign merkle root: %w", err)
	}

	var sigType string
	switch fullIdent.PrivateKey().Type() { //nolint:exhaustive
	case crypto.KeyTypeSecp256k1:
		sigType = "ES256K"
	case crypto.KeyTypeEd25519:
		sigType = "EdDSA"
	default:
		return nil, fmt.Errorf("unsupported key type: %v", fullIdent.PrivateKey().Type()) //nolint:err113
	}

	sig := &node.BatchSignature{}
	sig.Header.Type = sigType
	sig.Header.Identity = []byte(fullIdent.PublicKey().String())
	sig.Value = sigValue
	sig.MerkleRoot = merkleRoot
	sig.CIDCount = len(cids)
	return sig, nil
}

// buildDocIDJSONArray builds a JSON array string from docIDs for use in GQL filters.
func buildDocIDJSONArray(docIDs []string) string {
	var idsJSON strings.Builder
	idsJSON.WriteString(`[`)
	for i, id := range docIDs {
		if i > 0 {
			idsJSON.WriteString(",")
		}
		idsJSON.WriteString(`"` + id + `"`)
	}
	idsJSON.WriteString(`]`)
	return idsJSON.String()
}

// extractCIDsFromCollection queries a single collection and returns all CIDs found.
func (h *BlockHandler) extractCIDsFromCollection(ctx context.Context, colName, idsJSON string) []cid.Cid {
	query := `query { ` + colName + `(filter: {_docID: {_in: ` + idsJSON + `}}) { _version { cid } } }`
	result := h.db.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return nil
	}
	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return nil
	}
	var docMaps []map[string]any
	switch v := data[colName].(type) {
	case []any:
		for _, d := range v {
			if m, ok := d.(map[string]any); ok {
				docMaps = append(docMaps, m)
			}
		}
	case []map[string]any:
		docMaps = v
	}
	var cids []cid.Cid
	for _, docMap := range docMaps {
		var versions []map[string]any
		switch v := docMap["_version"].(type) {
		case []any:
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					versions = append(versions, m)
				}
			}
		case []map[string]any:
			versions = v
		}
		for _, vMap := range versions {
			cidStr, _ := vMap["cid"].(string)
			if cidStr == "" {
				continue
			}
			c, err := cid.Decode(cidStr)
			if err == nil {
				cids = append(cids, c)
			}
		}
	}
	return cids
}

// defaultCollectDocCIDs queries each collection via GQL to retrieve CIDs for the given docIDs.
func (h *BlockHandler) defaultCollectDocCIDs(ctx context.Context, docIDs []string, collectionNames []string) ([]cid.Cid, error) {
	if len(docIDs) == 0 {
		return nil, nil
	}

	idsJSON := buildDocIDJSONArray(docIDs)

	var allCIDs []cid.Cid
	for _, colName := range collectionNames {
		allCIDs = append(allCIDs, h.extractCIDsFromCollection(ctx, colName, idsJSON)...)
	}
	return allCIDs, nil
}

// sortedCIDStrings returns a sorted slice of CID strings.
func sortedCIDStrings(cids []cid.Cid) []string {
	out := make([]string, len(cids))
	for i, c := range cids {
		out[i] = c.String()
	}
	sort.Strings(out)
	return out
}

// SetDocIDTracker sets the tracker for recording docIDs at insert time.
func (h *BlockHandler) SetDocIDTracker(tracker DocIDTrackerInterface) {
	h.docIDTracker = tracker
}

// toInt64 extracts an int64 from a map value that may be int64, int, or float64.
func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case float64:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("expected numeric type, got %T", v) //nolint:err113
	}
}

// Store persists a block and all its constituent documents (transactions, logs,
// access-list entries, etc.) from the ConversionResult. It writes the block
// document first, then writes the remaining groups in order, resolving
// cross-document link fields (_blockID, _transactionID) via the
// chain-provided LinkStamper. The block signature is created over the
// collected CIDs when signing identity is available.
func (h *BlockHandler) Store(
	ctx context.Context,
	result chains.ConversionResult,
) (*BlockCreationResult, error) {
	if h.db == nil {
		return nil, errors.NewConfigurationError("defra", "Store",
			"store requires embedded DefraDB node", "", nil)
	}
	if len(result.Groups) == 0 {
		return nil, fmt.Errorf("no document groups to store") //nolint:err113
	}

	blockGroup := result.Groups[0]
	if len(blockGroup.Docs) == 0 {
		return nil, fmt.Errorf("no block document in groups") //nolint:err113
	}
	blockData := blockGroup.Docs[0]
	blockInt, err := toInt64(blockData[blockGroup.BlockNumField])
	if err != nil {
		return nil, fmt.Errorf("invalid block number: %w", err) //nolint:err113
	}
	blockHash, _ := blockData[constants.HashKeyValue].(string)

	collector := node.NewBatchCIDCollector()
	ctx = node.ContextWithBatchSigning(ctx, collector)

	blockID, err := h.storeBlockDoc(ctx, blockData, blockGroup.Collection)
	if err != nil {
		return nil, err
	}

	if result.LinkStamper != nil {
		result.LinkStamper.StampLinks(result.Groups, blockGroup.Collection, blockGroup.Docs, []string{blockID})
	}

	allDocIDs := []string{blockID}
	otherDocIDs := map[string][]string{}
	var batchErrors []error

	for _, g := range result.Groups[1:] {
		if result.LinkStamper != nil {
			result.LinkStamper.StampLinks(result.Groups, g.Collection, g.Docs, nil)
		}

		ids, err := h.writeGroup(ctx, blockInt, g)
		if err != nil {
			batchErrors = append(batchErrors, err)
		}

		if result.LinkStamper != nil {
			result.LinkStamper.StampLinks(result.Groups, g.Collection, g.Docs, ids)
		}

		otherDocIDs[g.Collection] = append(otherDocIDs[g.Collection], ids...)
		allDocIDs = append(allDocIDs, ids...)
	}

	blockSigDocID := h.signStoredBlock(ctx, blockInt, blockHash, allDocIDs, batchErrors, result.SignatureCollection, collector)

	creationResult := &BlockCreationResult{
		BlockNumber:              blockInt,
		BlockID:                  blockID,
		BlockSignatureID:         blockSigDocID,
		BlockSignatureCollection: result.SignatureCollection,
		OtherDocIDs:              otherDocIDs,
	}

	if h.docIDTracker != nil {
		if err := h.docIDTracker.TrackBlock(ctx, blockInt, creationResult); err != nil {
			logger.Sugar.Warnf("Failed to track docIDs for block %d: %v", blockInt, err)
		}
	}

	if len(batchErrors) > 0 {
		return creationResult, fmt.Errorf("block %d partially indexed with %d batch errors (first: %w)", //nolint:err113
			blockInt, len(batchErrors), batchErrors[0])
	}

	return creationResult, nil
}

// writeGroup writes a DocumentGroup's docs in batches, returning all docIDs.
func (h *BlockHandler) writeGroup(ctx context.Context, blockInt int64, g chains.DocumentGroup) ([]string, error) {
	batchSize := g.BatchSize
	if batchSize <= 0 {
		batchSize = h.maxDocsPerTxn
	}
	return h.createDocBatch(ctx, blockInt, g.Collection, g.Docs, batchSize)
}

func (h *BlockHandler) signStoredBlock(
	ctx context.Context,
	blockInt int64,
	blockHash string,
	allDocIDs []string,
	batchErrors []error,
	signatureCollection string,
	collector *node.BatchCIDCollector,
) string {
	for _, be := range batchErrors {
		logger.Sugar.Warnf("Block %d: batch write error: %v", blockInt, be)
	}

	if len(batchErrors) > 0 {
		logger.Sugar.Warnf("Block %d: not signing partial block: %d docs, %d batch errors",
			blockInt, len(allDocIDs), len(batchErrors))
		return ""
	}

	cids := collector.GetCIDs()
	if len(cids) != len(allDocIDs) {
		logger.Sugar.Warnf("Block %d: not signing, collected %d CIDs for %d documents",
			blockInt, len(cids), len(allDocIDs))
		return ""
	}

	sigID, sigErr := h.signBlockOverCIDs(ctx, blockInt, blockHash, len(allDocIDs), cids, signatureCollection)
	if sigErr != nil {
		logger.Sugar.Warnf("Block %d: signing failed: %v", blockInt, sigErr)
		return ""
	}
	return sigID
}

// storeBlockDoc creates the block document in its own transaction.
func (h *BlockHandler) storeBlockDoc(ctx context.Context, blockData map[string]any, blockColName string) (string, error) {
	txn, err := h.db.NewTxn(false)
	if err != nil {
		return "", fmt.Errorf("create txn for block: %w", err) //nolint:err113
	}

	col, err := txn.GetCollectionByName(ctx, blockColName)
	if err != nil {
		txn.Discard()
		return "", fmt.Errorf("get block collection: %w", err) //nolint:err113
	}

	doc, err := client.NewDocFromMap(ctx, blockData, col.Version())
	if err != nil {
		txn.Discard()
		return "", fmt.Errorf("build block document: %w", err) //nolint:err113
	}

	if err := col.AddDocument(ctx, doc); err != nil {
		txn.Discard()
		if errors.IsErrAlreadyExists(err) {
			return "", fmt.Errorf("block already exists") //nolint:err113
		}
		return "", fmt.Errorf("create block: %w", err) //nolint:err113
	}

	blockID := doc.ID().String()

	if err := txn.Commit(); err != nil {
		return "", fmt.Errorf("commit block: %w", err) //nolint:err113
	}

	return blockID, nil
}

// createDocBatch writes documents in batches of batchSize, returning all docIDs.
func (h *BlockHandler) createDocBatch(
	ctx context.Context,
	blockInt int64,
	colName string,
	dataMaps []map[string]any,
	batchSize int,
) ([]string, error) {
	var allIDs []string
	for i := 0; i < len(dataMaps); i += batchSize {
		end := min(i+batchSize, len(dataMaps))
		batch := dataMaps[i:end]
		if len(batch) == 0 {
			continue
		}
		var ids []string
		err := h.writeBatchWithRetry(ctx, blockInt, colName, func() error {
			var e error
			ids, e = h.createDocsInTxn(ctx, colName, batch)
			return e
		})
		allIDs = append(allIDs, ids...)
		if err != nil {
			return allIDs, err
		}
	}
	return allIDs, nil
}

// createDocsInTxn creates documents from data maps in a single transaction.
func (h *BlockHandler) createDocsInTxn(
	ctx context.Context,
	colName string,
	dataMaps []map[string]any,
) ([]string, error) {
	txn, err := h.db.NewTxn(false)
	if err != nil {
		return nil, fmt.Errorf("create txn for %s: %w", colName, err) //nolint:err113
	}

	col, err := txn.GetCollectionByName(ctx, colName)
	if err != nil {
		txn.Discard()
		return nil, fmt.Errorf("get collection %s: %w", colName, err) //nolint:err113
	}

	docs := make([]*client.Document, 0, len(dataMaps))
	for _, data := range dataMaps {
		doc, err := client.NewDocFromMap(ctx, data, col.Version())
		if err != nil {
			txn.Discard()
			return nil, fmt.Errorf("create doc in %s: %w", colName, err) //nolint:err113
		}
		docs = append(docs, doc)
	}

	if len(docs) == 0 {
		txn.Discard()
		return nil, nil
	}

	if err := col.AddManyDocuments(ctx, docs); err != nil {
		txn.Discard()
		if errors.IsErrAlreadyExists(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("add documents to %s: %w", colName, err) //nolint:err113
	}

	if err := txn.Commit(); err != nil {
		return nil, fmt.Errorf("commit %s batch: %w", colName, err) //nolint:err113
	}

	ids := make([]string, len(docs))
	for i, doc := range docs {
		ids[i] = doc.ID().String()
	}
	return ids, nil
}

// SignExisting creates a block signature for a block that has already been
// stored. It collects the stored docIDs by querying each group's collection
// for the given block number, waits for CIDs, then signs over them.
//
// The result.Groups identify which collections to query (typically the same
// groups produced by Convert, minus the signature group).
// result.SignatureCollection names the collection where the block signature
// document will be stored.
func (h *BlockHandler) SignExisting(
	ctx context.Context,
	result chains.ConversionResult,
	blockHash string,
	blockNumber int64,
) (string, error) {
	if h.db == nil {
		return "", fmt.Errorf("defraNode is nil") //nolint:err113
	}

	var allDocIDs []string
	var collectionNames []string
	for _, g := range result.Groups {
		field := g.BlockNumField
		if field == "" {
			field = constants.BlockNumberKeyValue
		}
		docIDs, err := h.queryCollectionDocIDs(ctx, g.Collection, field, blockNumber, blockNumber)
		if err != nil {
			return "", fmt.Errorf("query docIDs for %s: %w", g.Collection, err) //nolint:err113
		}
		allDocIDs = append(allDocIDs, docIDs...)
		collectionNames = append(collectionNames, g.Collection)
	}

	cids, err := h.waitForCIDs(ctx, blockNumber, allDocIDs, collectionNames)
	if err != nil {
		return "", err
	}

	return h.signBlockOverCIDs(ctx, blockNumber, blockHash, len(allDocIDs), cids, result.SignatureCollection)
}

// buildBlockSignatureDocument creates a client.Document for a block signature.
func (h *BlockHandler) buildBlockSignatureDocument(ctx context.Context, blockSig *node.BatchSignature, blockHash string, blockNumber int64, col client.Collection, sortedCIDStrings []string) (*client.Document, error) {
	data := map[string]any{
		constants.BlockNumberKeyValue: blockNumber,
		constants.BlockHashKeyValue:   blockHash,
		"merkleRoot":                  hex.EncodeToString(blockSig.MerkleRoot),
		"cidCount":                    blockSig.CIDCount,
		"cids":                        sortedCIDStrings,
		"signatureType":               blockSig.Header.Type,
		"signatureIdentity":           string(blockSig.Header.Identity),
		"signatureValue":              hex.EncodeToString(blockSig.Value),
		"createdAt":                   time.Now().UTC().Format(time.RFC3339),
	}
	return client.NewDocFromMap(ctx, data, col.Version())
}

// waitForCIDs collects the CIDs for allDocIDs, retrying while they are still arriving (P2P data
// can lag). It returns the CIDs only once every document has one; partial coverage is an error so
// a signature is never made over a subset of the block.
func (h *BlockHandler) waitForCIDs(ctx context.Context, blockNumber int64, allDocIDs []string, collectionNames []string) ([]cid.Cid, error) {
	maxRetries := h.maxCIDRetries
	var lastCIDCount int
	var lastErr error

	for attempt := range maxRetries {
		cids, err := h.collectDocCIDsFn(ctx, allDocIDs, collectionNames)
		if err != nil {
			lastErr = err
			logger.Sugar.Warnf("Block %d: CID query failed (attempt %d/%d): %v", blockNumber, attempt+1, maxRetries, err)
			if attempt < maxRetries-1 {
				time.Sleep(h.retryBackoffFn(attempt))
			}
			continue
		}

		lastCIDCount = len(cids)
		if len(cids) >= len(allDocIDs) {
			return cids, nil
		}

		lastErr = fmt.Errorf("got %d CIDs for %d docs", len(cids), len(allDocIDs)) //nolint:err113
		if attempt < maxRetries-1 {
			logger.Sugar.Debugf("Block %d: waiting for P2P data (%d/%d CIDs, attempt %d/%d)",
				blockNumber, len(cids), len(allDocIDs), attempt+1, maxRetries)
			time.Sleep(h.retryBackoffFn(attempt))
		}
	}

	if lastCIDCount == 0 {
		return nil, fmt.Errorf("no CIDs found for block %d after %d retries (%d docs): %w", //nolint:err113
			blockNumber, maxRetries, len(allDocIDs), lastErr)
	}
	return nil, fmt.Errorf("incomplete CID coverage for block %d after %d retries (%d/%d docs): %w", //nolint:err113
		blockNumber, maxRetries, lastCIDCount, len(allDocIDs), lastErr)
}

// signBlockOverCIDs signs the block over cids and stores the signature, returning its document id.
// docCount is the number of documents the CIDs cover and is used only for logging.
// signatureCollection names the collection where the signature document is stored.
func (h *BlockHandler) signBlockOverCIDs(ctx context.Context, blockNumber int64, blockHash string, docCount int, cids []cid.Cid, signatureCollection string) (string, error) {
	collector := node.NewBatchCIDCollector()
	for _, c := range cids {
		collector.Add(c)
	}

	blockSig, err := h.signBatchFn(ctx, collector)
	if err != nil {
		return "", fmt.Errorf("failed to sign block: %w", err) //nolint: err113
	}
	if blockSig == nil {
		return "", fmt.Errorf("signing returned nil (no identity?)") //nolint: err113
	}

	// The signature must verify over the CIDs it is about to attest. A failure means signing
	// produced an inconsistent signature, so it is not stored.
	if valid, verifyErr := h.verifyBatchSigFn(blockSig, cids); verifyErr != nil {
		return "", fmt.Errorf("verify block signature: %w", verifyErr) //nolint: err113
	} else if !valid {
		return "", fmt.Errorf("block signature failed self-verification") //nolint: err113
	}

	sigTxn, err := h.db.NewTxn(false)
	if err != nil {
		return "", fmt.Errorf("failed to create signing transaction: %w", err) //nolint: err113
	}

	colBlockSig, err := sigTxn.GetCollectionByName(ctx, signatureCollection)
	if err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to get block signature collection: %w", err) //nolint: err113
	}

	sortedCIDs := sortedCIDStrings(cids)
	blockSigDoc, err := h.buildBlockSignatureDocument(ctx, blockSig, blockHash, blockNumber, colBlockSig, sortedCIDs)
	if err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to build block signature document: %w", err) //nolint: err113
	}

	if err := colBlockSig.AddDocument(ctx, blockSigDoc); err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to create block signature document: %w", err) //nolint: err113
	}

	if err := sigTxn.Commit(); err != nil {
		return "", fmt.Errorf("failed to commit block signature: %w", err) //nolint: err113
	}

	docID := blockSigDoc.ID().String()
	logger.Sugar.Infof("Block %d: signed (%d docs, %d CIDs, identity: %s...)",
		blockNumber, docCount, len(cids), truncate(string(blockSig.Header.Identity), 16)) //nolint:mnd

	return docID, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

const (
	// maxBatchRetries caps how many times a batch write is attempted when it hits a transaction
	// conflict. Document creates share the /seq/doc sequence, so concurrent writers collide; a
	// retry rebuilds and re-commits the batch.
	maxBatchRetries = 3
	// batchConflictRetryDelay is the base delay between batch retries; the wait grows linearly with
	// the attempt (50ms, 100ms, ...).
	batchConflictRetryDelay = 50 * time.Millisecond
)

// writeBatchWithRetry runs write, retrying on a transaction conflict up to maxBatchRetries with
// backoff. A discarded attempt's collected CIDs are rolled back so the collector reflects only
// committed writes. kind names the batch for the retry log.
func (h *BlockHandler) writeBatchWithRetry(ctx context.Context, blockInt int64, kind string, write func() error) error {
	collector := node.BatchSigningCollectorFromContext(ctx)
	for attempt := range maxBatchRetries {
		mark := 0
		if collector != nil {
			mark = collector.Len()
		}
		err := write()
		if err == nil {
			return nil
		}
		if collector != nil {
			collector.Truncate(mark)
		}
		if !errors.IsErrTransactionConflict(err) {
			return err
		}
		if attempt == maxBatchRetries-1 {
			logger.Sugar.Warnf("Block %d: %s batch still conflicting after %d attempts, giving up", blockInt, kind, maxBatchRetries)
			return err
		}
		logger.Sugar.Infof("Block %d: %s batch conflict, retrying (attempt %d/%d)", blockInt, kind, attempt+1, maxBatchRetries)
		time.Sleep(time.Duration(attempt+1) * batchConflictRetryDelay)
	}
	return nil
}

// queryCollectionDocIDs queries a single collection for all document IDs
// whose block-number field falls in [from, to] inclusive. It uses chunked
// _geq/_leq GraphQL range filters, matching the snapshotter's queryDocIDs
// pattern. When from == to, a single _geq/_leq query is issued (equivalent
// to _eq).
func (h *BlockHandler) queryCollectionDocIDs(ctx context.Context, colName, field string, from, to int64) ([]string, error) {
	var allDocIDs []string
	const chunkSize = 100

	for chunkStart := from; chunkStart <= to; chunkStart += chunkSize {
		chunkEnd := chunkStart + chunkSize - 1
		chunkEnd = min(chunkEnd, to)

		query := fmt.Sprintf(
			`query { %s(filter: {%s: {_geq: %d, _leq: %d}}) { _docID } }`,
			colName, field, chunkStart, chunkEnd,
		)

		result := h.db.ExecRequest(ctx, query)
		if len(result.GQL.Errors) > 0 {
			return nil, fmt.Errorf("query %s [%d-%d]: %w", colName, chunkStart, chunkEnd, result.GQL.Errors[0])
		}

		data, ok := result.GQL.Data.(map[string]any)
		if !ok {
			continue
		}

		raw := data[colName]
		if raw == nil {
			continue
		}

		var docs []any
		switch typed := raw.(type) {
		case []any:
			docs = typed
		case []map[string]any:
			docs = make([]any, len(typed))
			for i, d := range typed {
				docs[i] = d
			}
		default:
			continue
		}

		for _, doc := range docs {
			m, ok := doc.(map[string]any)
			if !ok {
				continue
			}
			if docID, ok := m["_docID"].(string); ok {
				allDocIDs = append(allDocIDs, docID)
			}
		}
	}

	return allDocIDs, nil
}
