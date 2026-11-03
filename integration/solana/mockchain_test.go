//go:build integration
// +build integration

package integration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/gagliardetto/solana-go"
)

// Scripted Solana chain fake served as one JSON-RPC endpoint. The branch
// spans slots 100..112 with a skipped slot at 107: a null getBlock below
// the confirmed tip, exactly the missing-slot outcome the fetcher
// classifies as ErrHeightSkipped. Slot 100 is the hot slot (one
// transaction carrying two byte-identical outer instructions and one CPI
// group with two byte-identical inner instructions, plus a token balance
// pair, all keyed by the real document ordinals of the 2026-10-31 fix),
// slot 101 is quiet (no transactions, rewards only), 102..112 are plain
// headers, and slots 100 and 101 both carry the fee/voting reward pair
// (a real Solana block carries rewards on every slot).
//
// The served JSON is the wire shape the compiled SDK client decodes
// (mirroring pkg/chains/solana's canned confirmed-block fixture:
// object-form legacy transactions with a meta object), so the indexer's
// full pipeline runs unmodified. Identity strings (blockhashes, pubkeys,
// mint) derive deterministically from 32-byte sha256 digests encoded to
// base58, so a block's blockhash differs per slot while its
// previousBlockhash points at the last PRODUCED block's hash: a skipped
// slot bridges naturally, which is what keeps the indexer's continuity
// check from raising a false reorg at the gap.

const (
	solanaStartSlot    = int64(100)
	solanaSkipSlot     = int64(107)
	solanaHeadInitial  = int64(109)
	solanaHeadExtended = int64(112)
	solanaFinalized    = int64(103)

	solanaTestAPIKey     = "test-solanakey-header"
	solanaTestKeyring    = "solana-suite-keyring-secret"
	solanaDefaultAuthHdr = "x-api-key"
	solanaBaseTS         = int64(1700000000)
	solanaSpacingSecs    = int64(3)

	// The hot transaction rides five committed keys: payer, outer program,
	// outer account, inner program, inner account. Every identity string is
	// derived from a 32-byte digest, so base58 decoding succeeds.
	solanaHotTxFee  = 5000
	solanaHotTxCU   = 4321
	solanaOuterData = "777"
	solanaInnerData = "555"

	solanaTBCPreAmt  = "100"
	solanaTBCPostAmt = "150"

	solanaRewardFeeLamp int64 = -25000
	solanaRewardFeePost       = 987654321
	solanaRewardFeeType       = "Fee"

	solanaRewardVoteLamp int64 = 405
	solanaRewardVotePost       = 1000
	solanaRewardVoteType       = "Voting"
	solanaCommission           = 5
)

// method and param shapes mirror the solana-go client's wire format:
// getSlot carries an options object whose commitment field selects the
// view, and getBlock carries the slot number followed by the opts object.
type solanaRPCRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	ID     json.RawMessage   `json:"id"`
}

// solanaCallRecorder journals the JSON-RPC surface so the restart phase
// can prove the resume point: slots requested after the first indexer
// stopped must all sit at or above the resume height, and every request
// must carry the configured API key header.
type solanaCallRecorder struct {
	getBlockSlots []int64
	getSlotCalls  int
	missingAuth   int
}

// solanaMockChain owns the scripted wire blocks, the head, and the
// recorder.
type solanaMockChain struct {
	mu        sync.Mutex
	blocks    map[int64]map[string]any
	head      int64
	finalized int64
	rec       solanaCallRecorder
}

// solanaBase58Key derives a deterministic 32-byte public-key string from a
// seed: same seed in, same base58 identity out.
func solanaBase58Key(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return solana.PublicKeyFromBytes(sum[:]).String()
}

// solanaBlockHash returns the per-slot blockhash.
func solanaBlockHash(slot int64) string {
	return solanaBase58Key(fmt.Sprintf("mock-blockhash-%d", slot))
}

// solanaBlockTime returns the deterministic per-slot block time.
func solanaBlockTime(slot int64) int64 {
	return solanaBaseTS + (slot-solanaStartSlot)*solanaSpacingSecs
}

