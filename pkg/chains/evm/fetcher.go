package evm

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
	codecevm "github.com/shinzonetwork/shinzo-evidence-codec/evm"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	shinzoerrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
)

// rpcClient abstracts the subset of *rpc.EthereumClient methods used by the
// fetcher. It exists so tests can inject a lightweight fake without dialing a
// real RPC endpoint. *rpc.EthereumClient satisfies this interface.
type rpcClient interface {
	GetLatestBlockNumber(ctx context.Context) (*big.Int, error)
	GetBlockByNumber(ctx context.Context, blockNumber *big.Int) (*Block, error)
	// GetHeaderByHash fetches only the header of the by-hash block (no
	// transaction bodies requested or decoded) for the walk-back descent.
	GetHeaderByHash(ctx context.Context, hash string) (*ethtypes.Header, error)
	// GetBlockReceipts fetches all receipts for a block in a single RPC
	// call, selected by the block's content hash so the receipts always
	// stay on the branch of the block the fetcher already holds.
	GetBlockReceipts(ctx context.Context, blockHash string) ([]*TransactionReceipt, error)
	GetTransactionReceipt(ctx context.Context, txHash string) (*TransactionReceipt, error)
	GetFinalizedBlockNumber(ctx context.Context) (int64, error)
	// GetFinalizedHeight returns the finalized tag's height, with no fallback.
	GetFinalizedHeight(ctx context.Context) (int64, error)
	// GetProof returns the node's eth_getProof answer for account's slots at the block with blockHash.
	GetProof(ctx context.Context, account common.Address, slots []string, blockHash string) (codecevm.AccountProof, error)
	Close() error
}

// BlockBundle is the concrete return type of Fetcher.FetchBlock. It bundles
// the raw block data with its transactions and receipts. The type is exported
// so other packages can construct it directly in tests (converter fixtures in
// pkg/defra and pkg/snapshot); in production it stays behind the `any` return
// of FetchBlock and is type-asserted only by the converter in this package.
type BlockBundle struct {
	Block        *Block
	Transactions []*Transaction
	Receipts     []*TransactionReceipt
	// StateCaptures holds, in tokens mode at a capture height, each listed token's proven total
	// supply. A token whose proof failed every attempt is missing.
	StateCaptures []StateCapture
}

// StateCapture is a token's total supply at a block, with the proof that showed it.
type StateCapture struct {
	Asset string   // the token's address as 40 lower-case hex digits
	Value *big.Int // the supply slot's value
	Proof []byte   // the eth_getProof answer as the evidence codec encodes it
}

// supplySlot is a listed token and the storage slot of its total supply.
type supplySlot struct {
	asset    string // 40 lower-case hex digits
	contract common.Address
	slot     string      // as configured, the form eth_getProof is asked for
	key      common.Hash // the same slot as a 32-byte key, the form a proof is checked against
}

// captureAttempts is how many times a token's supply proof is tried at a capture height, with no
// delay between attempts. A token whose proof fails every attempt has no capture at that height, and
// nothing proves it later.
const captureAttempts = 3

// errInvalidSupplyProof wraps a node's eth_getProof answer that does not prove a token's supply
// against the header's state root.
var errInvalidSupplyProof = errors.New("supply proof does not verify")

// Fetcher is the RPC I/O layer for EVM chains. It fetches raw block data
// (block + transactions + receipts) from the chain node and queries the
// on-chain tip. It implements chains.Fetcher.
//
// Fetcher is safe for concurrent use across different block heights; the
// orchestration layer is responsible for parallel fan-out.
type Fetcher struct {
	client         rpcClient
	receiptWorkers int

	// Connection-config fields populated by NewFetcherFromConfig. When
	// non-empty, Connect(ctx) dials the RPC endpoint using these values.
	// dialTimeout bounds the dial phase when positive; non-positive values
	// leave the dial unbounded (the caller's context governs). The low-level
	// NewFetcher constructor sets client directly and leaves these blank,
	// making Connect a no-op.
	nodeURL     string
	wsURL       string
	apiKey      string
	apiKeyType  string
	dialTimeout time.Duration
	// chainID is the chain ID Connect requires the endpoint to report.
	chainID uint64
	// tokensMode is set for a tokens-mode chain. FetchBlock checks the decoded header's hash with
	// the node and the block's transactions and receipts against the header's roots, and
	// FetchFinalizedBlockNumber reports the finalized tag only, without the safe fallback.
	tokensMode bool
	// supplySlots and captureInterval are set in tokens mode: FetchBlock asks for a proof of each
	// token's total supply at every block whose number is a multiple of captureInterval. supplyProofs
	// counts each proof attempt by result, and suppliesMissed each token left without a capture.
	supplySlots     []supplySlot
	captureInterval int64
	supplyProofs    metric.Int64Counter
	suppliesMissed  metric.Int64Counter
}

