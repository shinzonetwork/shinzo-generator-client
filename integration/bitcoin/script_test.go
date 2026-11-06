//go:build integration
// +build integration

package bitcoin

// The scripted chain model behind the fake: a deterministic canonical
// branch (every identity string is a sha256 of a seed key, so tests pin the
// exact hashes the wire carries), tip control for the restart/resume leg,
// and a one-block fork branch that is served on the first fetch of the
// armed height only, mirroring the processor's first-fetch orphan flow.
//
// Height inversion: both branch families embed the height into the hex
// digest's tail, so a by-hash lookup (getblock/getblockheader) reconstructs
// the payload from the hash alone without keeping hash maps. The 40-character
// digest head keeps the values 64-hex valid for btcd's chainhash decoder.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	btc "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/bitcoin"
)

// Seed section prefixes. Canonical and fork identities must never collide,
// so every fork seed carries its own section prefix.
const (
	seedSectionCanonical = "canon-"
	seedSectionFork      = "fork-"

	// hashHeadLen is the digest head kept before the height's hex tail.
	hashHeadLen = 40

	// maxScriptedHeight bounds the height inversion: hashes whose tail
	// decodes beyond it answer with Core's not-found error instead of
	// serving an unbounded synthetic branch.
	maxScriptedHeight = int64(10000000)

	// canonicalBlockTime is the block time of height zero; each height adds
	// ten minutes so parent/child times stay monotonic.
	canonicalBlockTime = int64(1650000000)
)

// Satoshi-exact fixtures. The converters turn BTC floats into int64
// satoshis through shortest-round-trip parsing, so these wire amounts must
// round-trip exactly.
const (
	coinbaseRewardBTC  = 50.0
	coinbaseRewardSat  = int64(5000000000)
	witnessFeeSat      = int64(10000)
	prevoutValueSat    = int64(85705000000)
	witnessOutputSat   = int64(85704990000)
	sequenceMax        = uint32(4294967295)
	scriptPubKeyStdHex = "76a91481b02c27b21004aeb4df5265dd90d4d56f56f84a88ac"
	witnessAddr        = "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080"
	witnessPubKeyHex   = "0014abcdef"
	witnessScriptType  = "witness_v0_keyhash"
	pubKeyHashType     = "pubkeyhash"
	scriptSigStdHex    = "00"
	blockBits          = "1a0facdc"
)

// RPC method names served by the fake.
const (
	methodBlockCount    = "getblockcount"
	methodBlockHash     = "getblockhash"
	methodBlock         = "getblock"
	methodBlockHeader   = "getblockheader"
	methodChainInfo     = "getblockchaininfo"
	methodGetInfo       = "getinfo"
	methodNetworkInfo   = "getnetworkinfo"
	errCodeBlock        = -5
	errCodeHeight       = -8
	errCodeMethod       = -32601
	errCodeParams       = -32602
	errMsgBlockNotFound = "Block not found"
	errMsgHeightRange   = "Block height out of range"
	errMsgMethodMissing = "Method not found"
)

// seedHash derives the 64-hex identity of one seeded key.
func seedHash(section, key string, height int64) string {
	sum := sha256.Sum256([]byte(section + key + strconv.FormatInt(height, 10)))
	return hex.EncodeToString(sum[:])
}

// scriptHash converts a seed digest into a height-bearing wire hash: the
// 40-char digest head plus a 24-char zero-padded hex tail carrying the
// height, decodable without any server-side registry.
func scriptHash(section, key string, height int64) string {
	return seedHash(section, key, height)[:hashHeadLen] + fmt.Sprintf("%024x", height)
}

// hashHeight inverts scriptHash: it decodes the hex tail without deciding
// which section produced the head.
func hashHeight(hash string) (int64, bool) {
	if len(hash) != hashHeadLen+24 {
		return 0, false
	}
	height, err := strconv.ParseInt(hash[hashHeadLen:], 16, 64)
	if err != nil || height < 0 || height > maxScriptedHeight {
		return 0, false
	}
	return height, true
}

// Expected-value accessors. Tests and payloads share them, so field
// assertions are pinned against the same seeds the fake derives from.
func expectedBlockHash(section string, height int64) string {
	return scriptHash(section, "block-hash-", height)
}

