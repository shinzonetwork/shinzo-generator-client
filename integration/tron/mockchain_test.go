//go:build integration
// +build integration

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/tron"
)

// Scripted two-branch Tron chain plus a wallet-API fake served over one
// httptest server. Branch A spans heights 100..109, branch B shares
// 100..103 byte-identical and diverges 104..115 with the higher head, so a
// forward-walking indexer driven into height 110 meets the fork. The
// blockID is a sha256 over the marshaled header content: deterministic,
// content-derived, and naturally divergent because the diverged blocks
// carry different witness addresses, timestamps, and trie roots.
//
// Sender value map keys (owner_address, to_address, amount, call_value) use
// the java-tron field spellings; they round-trip through the Go structs
// untouched, and the mock serves and the client re-decodes exactly what the
// adapter expects from a real node.

const (
	tronBranchA = "A"
	tronBranchB = "B"

	tronStartHeight  = int64(100)
	tronForkHeight   = int64(103)
	tronHeadA        = int64(109)
	tronHeadBInitial = int64(115)
	tronHeadBFinal   = int64(118)

	tronTestAPIKey        = "test-key"
	tronTestKeyringSecret = "tron-suite-keyring-secret"
	tronWalletAPIKeyHdr   = "Tron-Pro-Api-Key"

	tronWitnessA    = "a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5"
	tronWitnessB    = "b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7"
	tronTxTrieRootA = "3131313131313131313131313131313131313131313131313131313131313131"
	tronTxTrieRootB = "5252525252525252525252525252525252525252525252525252525252525252"

	tronBaseTimestamp  = int64(1700000000000)
	tronBlockSpacingMS = int64(3000)
	tronTSOffsetB      = int64(111000)

	tronOwnerAddr     = "31aa2c1ee2fb6fa5b3d3b3dac9dcf7b3e5a1a0cf21f3c4d5e6f708192a3b4c5d"
	tronRecipientAddr = "42bb5c8f7a3ef2d94c6e8b1ae0d3f5c9e2a7b4d1f8c6e3a5b7d9f0e2c4a6b8d1f"
	tronContractAddr  = "43cc6e1f9b4d7a2c8e5f0b3d6a9c1e4f7b2d8a5c3e6f9b1d4a7c0e3f6b9d2a5c8"

	tronTx100ID      = "mock-tx-100-transfer"
	tronTx101ID      = "mock-tx-101-trigger"
	tronInternalTxID = "mock-internal-tx-101"
	tronEventTopic   = "66554433221100ffee99ddcc88bbcc77aa66ff55ff44ff33ff22ff11ff00eeff"

	tronTransferFee   = int64(21000)
	tronTransferFuel  = int64(500)
	tronTriggerFee    = int64(43210)
	tronTriggerFuel   = int64(1200)
	tronContractValue = int64(7)
	tronNumericAsset  = int64(105)

	tronVersion = int64(29)
)

// Wallet API endpoint paths, mirroring the adapter's java-tron bindings.
// One server hosts both the FullNode (/wallet/*) and the SolidityNode
// (/walletsolidity/*) paths: the client's same-host fallback routes
// /walletsolidity calls to the NodeURL host when SolidityURL is empty.
const (
	mockEndpointGetBlockByNum       = "/wallet/getblockbynum"
	mockEndpointGetBlockByID        = "/wallet/getblockbyid"
	mockEndpointGetNowBlock         = "/wallet/getnowblock"
	mockEndpointGetTxInfoByBlockNum = "/wallet/gettransactioninfobyblocknum"
	mockEndpointSolidityNowBlock    = "/walletsolidity/getnowblock"
)

// callRecorder is a per-endpoint request journal. It backs the restart
// phase's no-refetch assertion: heights requested after the first indexer
// stopped must all sit at or above the resume height.
type callRecorder struct {
	blockByNum  []int64
	blockByID   []string
	receipts    []int64
	missingAuth int
}

// mockChain owns both scripted branches, their receipts, and the recorder.
type mockChain struct {
	mu         sync.Mutex
	active     string
	byNum      map[string]map[int64]*tron.Block
	receipts   map[string]map[int64][]*tron.TransactionInfo
	blocksByID map[string]*tron.Block
	heads      map[string]int64
	solidified *tron.Block
	rec        callRecorder
}