// Compile-time guarantee that Fetcher implements chains.Fetcher and chains.StateProver.
var (
	_ chains.Fetcher     = (*Fetcher)(nil)
	_ chains.StateProver = (*Fetcher)(nil)
)

// NewFetcher creates an Fetcher wrapping the given RPC client. The
// receiptWorkers parameter controls concurrency for individual receipt fetches
// when the batch call is unavailable (fallback path).
func NewFetcher(client rpcClient, receiptWorkers int) *Fetcher {
	return &Fetcher{
		client:         client,
		receiptWorkers: receiptWorkers,
	}
}

// NewFetcherFromConfig creates a Fetcher from the given config without dialing
// the RPC endpoint. Call Connect(ctx) to establish the connection before using
// FetchBlock/FetchHighestBlockNumber.
func NewFetcherFromConfig(cfg *config.Config) (*Fetcher, error) {
	if cfg == nil {
		return nil, shinzoerrors.NewConfigurationError("chain", "NewFetcherFromConfig", "config is nil", "", nil)
	}
	receiptWorkers := cfg.Indexer.ReceiptWorkers
	if receiptWorkers <= 0 {
		receiptWorkers = 16 //nolint:mnd
	}
	f := &Fetcher{
		nodeURL:        cfg.Geth.NodeURL,
		wsURL:          cfg.Geth.WsURL,
		apiKey:         cfg.Geth.APIKey,
		apiKeyType:     cfg.Geth.APIKeyType,
		dialTimeout:    time.Duration(cfg.Geth.DialTimeoutSeconds) * time.Second,
		chainID:        cfg.Chain.ChainID,
		tokensMode:     cfg.Chain.Mode == config.ChainModeTokens,
		receiptWorkers: receiptWorkers,
	}
	if f.tokensMode {
		f.captureInterval = cfg.Chain.StateCaptureInterval
		meter := otel.Meter("github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm")
		var err error
		f.supplyProofs, err = meter.Int64Counter("shinzo.generator.supply.proofs",
			metric.WithDescription("Supply proof attempts at capture heights, by result: verified, rpc_error or invalid_proof. An attempt cut short by a stopping generator is not counted."),
			metric.WithUnit("{attempt}"),
		)
		if err != nil {
			otel.Handle(err)
		}
		f.suppliesMissed, err = meter.Int64Counter("shinzo.generator.supply.missed",
			metric.WithDescription("Tokens left without a supply capture at a capture height after every attempt failed."),
			metric.WithUnit("{capture}"),
		)
		if err != nil {
			otel.Handle(err)
		}
		for _, t := range cfg.Chain.Tokens {
			asset, err := config.ContractAddress(t.Address)
			if err != nil {
				return nil, err
			}
			key, err := codecevm.ParseStorageKey(t.SupplySlot)
			if err != nil {
				return nil, fmt.Errorf("token %s supply slot: %w", asset, err)
			}
			f.supplySlots = append(f.supplySlots, supplySlot{
				asset:    asset,
				contract: common.HexToAddress(asset),
				slot:     t.SupplySlot,
				key:      common.Hash(key),
			})
		}
		slices.SortFunc(f.supplySlots, func(a, b supplySlot) int { return strings.Compare(a.asset, b.asset) })
	}
	return f, nil
}

// errChainIDMismatch reports that the endpoint serves a chain other than the configured one.
var errChainIDMismatch = errors.New("node serves another chain")