func expectedMerkleRoot(section string, height int64) string {
	return seedHash(section, "merkle-root-", height)
}

func expectedChainwork(height int64) string {
	return seedHash(seedSectionCanonical, "chainwork-", height)
}

func expectedBlockTime(height int64) int64 {
	return canonicalBlockTime + height*600
}

func expectedNonceDoc(height int64) string {
	return strconv.FormatUint(uint64(height), 10)
}

func expectedCoinbaseTxID(section string, height int64) string {
	return scriptHash(section, "coinbase-txid-", height)
}

func expectedCoinbaseWtxID(section string, height int64) string {
	return seedHash(section, "coinbase-wtxid-", height)
}

func expectedWitnessTxID(section string, height int64) string {
	return scriptHash(section, "witness-txid-", height)
}

func expectedWitnessWtxID(section string, height int64) string {
	return seedHash(section, "witness-wtxid-", height)
}

func expectedWitnessPrevTxID(section string, height int64) string {
	return scriptHash(section, "witness-prev-", height)
}

func expectedWitnessEntry(section string, height int64, index int) string {
	return seedHash(section, fmt.Sprintf("witness-%d-", index), height)
}

// expectedCoinbaseScript builds the coinbase input script of one height,
// bootstrapping the block height the way a serialized block header does.
func expectedCoinbaseScript(height int64) string {
	return fmt.Sprintf("03%06x", height)
}

func floatPtr(v float64) *float64 { return &v }
func uint32Ptr(v uint32) *uint32  { return &v }

// coinbaseTx builds one coinbase transaction: a single coinbase input, one
// output, no fee and no prevout, exactly the wire shape undo data never
// prices.
func coinbaseTx(section string, height int64) btc.Tx {
	return btc.Tx{
		TxID:     expectedCoinbaseTxID(section, height),
		Hash:     expectedCoinbaseWtxID(section, height),
		Version:  1,
		LockTime: 0,
		Vin:      []btc.Vin{{Coinbase: expectedCoinbaseScript(height), Sequence: sequenceMax}},
		Vout: []btc.Vout{{
			Index: 0,
			Value: coinbaseRewardBTC,
			ScriptPubKey: btc.ScriptPubKey{
				Hex:  scriptPubKeyStdHex,
				Type: pubKeyHashType,
			},
		}},
	}
}

// witnessTx builds one regular transaction with the full verbosity-3 shape:
// a witness input carrying a denormalized prevout, a fee, and an output.
func witnessTx(section string, height int64) btc.Tx {
	return btc.Tx{
		TxID:     expectedWitnessTxID(section, height),
		Hash:     expectedWitnessWtxID(section, height),
		Version:  2,
		LockTime: 0,
		Fee:      floatPtr(0.0001),
		Vin: []btc.Vin{{
			TxID:        expectedWitnessPrevTxID(section, height),
			Vout:        uint32Ptr(0),
			ScriptSig:   &btc.ScriptSig{Hex: scriptSigStdHex},
			Sequence:    sequenceMax,
			TxInWitness: []string{expectedWitnessEntry(section, height, 0), expectedWitnessEntry(section, height, 1)},
			Prevout: &btc.PrevOut{
				Generated: false,
				Height:    height - 1,
				Value:     857.05,
				ScriptPubKey: btc.ScriptPubKey{
					Hex:     witnessPubKeyHex,
					Address: witnessAddr,
					Type:    witnessScriptType,
				},
			},
		}},
		Vout: []btc.Vout{{
			Index: 0,
			Value: 857.0499,
			ScriptPubKey: btc.ScriptPubKey{
				Hex:     scriptPubKeyStdHex,
				Address: witnessAddr,
				Type:    pubKeyHashType,
			},
		}},
	}
}