// solanaGenesisHash is the zero-parent identity the first block bridges
// from.
func solanaGenesisHash() string {
	return solanaBase58Key("mock-blockhash-genesis")
}

// solanaSignatureWire returns an all-ones signature string: 64 bytes of
// zero in base58, the trivially decodable placeholder shape. Length must
// decode to exactly the 64-byte signature size the SDK enforces, so 88
// characters (which would decode to 88 zero bytes) is invalid.
func solanaSignatureWire() string {
	return strings.Repeat("1", 64)
}

// solanaHotTransaction builds the wire transaction of the hot slot: two
// byte-identical outer instructions, one CPI group with two byte-identical
// inner instructions, and a pre/post token balance pair that survives into
// one TokenBalanceChange document. Shape mirrors the canned fixture's
// object-form legacy transaction, which the SDK decode is proven against.
func solanaHotTransaction(seedPrefix string) map[string]any {
	payer := solanaBase58Key(seedPrefix + "-payer")
	outerProgram := solanaBase58Key(seedPrefix + "-outer-program")
	outerAccount := solanaBase58Key(seedPrefix + "-outer-account")
	innerProgram := solanaBase58Key(seedPrefix + "-inner-program")
	innerAccount := solanaBase58Key(seedPrefix + "-inner-account")
	mint := solanaBase58Key(seedPrefix + "-mint")

	outerProgramIDIndex := float64(1) // outer program rides the outer account slot
	outerAccountIndex := float64(2)
	innerProgramIndex := float64(3)
	innerAccountIndex := float64(4)

	outerInstr := map[string]any{"programIdIndex": outerProgramIDIndex, "accounts": []float64{outerAccountIndex}, "data": solanaOuterData}

	return map[string]any{
		"transaction": map[string]any{
			"signatures": []string{solanaSignatureWire()},
			"message": map[string]any{
				"header": map[string]any{
					"numRequiredSignatures":       float64(1),
					"numReadonlySignedAccounts":   float64(0),
					"numReadonlyUnsignedAccounts": float64(1),
				},
				"accountKeys": []string{
					payer, outerProgram, outerAccount, innerProgram, innerAccount,
				},
				"recentBlockhash": solanaBase58Key(seedPrefix + "-recent-blockhash"),
				"instructions":    []any{outerInstr, outerInstr},
			},
		},
		"meta": map[string]any{
			"err":                  nil,
			"fee":                  float64(solanaHotTxFee),
			"computeUnitsConsumed": float64(solanaHotTxCU),
			"logMessages":          []string{"Program log: mock entry"},
			"preBalances":          []float64{1000000, 2000000},
			"postBalances":         []float64{995000, 125000},
			"loadedAddresses":      map[string]any{"writable": []string{}, "readonly": []string{}},
			"innerInstructions": []any{
				map[string]any{
					"index": float64(0),
					"instructions": []any{
						map[string]any{
							"programIdIndex": innerProgramIndex,
							"accounts":       []float64{innerAccountIndex},
							"data":           solanaInnerData,
							"stackHeight":    float64(2),
						},
						map[string]any{
							"programIdIndex": innerProgramIndex,
							"accounts":       []float64{innerAccountIndex},
							"data":           solanaInnerData,
							"stackHeight":    float64(2),
						},
					},
				},
			},
			"preTokenBalances": []any{
				map[string]any{
					"accountIndex": innerAccountIndex,
					"mint":         mint,
					"uiTokenAmount": map[string]any{
						"amount":         solanaTBCPreAmt,
						"decimals":       float64(6),
						"uiAmountString": "0.0001",
					},
				},
			},
			"postTokenBalances": []any{
				map[string]any{
					"accountIndex": innerAccountIndex,
					"mint":         mint,
					"programId":    payer,
					"uiTokenAmount": map[string]any{
						"amount":         solanaTBCPostAmt,
						"decimals":       float64(6),
						"uiAmountString": "0.00015",
					},
				},
			},
		},
		"version": "legacy",
	}
}