// Connect dials the RPC endpoint using the connection-config fields and returns
// errChainIDMismatch when the endpoint reports a chain ID other than the
// configured one, so a generator pointed at another chain's RPC does not index
// that chain under its own collection names. The provided context governs the
// dial; f.dialTimeout (GethConfig's dial_timeout_seconds) may additionally bound
// the WebSocket dial phase. If the fetcher was built via NewFetcher
// (pre-connected client), Connect is a no-op.
func (f *Fetcher) Connect(ctx context.Context) error {
	if f.client != nil {
		return nil
	}
	client, err := NewEthereumClient(ctx, f.nodeURL, f.wsURL, f.apiKey, f.apiKeyType, f.dialTimeout)
	if err != nil {
		return fmt.Errorf("create ethereum client: %w", err)
	}
	chainID, err := client.GetChainID(ctx)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("read chain id: %w", err)
	}
	if !chainID.IsUint64() || chainID.Uint64() != f.chainID {
		_ = client.Close()
		return fmt.Errorf("endpoint reports chain id %s: %w", chainID, errChainIDMismatch)
	}
	f.client = client
	return nil
}

// FetchBlock implements chains.Fetcher. It retrieves the block at the given
// height along with its transactions and receipts, returning them bundled in a
// *BlockBundle (typed as any per the interface). The receipts are pinned to
// the fetched block's own hash, so a reorg between the block fetch and the
// receipt fetch cannot serve receipts from the new chain's block. In tokens mode
// it also checks the block's transactions and receipts against the header's
// roots.
func (f *Fetcher) FetchBlock(ctx context.Context, height int64) (any, error) {
	if f.client == nil {
		return nil, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchBlock")
	}
	block, err := f.fetchBlock(ctx, height)
	if err != nil {
		return nil, err
	}
	transactions, receipts, err := f.fetchTransactionsAndReceipts(ctx, block, height)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch block %d receipts: %w", height, err)
	}
	// block.Hash is computed from the decoded header, so a dropped header field changes it. The
	// receipt fetch checks it for a block with transactions; tokens mode checks one without by
	// fetching the header by that hash.
	if f.tokensMode && len(block.Transactions) == 0 {
		if _, err := f.client.GetHeaderByHash(ctx, block.Hash); err != nil {
			return nil, fmt.Errorf("block %d: node has no header with hash %s: %w", height, block.Hash, err)
		}
	}
	bundle := &BlockBundle{
		Block:        block,
		Transactions: transactions,
		Receipts:     receipts,
	}
	if f.tokensMode {
		if err := checkRoots(block, receipts); err != nil {
			return nil, fmt.Errorf("block %d: %w", height, err)
		}
		if f.captureInterval > 0 && height%f.captureInterval == 0 {
			if bundle.StateCaptures, err = f.captureSupply(ctx, height, block); err != nil {
				return nil, err
			}
		}
	}
	return bundle, nil
}

// captureSupply returns the verified total-supply captures of the listed tokens at block, leaving
// out a token whose proof fails captureAttempts times. It fails only when ctx ends, so a stopping
// generator does not return a block missing captures it could have made.
func (f *Fetcher) captureSupply(ctx context.Context, height int64, block *Block) ([]StateCapture, error) {
	var captures []StateCapture
	for _, s := range f.supplySlots {
		var capture StateCapture
		var err error
		for range captureAttempts {
			capture, err = f.proveSupply(ctx, block, s)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			f.supplyProofs.Add(ctx, 1, metric.WithAttributes(attribute.String("result", supplyProofResult(err))))
			if err == nil {
				break
			}
		}
		if err != nil {
			f.suppliesMissed.Add(ctx, 1)
			logger.Sugar.Warnf("Block %d: no supply capture for %s after %d attempts: %v", height, s.asset, captureAttempts, err)
			continue
		}
		captures = append(captures, capture)
	}
	return captures, nil
}

// supplyProofResult names the result of one supply proof attempt for the result attribute of
// shinzo.generator.supply.proofs.
func supplyProofResult(err error) string {
	switch {
	case err == nil:
		return "verified"
	case errors.Is(err, errInvalidSupplyProof):
		return "invalid_proof"
	default:
		return "rpc_error"
	}
}