// branchBlock builds the wire block of one branch's height, the coinbase
// plus one witness transaction in wire order.
func branchBlock(section string, height int64) *btc.Block {
	block := &btc.Block{
		Hash:          expectedBlockHash(section, height),
		Confirmations: 1,
		StrippedSize:  1000,
		Size:          2000,
		Weight:        4000,
		Height:        height,
		Version:       2,
		MerkleRoot:    expectedMerkleRoot(section, height),
		Tx:            []btc.Tx{coinbaseTx(section, height), witnessTx(section, height)},
		Time:          expectedBlockTime(height),
		Nonce:         uint32(height),
		Bits:          blockBits,
		Chainwork:     expectedChainwork(height),
		Difficulty:    1.0,
	}
	if height > 0 {
		block.PreviousBlockHash = expectedBlockHash(seedSectionCanonical, height-1)
	}
	return block
}

func canonicalBlock(height int64) *btc.Block {
	return branchBlock(seedSectionCanonical, height)
}

func forkBlock(height int64) *btc.Block {
	return branchBlock(seedSectionFork, height)
}

// branchHeader builds the verbose header of one branch's height. The client
// echoes the requested hash, so only Height and PreviousBlockHash matter.
func branchHeader(section string, height int64) btc.VerboseBlockHeader {
	header := btc.VerboseBlockHeader{
		Hash:          expectedBlockHash(section, height),
		Confirmations: 1,
		Height:        height,
		Version:       2,
		MerkleRoot:    expectedMerkleRoot(section, height),
		Time:          expectedBlockTime(height),
		Nonce:         uint64(height),
		Bits:          blockBits,
		Difficulty:    1.0,
	}
	if height > 0 {
		header.PreviousBlockHash = expectedBlockHash(seedSectionCanonical, height-1)
	}
	return header
}

// scriptedChain is the stateful slice of the fake: the tip, the armed fork
// heights, and the per-height first-fetch counters. Everything else is a
// pure function of a height or hash.
type scriptedChain struct {
	mu      sync.Mutex
	tip     int64
	forks   map[int64]struct{}
	fetches sync.Map // height -> *atomic.Int64 (first-fetch counters)
}

func newScriptedChain(initialTip int64) *scriptedChain {
	return &scriptedChain{tip: initialTip, forks: map[int64]struct{}{}}
}

// setTip moves the chain tip; parked height requests above the old tip
// start answering after the call.
func (c *scriptedChain) setTip(newTip int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tip = newTip
}

// armFork registers a one-block orphan branch at the given height and moves
// the tip in one lock, so no request can observe the tip advance without
// the fork being ready.
func (c *scriptedChain) armFork(height, newTip int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forks[height] = struct{}{}
	c.tip = newTip
}

// tipHeight reports the current tip.
func (c *scriptedChain) tipHeight() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tip
}

// takeFirstFetch increments the height's fetch counter and reports whether
// this ask is the first one only when a fork is armed at that height.
func (c *scriptedChain) takeFirstFetch(height int64) bool {
	c.mu.Lock()
	_, armed := c.forks[height]
	c.mu.Unlock()
	if !armed {
		return false
	}
	entry, _ := c.fetches.LoadOrStore(height, &atomic.Int64{})
	return entry.(*atomic.Int64).Add(1) == 1
}

// respond is the fake's dispatch table. Every unscripted method answers
// with Core's method-not-found error so unexpected RPC traffic fails loudly
// instead of drifting silently.
func (c *scriptedChain) respond(call recordedCall) mockResponse {
	params, err := decodeParams(call)
	if err != nil {
		return rpcError(errCodeParams, "malformed params")
	}

	switch call.Method {
	case methodBlockCount:
		return marshalResult(c.tipHeight())

	case methodBlockHash:
		height, ok := paramInt(params)
		if !ok {
			return rpcError(errCodeParams, "height param missing")
		}
		if c.takeFirstFetch(height) {
			return marshalResult(expectedBlockHash(seedSectionFork, height))
		}
		if height > c.tipHeight() {
			return rpcError(errCodeHeight, errMsgHeightRange)
		}
		return marshalResult(expectedBlockHash(seedSectionCanonical, height))

	case methodBlock:
		hash, ok := paramString(params)
		if !ok {
			return rpcError(errCodeParams, "block hash param missing")
		}
		if block, ok := c.blockPayloadForHash(hash); ok {
			return marshalResult(block)
		}
		return rpcError(errCodeBlock, errMsgBlockNotFound)

	case methodBlockHeader:
		hash, ok := paramString(params)
		if !ok {
			return rpcError(errCodeParams, "header hash param missing")
		}
		if header, ok := c.headerPayloadForHash(hash); ok {
			return marshalResult(header)
		}
		return rpcError(errCodeBlock, errMsgBlockNotFound)

	case methodChainInfo:
		return marshalResult(chainInfoPayload(c.tipHeight()))

	case methodGetInfo:
		// Bitcoin Core has no getinfo; the backend-version probe falls back
		// to getnetworkinfo when this lands.
		return rpcError(errCodeMethod, errMsgMethodMissing)

	case methodNetworkInfo:
		return marshalResult(map[string]any{
			"version":         270000,
			"subversion":      "/Satoshi:27.0.0/",
			"protocolversion": 70016,
			"relayfee":        0.00001,
		})

	default:
		return rpcError(errCodeMethod, errMsgMethodMissing)
	}
}