// solanaWireRewards builds the fee/voting reward pair shared by the hot and
// quiet slots, with the commission surviving on the voting entry.
func solanaWireRewards(slot int64) []any {
	return []any{
		map[string]any{
			"pubkey":      solanaBase58Key(fmt.Sprintf("mock-fee-key-%d", slot)),
			"lamports":    float64(solanaRewardFeeLamp),
			"postBalance": float64(solanaRewardFeePost),
			"rewardType":  solanaRewardFeeType,
			"commission":  nil,
		},
		map[string]any{
			"pubkey":      solanaBase58Key(fmt.Sprintf("mock-vote-key-%d", slot)),
			"lamports":    float64(solanaRewardVoteLamp),
			"postBalance": float64(solanaRewardVotePost),
			"rewardType":  solanaRewardVoteType,
			"commission":  float64(solanaCommission),
		},
	}
}

// solanaWireBlock assembles the full wire block served for a slot:
// blockhash from the slot, previousBlockhash from the last PRODUCED
// ancestor, deterministic block time, and the scripted content.
func solanaWireBlock(parentHash string, slot int64, transaction map[string]any, withRewards bool) map[string]any {
	var transactions []any
	if transaction != nil {
		transactions = []any{transaction}
	}
	var rewards []any
	if withRewards {
		rewards = solanaWireRewards(slot)
	}
	return map[string]any{
		"blockHeight":       float64(slot),
		"blockTime":         float64(solanaBlockTime(slot)),
		"blockhash":         solanaBlockHash(slot),
		"previousBlockhash": parentHash,
		"parentSlot":        float64(slot - 1),
		"transactions":      transactions,
		"rewards":           rewards,
	}
}

// newSolanaMockChain scripts slots [startSlot, headInitial] with the skip
// at solanaSkipSlot: 100 hot (transaction + rewards), 101 quiet (rewards
// only), 102..109 plain headers. Every produced block's previousBlockhash
// points at the previous PRODUCED block, so slot 108 chains onto 106
// across the bridge and the indexer's continuity check sees no false
// reorg at the skipped height.
func newSolanaMockChain() *solanaMockChain {
	c := &solanaMockChain{
		blocks:    map[int64]map[string]any{},
		head:      solanaHeadInitial,
		finalized: solanaFinalized,
	}

	parent := solanaGenesisHash()
	for slot := solanaStartSlot; slot <= solanaHeadInitial; slot++ {
		if slot == solanaSkipSlot {
			// A skipped slot produces nothing: the map simply stays empty
			// and the next produced block inherits the previous parent.
			continue
		}
		var transaction map[string]any
		withRewards := slot <= solanaStartSlot+1
		switch slot {
		case solanaStartSlot:
			transaction = solanaHotTransaction(fmt.Sprintf("mock-tx-%d", slot))
		}
		blk := solanaWireBlock(parent, slot, transaction, withRewards)
		c.blocks[slot] = blk
		parent = blk["blockhash"].(string)
	}
	return c
}

// startServer binds the mock chain to an httptest server speaking the real
// JSON-RPC envelope of the solana-go client.
func (c *solanaMockChain) startServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(c.handleRPC))
}

// checkAuth enforces the configured API key on every request, proving the
// client's header wiring end to end.
func (c *solanaMockChain) checkAuth(r *http.Request) {
	if r.Header.Get(solanaDefaultAuthHdr) != solanaTestAPIKey {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.rec.missingAuth++
	}
}

// handleRPC routes the two supported methods and answers with the JSON-RPC
// 2.0 envelope the jsonrpc client decodes.
func (c *solanaMockChain) handleRPC(w http.ResponseWriter, r *http.Request) {
	c.checkAuth(r)
	var req solanaRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.writeError(w, req.ID, -32700, "invalid request body")
		return
	}
	c.mu.Lock()
	switch req.Method {
	case "getBlock":
		c.rec.getBlockSlots = append(c.rec.getBlockSlots, solanaSlotFromParams(req.Params))
		head := c.head
		c.mu.Unlock()
		c.handleGetBlock(w, req, head)
	case "getSlot":
		c.rec.getSlotCalls++
		head, finalized := c.head, c.finalized
		c.mu.Unlock()
		c.handleGetSlot(w, req, head, finalized)
	default:
		c.mu.Unlock()
		c.writeError(w, req.ID, -32601, "method not found")
	}
}