// tronBlockIDHash derives the 64-hex blockID as sha256 over the marshaled
// unsigned header: same content in, same blockID out.
func tronBlockIDHash(block *tron.Block) string {
	raw, err := json.Marshal(block.BlockHeader.RawData)
	if err != nil {
		panic(fmt.Sprintf("mock chain: header marshal: %v", err))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// tronTSA returns branch-A timestamps: 3s apart from the suite base.
func tronTSA(num int64) int64 {
	return tronBaseTimestamp + (num-tronStartHeight)*tronBlockSpacingMS
}

// tronTSB returns branch-B timestamps for the diverged heights: 3s apart
// within B and distinct from A at the same height.
func tronTSB(num int64) int64 {
	return tronBaseTimestamp + tronTSOffsetB + (num-tronStartHeight)*tronBlockSpacingMS
}

// buildTronBlock assembles one block and pins its content-derived blockID.
// The parent's blockID is the parentHash, so the mock hashes chain.
func buildTronBlock(parentID string, num, timestamp int64, witness, txTrieRoot, signature string, txs []tron.Transaction) *tron.Block {
	block := &tron.Block{
		BlockHeader: tron.BlockHeader{
			RawData: tron.BlockHeaderRawData{
				Number:         num,
				Timestamp:      timestamp,
				ParentHash:     parentID,
				WitnessAddress: witness,
				Version:        tronVersion,
				TxTrieRoot:     txTrieRoot,
			},
			WitnessSignature: signature,
		},
		Transactions: txs,
	}
	block.BlockID = tronBlockIDHash(block)
	return block
}

// tronReceipt builds one receipt pinned to its block: the
// blockNumber/blockTimeStamp tie the fetcher verifies before bundling.
func tronReceipt(id string, block *tron.Block, fee, energy int64, logs []tron.TxLog, internals []tron.InternalTransaction) *tron.TransactionInfo {
	return &tron.TransactionInfo{
		ID:                   id,
		BlockNumber:          block.BlockHeader.RawData.Number,
		BlockTimeStamp:       block.BlockHeader.RawData.Timestamp,
		Fee:                  fee,
		Receipt:              tron.TxReceipt{Result: "SUCCESS", EnergyUsage: energy, EnergyFee: fee, NetUsage: 64},
		Log:                  logs,
		InternalTransactions: internals,
	}
}

// tronTransferTx builds the TRX TransferContract transaction of block 100.
func tronTransferTx() tron.Transaction {
	return tron.Transaction{
		TxID: tronTx100ID,
		RawData: tron.TransactionRawData{
			Contract: []tron.TransactionContract{{
				Type: "TransferContract",
				Parameter: tron.TransactionContractParam{Value: map[string]any{
					"owner_address": tronOwnerAddr,
					"to_address":    tronRecipientAddr,
					"amount":        int64(1500000),
				}},
			}},
			Expiration: tronTSA(100) + 600000,
			Timestamp:  tronTSA(100) - 1000,
			FeeLimit:   1,
		},
		Ret: []tron.TxRet{{ContractRet: "SUCCESS"}},
	}
}

// tronTriggerTx builds the TriggerSmartContract transaction of block 101.
// amount stays 0 (no native movement in the contract call itself), so the
// suite's movement assertions focus on the logs and the internal transfer.
func tronTriggerTx() tron.Transaction {
	return tron.Transaction{
		TxID: tronTx101ID,
		RawData: tron.TransactionRawData{
			Contract: []tron.TransactionContract{{
				Type: "TriggerSmartContract",
				Parameter: tron.TransactionContractParam{Value: map[string]any{
					"owner_address":    tronOwnerAddr,
					"contract_address": tronContractAddr,
					"call_value":       int64(0),
					"data":             "11000020202011223344556677889900112233445566778899aabbccddeeff",
				}},
			}},
			Expiration: tronTSA(101) + 600000,
			Timestamp:  tronTSA(101) - 1000,
			FeeLimit:   2000000,
		},
		Ret: []tron.TxRet{{ContractRet: "SUCCESS"}},
	}
}

// tronDuplicateLogs returns one event served twice byte-for-byte: the log
// documents become distinct only through the synthetic logIndex.
func tronDuplicateLogs() []tron.TxLog {
	log := tron.TxLog{
		Address: tronContractAddr,
		Topics:  []string{tronEventTopic},
		Data:    "656465616462656566",
	}
	return []tron.TxLog{log, log}
}

// tronInternalTx returns one internal movement whose asset id arrives as a
// JSON number, exercising the any-typed normalization on the wire.
func tronInternalTx() tron.InternalTransaction {
	return tron.InternalTransaction{
		Hash:              tronInternalTxID,
		CallerAddress:     tronOwnerAddr,
		TransferToAddress: tronContractAddr,
		CallValueInfo: []tron.CallValueInfo{
			{CallValue: tronContractValue, AssetName: tronNumericAsset},
		},
		Rejected: false,
		Note:     "trc-10 transfer",
	}
}

// newMockChain scripts the two branches, registers every block by ID, and
// pins the solidified head at the fork base so the reorg walk-back can
// descend to the common ancestor at height 103.
func newMockChain() *mockChain {
	c := &mockChain{
		active: tronBranchA,
		byNum: map[string]map[int64]*tron.Block{
			tronBranchA: {},
			tronBranchB: {},
		},
		receipts: map[string]map[int64][]*tron.TransactionInfo{
			tronBranchA: {},
			tronBranchB: {},
		},
		blocksByID: map[string]*tron.Block{},
		heads: map[string]int64{
			tronBranchA: tronHeadA,
			tronBranchB: tronHeadBInitial,
		},
	}

	// Branch A. Height 100 carries the transfer, 101 the contract call
	// with the duplicated event and the internal transfer, 102 is empty,
	// 103..109 are plain headers.
	zeroParent := strings.Repeat("0", 64)
	b100 := buildTronBlock(zeroParent, 100, tronTSA(100), tronWitnessA, tronTxTrieRootA, "mock-sig-a-100", []tron.Transaction{tronTransferTx()})
	b101 := buildTronBlock(b100.BlockID, 101, tronTSA(101), tronWitnessA, tronTxTrieRootA, "mock-sig-a-101", []tron.Transaction{tronTriggerTx()})
	b102 := buildTronBlock(b101.BlockID, 102, tronTSA(102), tronWitnessA, tronTxTrieRootA, "mock-sig-a-102", nil)
	parent := b102.BlockID
	for num := int64(103); num <= tronHeadA; num++ {
		blk := buildTronBlock(parent, num, tronTSA(num), tronWitnessA, tronTxTrieRootA, fmt.Sprintf("mock-sig-a-%d", num), nil)
		c.register(tronBranchA, blk)
		parent = blk.BlockID
	}
	c.register(tronBranchA, b100)
	c.register(tronBranchA, b101)
	c.register(tronBranchA, b102)

	c.receipts[tronBranchA][100] = []*tron.TransactionInfo{tronReceipt(tronTx100ID, b100, tronTransferFee, tronTransferFuel, nil, nil)}
	c.receipts[tronBranchA][101] = []*tron.TransactionInfo{tronReceipt(tronTx101ID, b101, tronTriggerFee, tronTriggerFuel, tronDuplicateLogs(), []tron.InternalTransaction{tronInternalTx()})}

	// Branch B: byte-identical shared prefix, diverged suffix with the
	// higher head, so the walk into height 110 meets the fork.
	for num := tronStartHeight; num <= tronForkHeight; num++ {
		c.register(tronBranchB, c.byNum[tronBranchA][num])
		c.receipts[tronBranchB][num] = c.receipts[tronBranchA][num]
	}
	parent = c.byNum[tronBranchA][tronForkHeight].BlockID
	for num := tronForkHeight + 1; num <= tronHeadBInitial; num++ {
		blk := buildTronBlock(parent, num, tronTSB(num), tronWitnessB, tronTxTrieRootB, fmt.Sprintf("mock-sig-b-%d", num), nil)
		c.register(tronBranchB, blk)
		parent = blk.BlockID
	}

	// The solidified head sits at the fork base: the walk-back floor.
	c.solidified = c.byNum[tronBranchA][tronForkHeight]

	return c
}

// register files one block into the branch-by-height view and the global
// by-hash view (getblockbyid serves both branches).
func (c *mockChain) register(branch string, blk *tron.Block) {
	c.byNum[branch][blk.BlockHeader.RawData.Number] = blk
	c.blocksByID[blk.BlockID] = blk
}

// startServer binds the mock chain to an httptest server and registers the
// wallet endpoints on the shared URL.
func (c *mockChain) startServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc(mockEndpointGetBlockByNum, c.handleGetBlockByNum)
	mux.HandleFunc(mockEndpointGetBlockByID, c.handleGetBlockByID)
	mux.HandleFunc(mockEndpointGetNowBlock, c.handleGetNowBlock)
	mux.HandleFunc(mockEndpointGetTxInfoByBlockNum, c.handleGetTransactionInfoByBlockNum)
	mux.HandleFunc(mockEndpointSolidityNowBlock, c.handleSolidityNowBlock)
	return httptest.NewServer(mux)
}

// checkAuth enforces the configured API key on every wallet request, proving
// the client's header wiring end to end.
func (c *mockChain) checkAuth(r *http.Request) {
	if r.Header.Get(tronWalletAPIKeyHdr) != tronTestAPIKey {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.rec.missingAuth++
	}
}

// mockBlockAt resolves the active branch's block at a height. Heights above
// the active head resolve to not-found (the empty object), the retry path
// the processor's 3s cadence depends on.
func (c *mockChain) mockBlockAt(num int64) (*tron.Block, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if num > c.heads[c.active] {
		return nil, false
	}
	return c.byNum[c.active][num], true
}

func (c *mockChain) handleGetBlockByNum(w http.ResponseWriter, r *http.Request) {
	c.checkAuth(r)
	var req struct {
		Num int64 `json:"num"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		mockWriteJSON(w, map[string]string{"Error": "invalid request body"})
		return
	}
	c.mu.Lock()
	c.rec.blockByNum = append(c.rec.blockByNum, req.Num)
	c.mu.Unlock()
	blk, found := c.mockBlockAt(req.Num)
	if !found {
		// An unknown height is the empty object, not an HTTP error: the
		// client maps it to the not-found classification.
		c.closeBody(r)
		mockWriteJSON(w, map[string]any{})
		return
	}
	c.closeBody(r)
	mockWriteJSON(w, blk)
}

func (c *mockChain) handleGetBlockByID(w http.ResponseWriter, r *http.Request) {
	c.checkAuth(r)
	var req struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		mockWriteJSON(w, map[string]string{"Error": "invalid request body"})
		return
	}
	c.mu.Lock()
	c.rec.blockByID = append(c.rec.blockByID, req.Value)
	blk := c.blocksByID[req.Value]
	c.mu.Unlock()
	c.closeBody(r)
	if blk == nil {
		mockWriteJSON(w, map[string]any{})
		return
	}
	// Served from either branch: the walk-back follows the new chain's
	// parent hashes, several of which belong to the old branch after a flip.
	mockWriteJSON(w, blk)
}

func (c *mockChain) handleGetNowBlock(w http.ResponseWriter, r *http.Request) {
	c.checkAuth(r)
	c.mu.Lock()
	head := c.byNum[c.active][c.heads[c.active]]
	c.mu.Unlock()
	c.closeBody(r)
	if head == nil {
		mockWriteJSON(w, map[string]any{})
		return
	}
	mockWriteJSON(w, head)
}

func (c *mockChain) handleGetTransactionInfoByBlockNum(w http.ResponseWriter, r *http.Request) {
	c.checkAuth(r)
	var req struct {
		Num int64 `json:"num"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		mockWriteJSON(w, map[string]string{"Error": "invalid request body"})
		return
	}
	c.mu.Lock()
	c.rec.receipts = append(c.rec.receipts, req.Num)
	active := c.active
	head := c.heads[active]
	receiptList := c.receipts[active][req.Num]
	c.mu.Unlock()
	c.closeBody(r)
	if req.Num > head {
		mockWriteJSON(w, map[string]any{})
		return
	}
	if receiptList == nil {
		// A height without transactions answers with the empty array, the
		// normal no-receipts result for an empty block.
		mockWriteJSON(w, []*tron.TransactionInfo{})
		return
	}
	mockWriteJSON(w, receiptList)
}

func (c *mockChain) handleSolidityNowBlock(w http.ResponseWriter, r *http.Request) {
	c.checkAuth(r)
	c.mu.Lock()
	solidified := c.solidified
	c.mu.Unlock()
	c.closeBody(r)
	if solidified == nil {
		mockWriteJSON(w, map[string]any{})
		return
	}
	mockWriteJSON(w, solidified)
}

func (c *mockChain) closeBody(r *http.Request) {
	_ = r.Body.Close()
}

func mockWriteJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// setActiveBranch flips the branch selector: the next serving pass reads
// from the chosen branch, driving the reorg at the next not-found retry.
func (c *mockChain) setActiveBranch(branch string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active = branch
}

// blockByNum reading accessors. Each locks independently so assertions can
// call them freely while the server handles requests.
func (c *mockChain) blockByNum(branch string, num int64) *tron.Block {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byNum[branch][num]
}

func (c *mockChain) blockHashAt(branch string, num int64) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	blk := c.byNum[branch][num]
	if blk == nil {
		return ""
	}
	return blk.BlockID
}

// extendBranchB appends plain-header blocks up to the given height and
// raises that branch's head, the restart phase's growth step.
func (c *mockChain) extendBranchB(to int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	head := c.heads[tronBranchB]
	if to <= head {
		return
	}
	parent := c.byNum[tronBranchB][head].BlockID
	for num := head + 1; num <= to; num++ {
		blk := buildTronBlock(parent, num, tronTSB(num), tronWitnessB, tronTxTrieRootB, fmt.Sprintf("mock-sig-b-%d", num), nil)
		c.register(tronBranchB, blk)
		parent = blk.BlockID
	}
	c.heads[tronBranchB] = to
}

// byNumLen snapshots the getblockbynum journal length, the marker the
// restart phase takes between the first indexer's stop and the second's
// start.
func (c *mockChain) byNumLen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.rec.blockByNum)
}

// byNumSince returns the getblockbynum heights recorded after the marker.
func (c *mockChain) byNumSince(marker int) []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if marker > len(c.rec.blockByNum) {
		return nil
	}
	return append([]int64(nil), c.rec.blockByNum[marker:]...)
}

// missingAuthCount reports wallet requests without the configured API key.
func (c *mockChain) missingAuthCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rec.missingAuth
}