// blockPayloadForHash rebuilds the verbosity-3 payload a hash identifies.
func (c *scriptedChain) blockPayloadForHash(hash string) (any, bool) {
	height, ok := hashHeight(hash)
	if !ok {
		return nil, false
	}
	if hash == expectedBlockHash(seedSectionFork, height) {
		if c.forkArmed(height) {
			return forkBlock(height), true
		}
		return nil, false
	}
	if hash == expectedBlockHash(seedSectionCanonical, height) {
		return canonicalBlock(height), true
	}
	return nil, false
}

// headerPayloadForHash rebuilds the verbose header a hash identifies; the
// fork branch answers regardless of the height's position relative to the
// tip so the walk-back descends hash-faithfully along both branches.
func (c *scriptedChain) headerPayloadForHash(hash string) (any, bool) {
	height, ok := hashHeight(hash)
	if !ok {
		return nil, false
	}
	if hash == expectedBlockHash(seedSectionFork, height) {
		if c.forkArmed(height) {
			return branchHeader(seedSectionFork, height), true
		}
		return nil, false
	}
	if hash == expectedBlockHash(seedSectionCanonical, height) {
		return branchHeader(seedSectionCanonical, height), true
	}
	return nil, false
}

// chainInfoPayload builds the getblockchaininfo result the archive-floor
// log consumes; the softforks key rides the Core 27 unified object shape
// the client's version-probe path expects.
func chainInfoPayload(tip int64) map[string]any {
	return map[string]any{
		"chain":         "main",
		"blocks":        tip,
		"headers":       tip,
		"bestblockhash": expectedBlockHash(seedSectionCanonical, tip),
		"pruned":        false,
		"pruneheight":   int64(0),
		"softforks": map[string]any{
			"segwit": map[string]any{"type": "buried", "height": 481824, "active": true},
		},
	}
}

func (c *scriptedChain) forkArmed(height int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, armed := c.forks[height]
	return armed
}

// rpcError builds the RPC-failure style of the error envelope.
func rpcError(code int, message string) mockResponse {
	return mockResponse{rpcErr: &mockRPCError{Code: code, Message: message}}
}

// marshalResult turns a payload into the success style, converting a
// marshaling failure into a Core-style server error.
func marshalResult(payload any) mockResponse {
	raw, err := json.Marshal(payload)
	if err != nil {
		return rpcError(errCodeParams, "payload marshal failed: "+err.Error())
	}
	return mockResponse{result: raw}
}

// decodeParams decodes one recorded request's params array, with numbers as
// float64 and booleans as bool.
func decodeParams(call recordedCall) ([]any, error) {
	var params []any
	if len(call.Params) == 0 {
		return params, nil
	}
	err := json.Unmarshal(call.Params, &params)
	return params, err
}

// paramInt reads params[0] as a JSON number.
func paramInt(params []any) (int64, bool) {
	if len(params) == 0 {
		return 0, false
	}
	value, ok := params[0].(float64)
	if !ok {
		return 0, false
	}
	return int64(value), true
}

// paramString reads params[0] as a JSON string.
func paramString(params []any) (string, bool) {
	if len(params) == 0 {
		return "", false
	}
	hash, ok := params[0].(string)
	return hash, ok
}