// handleGetBlock serves the scripted block or the null result for a skipped
// or not-yet-produced slot: the SDK converts a null result into its own
// not-confirmed sentinel, which the fetcher maps onto the skip classes.
func (c *solanaMockChain) handleGetBlock(w http.ResponseWriter, req solanaRPCRequest, head int64) {
	slot := solanaSlotFromParams(req.Params)
	w.Header().Set("Content-Type", "application/json")
	if slot > head || slot == solanaSkipSlot {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  nil,
		})
		return
	}
	c.mu.Lock()
	blk := c.blocks[slot]
	c.mu.Unlock()
	if blk == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  nil,
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result":  blk,
	})
}

// handleGetSlot answers the confirmed view with the active head and the
// finalized view with the constant floor slot.
func (c *solanaMockChain) handleGetSlot(w http.ResponseWriter, req solanaRPCRequest, head, finalized int64) {
	commitment := ""
	if len(req.Params) > 0 {
		var opts []struct {
			Commitment string `json:"commitment"`
		}
		if err := json.Unmarshal(req.Params[0], &opts); err == nil && len(opts) > 0 {
			commitment = opts[0].Commitment
		}
	}
	seat := head
	if commitment == "finalized" {
		seat = finalized
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result":  seat,
	})
}

// writeError renders a JSON-RPC error envelope.
func (c *solanaMockChain) writeError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	})
}

// solanaSlotFromParams extracts the requested slot from a getBlock param
// list ([<slot>, <opts>]); the SDK encodes slot numbers as JSON numbers,
// some clients as strings, so both shapes are accepted.
func solanaSlotFromParams(params []json.RawMessage) int64 {
	if len(params) == 0 {
		return -1
	}
	var slot int64
	if err := json.Unmarshal(params[0], &slot); err == nil {
		return slot
	}
	var text string
	if err := json.Unmarshal(params[0], &text); err != nil {
		return -1
	}
	var parsed int64
	if _, scanErr := fmt.Sscanf(text, "%d", &parsed); scanErr != nil {
		return -1
	}
	return parsed
}

// extend appends plain-header blocks up to the given slot and raises the
// head, the restart phase's growth step.
func (c *solanaMockChain) extend(to int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if to <= c.head {
		return
	}
	parent := c.blocks[c.head]["blockhash"].(string)
	// The skipped slot inside the extended range keeps the parent chain
	// unbroken: nothing is produced at the skip, the next block inherits
	// the bridge parent directly.
	for slot := c.head + 1; slot <= to; slot++ {
		if slot == solanaSkipSlot {
			continue
		}
		blk := solanaWireBlock(parent, slot, nil, false)
		c.blocks[slot] = blk
		parent = blk["blockhash"].(string)
	}
	c.head = to
}

// blockHashAt returns the derived blockhash of a scripted slot ("") for the
// skip.
func (c *solanaMockChain) blockHashAt(slot int64) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	blk := c.blocks[slot]
	if blk == nil {
		return ""
	}
	return blk["blockhash"].(string)
}

// blockTimeAt returns the scripted block time of a slot (0 for the skip).
func (c *solanaMockChain) blockTimeAt(slot int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	blk := c.blocks[slot]
	if blk == nil {
		return 0
	}
	return int64(blk["blockTime"].(float64))
}

// byGetBlockLen snapshots the getBlock journal length, the marker the
// restart phase takes between the first indexer's stop and the second's.
func (c *solanaMockChain) byGetBlockLen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.rec.getBlockSlots)
}

// byGetBlockSince returns the slots requested after the marker.
func (c *solanaMockChain) byGetBlockSince(marker int) []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if marker > len(c.rec.getBlockSlots) {
		return nil
	}
	return append([]int64(nil), c.rec.getBlockSlots[marker:]...)
}

// missingAuthCount reports requests without the configured API key header.
func (c *solanaMockChain) missingAuthCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rec.missingAuth
}