// proveSupply asks the node for s's supply slot at block, by the block's hash, and checks the proof
// against the header's state root.
func (f *Fetcher) proveSupply(ctx context.Context, block *Block, s supplySlot) (StateCapture, error) {
	answer, err := f.client.GetProof(ctx, s.contract, []string{s.slot}, block.Hash)
	if err != nil {
		return StateCapture{}, err
	}
	proof, err := codecevm.EncodeStateProof(answer)
	if err != nil {
		return StateCapture{}, fmt.Errorf("%w: %w", errInvalidSupplyProof, err)
	}
	value, err := codecevm.VerifyStateCapture(block.Header.Root, s.contract, s.key, proof)
	if err != nil {
		return StateCapture{}, fmt.Errorf("%w: %w", errInvalidSupplyProof, err)
	}
	return StateCapture{Asset: s.asset, Value: value, Proof: proof}, nil
}

// ProveState implements chains.StateProver. It reads the block's header by hash for its number, and
// the node's eth_getProof answer for contract's slots at the block, also by hash. Errors carry no
// endpoint credentials.
func (f *Fetcher) ProveState(ctx context.Context, blockHash, contract string, slots []string) (int64, []byte, error) {
	if f.client == nil {
		return 0, nil, fmt.Errorf("fetcher not connected: call Connect(ctx) before ProveState")
	}
	header, err := f.client.GetHeaderByHash(ctx, blockHash)
	if err != nil {
		return 0, nil, utils.RedactHTTPError(err)
	}
	answer, err := f.client.GetProof(ctx, common.HexToAddress(contract), slots, blockHash)
	if err != nil {
		return 0, nil, err
	}
	proof, err := codecevm.EncodeStateProof(answer)
	if err != nil {
		return 0, nil, err
	}
	return header.Number.Int64(), proof, nil
}

// FetchHighestBlockNumber implements chains.Fetcher. It queries the RPC
// endpoint for the latest block number.
func (f *Fetcher) FetchHighestBlockNumber(ctx context.Context) (int64, error) {
	if f.client == nil {
		return 0, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchHighestBlockNumber")
	}
	n, err := f.client.GetLatestBlockNumber(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get latest block number: %w", err)
	}
	return n.Int64(), nil
}

// FetchBlockHeaderByHash implements chains.Fetcher. It retrieves only the
// header fields (height, content hash, parent hash) of the block with the
// given 0x-prefixed content hash through a header-only by-hash RPC: no
// transaction bodies are requested or decoded, so the reorg walk-back pays
// one header per descent step instead of a full block download and cannot
// trip the typed transaction decoder on chains with custom transaction
// types. The returned hash echoes the requested hash: a locally recomputed
// header.Hash() can differ from the node's hash on chains with extra header
// fields. Not-found errors from the client keep their message, so
// pkg/errors.IsErrNotFound continues to classify them for the shared retry
// logic.
func (f *Fetcher) FetchBlockHeaderByHash(ctx context.Context, hash string) (chains.BlockHeader, error) {
	if f.client == nil {
		return chains.BlockHeader{}, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchBlockHeaderByHash")
	}
	header, err := f.client.GetHeaderByHash(ctx, hash)
	if err != nil {
		return chains.BlockHeader{}, err
	}
	return chains.BlockHeader{
		Number:     header.Number.Int64(),
		Hash:       hash, // the hash we asked for; a recomputed header.Hash() can differ on chains with extra header fields
		ParentHash: header.ParentHash.Hex(),
	}, nil
}

// FetchFinalizedBlockNumber implements chains.Fetcher. It returns the
// finalized chain height (the client falls back to the safe height when the
// finalized tag is unavailable, except in tokens mode), used as the floor of
// the reorg walk-back and, in tokens mode, as the dispatch ceiling.
func (f *Fetcher) FetchFinalizedBlockNumber(ctx context.Context) (int64, error) {
	if f.client == nil {
		return 0, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchFinalizedBlockNumber")
	}
	if f.tokensMode {
		return f.client.GetFinalizedHeight(ctx)
	}
	return f.client.GetFinalizedBlockNumber(ctx)
}

// Close implements chains.Fetcher. It closes the underlying RPC client.
func (f *Fetcher) Close() error {
	if f.client != nil {
		return f.client.Close()
	}
	return nil
}

// fetchBlock fetches a block by number, returning not-found errors as-is
// so the caller (block processor) can apply its own retry policy.
func (f *Fetcher) fetchBlock(ctx context.Context, blockNum int64) (*Block, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return f.client.GetBlockByNumber(ctx, big.NewInt(blockNum))
}

// fetchTransactionsAndReceipts builds the transaction pointer slice and
// fetches receipts, pinned to the block's own content hash so the receipts
// always belong to the block the caller holds, whichever branch it sits on
// (a by-number receipts call would race a reorg between the two reads).
// The batch path is verified: every served receipt must carry the pinned
// hash AND cover every transaction in the block. Any failure, including a
// hash mismatch or an incomplete batch, degrades to the per-tx fallback.
// eth_getTransactionReceipt resolves to the tx's canonical inclusion, so
// each fallback receipt is verified against the pinned hash too, and a
// mismatch fails the fetch instead of bundling cross-branch data.
func (f *Fetcher) fetchTransactionsAndReceipts(ctx context.Context, block *Block, blockNum int64) ([]*Transaction, []*TransactionReceipt, error) {
	transactions := make([]*Transaction, len(block.Transactions))
	for i := range block.Transactions {
		transactions[i] = &block.Transactions[i]
	}

	batchReceipts, batchErr := f.client.GetBlockReceipts(ctx, block.Hash)
	if batchErr == nil && receiptHashesMatch(batchReceipts, block.Hash) && batchCoversTransactions(transactions, batchReceipts) {
		mergeReceiptsOntoTransactions(transactions, batchReceipts)
		return transactions, batchReceipts, nil
	}
	if batchErr == nil {
		// The batch result was rejected: either the receipts came back
		// for a different block than the one the caller holds, or the
		// node served only some of the block's transactions. Treat it as
		// failed and fall back rather than bundling cross-branch content
		// or letting an unseen outcome default to success.
		if !receiptHashesMatch(batchReceipts, block.Hash) {
			batchErr = fmt.Errorf("served receipts do not belong to block hash %s", block.Hash)
		} else {
			batchErr = fmt.Errorf("served %d receipts for %d transactions", len(batchReceipts), len(block.Transactions))
		}
	}

	if ctx.Err() == nil {
		logger.Sugar.Debugf("Block %d: pinned eth_getBlockReceipts not usable, falling back to individual fetches: %v", blockNum, batchErr)
	}

	receipts := f.fetchReceiptsIndividually(ctx, block)

	var failed int
	for _, r := range receipts {
		if r == nil {
			failed++
		}
	}
	if failed > 0 {
		return transactions, receipts, fmt.Errorf("block %d: %d/%d transaction receipts could not be fetched", blockNum, failed, len(receipts))
	}

	mergeReceiptsOntoTransactions(transactions, receipts)
	return transactions, receipts, nil
}

// fetchReceiptsIndividually fetches one receipt per transaction, so the
// result slice is positional with len(block.Transactions) slots and nil
// slots mark transactions whose receipt could not be fetched. Receipts
// fetch concurrently, bounded by the configured worker count, and failures
// are only warned, never aborted, so every tx always gets its own attempt.
// eth_getTransactionReceipt resolves to the tx's canonical inclusion, so
// each receipt is verified against the pinned hash too, and a mismatch
// leaves the slot nil for the caller to fail on instead of bundling
// cross-branch data.
func (f *Fetcher) fetchReceiptsIndividually(ctx context.Context, block *Block) []*TransactionReceipt {
	receipts := make([]*TransactionReceipt, len(block.Transactions))
	var wg sync.WaitGroup
	sem := make(chan struct{}, f.receiptWorkers)

	for i, tx := range block.Transactions {
		wg.Add(1)
		go func(idx int, txHash string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			receipt, err := f.client.GetTransactionReceipt(ctx, txHash)
			if err != nil {
				if ctx.Err() == nil && logger.Sugar != nil {
					logger.Sugar.Warnf("Failed to fetch receipt for tx %s: %v", txHash, err)
				}
				return
			}
			if receipt == nil || !strings.EqualFold(receipt.BlockHash, block.Hash) {
				// The node resolved the tx to another block: ours is no
				// longer canonical. Leave the slot nil so the fetch fails
				// and retries.
				if ctx.Err() == nil && logger.Sugar != nil {
					logger.Sugar.Warnf("Receipt for tx %s does not belong to block %s; the block is no longer canonical", txHash, block.Hash)
				}
				return
			}
			receipts[idx] = receipt
		}(i, tx.Hash)
	}
	wg.Wait()

	return receipts
}

// mergeReceiptsOntoTransactions copies execution results from the receipts
// onto their transactions so the bundle carries outcome data, not only the
// signed payloads. Matching is by the transaction's own hash because both
// receipt paths key receipts that way. The batch path is accepted only when
// it covers every transaction, and a per-tx miss fails the fallback fetch,
// so a miss here is a defensive no-op (logged, fields untouched) rather than
// a behavior callers can rely on.
func mergeReceiptsOntoTransactions(txs []*Transaction, receipts []*TransactionReceipt) {
	byHash := make(map[string]*TransactionReceipt, len(receipts))
	for _, r := range receipts {
		if r != nil {
			byHash[r.TransactionHash] = r
		}
	}
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		r, ok := byHash[tx.Hash]
		if !ok || r == nil {
			// Both callers verify full coverage before merging, so a miss
			// means verified data and the receipt set disagree: surface it
			// instead of silently persisting conversion defaults.
			if logger.Sugar != nil {
				logger.Sugar.Warnf("No receipt matched transaction %s; keeping conversion defaults", tx.Hash)
			}
			continue
		}
		// The comparison targets the client's converted status format
		// (getReceiptStatus emits decimal "1"/"0"), not the wire's hex
		// quantity form.
		tx.Status = r.Status == "1"
		tx.GasUsed = r.GasUsed
		tx.CumulativeGasUsed = r.CumulativeGasUsed
		tx.EffectiveGasPrice = r.EffectiveGasPrice
	}
}

// errRootMismatch reports that the node's transactions or receipts for a block do not rebuild
// the root its header commits to.
var errRootMismatch = errors.New("node's answer does not match the header's root")

// checkRoots rebuilds the block's transactions and receipts roots from the node's answers and
// returns errRootMismatch when either differs from the header's, so a missing, extra or altered
// transaction or receipt fails the fetch.
func checkRoots(block *Block, receipts []*TransactionReceipt) error {
	if root := ethtypes.DeriveSha(block.GethTransactions, trie.NewStackTrie(nil)); root != block.Header.TxHash {
		return fmt.Errorf("%w: transactions root %s, header %s", errRootMismatch, root, block.Header.TxHash)
	}
	if root := ethtypes.DeriveSha(gethReceipts(receipts), trie.NewStackTrie(nil)); root != block.Header.ReceiptHash {
		return fmt.Errorf("%w: receipts root %s, header %s", errRootMismatch, root, block.Header.ReceiptHash)
	}
	return nil
}

// receiptHashesMatch reports whether every receipt belongs to the pinned
// block hash, case-insensitively. An empty receipt list always matches: a
// receiptless block leaves nothing to verify.
func receiptHashesMatch(receipts []*TransactionReceipt, blockHash string) bool {
	for _, r := range receipts {
		if r == nil || !strings.EqualFold(r.BlockHash, blockHash) {
			return false
		}
	}
	return true
}

// batchCoversTransactions reports whether every transaction has a receipt in
// the batch result, matched by the transaction's own hash case-insensitively.
// An incomplete batch must not be accepted: a transaction without a receipt
// would keep the signed payload's conversion defaults and report success for
// an execution outcome the node never told us about.
func batchCoversTransactions(txs []*Transaction, receipts []*TransactionReceipt) bool {
	served := make(map[string]struct{}, len(receipts))
	for _, r := range receipts {
		if r != nil {
			served[strings.ToLower(r.TransactionHash)] = struct{}{}
		}
	}
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if _, ok := served[strings.ToLower(tx.Hash)]; !ok {
			return false
		}
	}
	return true
}
