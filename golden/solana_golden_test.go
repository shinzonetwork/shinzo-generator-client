//go:build integration
// +build integration

package golden

// The solana chain spec: getBlock returns the full transaction metadata and
// the block rewards inline, so the solana case runs one pass against a mock
// JSON-RPC server that replays the captured fixture body verbatim. There is
// no receipts call and no batch/fallback split to force, unlike evm.
//
// One fixture quirk shapes this spec: the getBlock result names no slot of
// its own (parentSlot is the parent's slot), so the fixture's decimal file
// stem is the only slot record. The capture script guarantees the stem
// matches the slot it requested, and the mock addresses fixtures by that
// stem, mirroring how the production client passes the requested slot into
// block conversion.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana"
)

// solanaSpec implements chainSpec for the solana chain family.
type solanaSpec struct{}

// methodGetBlock is the only RPC method the golden flow exercises. Any
// other method is rejected so unexpected RPC traffic fails loudly instead
// of returning junk.
const methodGetBlock = "getBlock"

func (solanaSpec) fixtures(t *testing.T, folder string) []chainFixture {
	return loadSolanaFixtures(t, folder,
		"make solana-golden-fetch captures a suitable mainnet slot into this folder (requires SOLANA_RPC_URL; SOLANA_API_KEY is optional)")
}

func (solanaSpec) modes() []receiptMode {
	return []receiptMode{{name: "embedded metadata"}}
}

func (solanaSpec) server(t *testing.T, fixtures []chainFixture, _ receiptMode) *httptest.Server {
	// The mode is ignored on purpose: getBlock carries the metadata and
	// rewards inline, so there is no receipt-path split to force.
	return newSolanaRPCServer(fixtures).Server
}

func (solanaSpec) fetcher(t *testing.T, serverURL string) chains.Fetcher {
	// The HTTP dial is lazy and NewClient performs no I/O, so an httptest
	// URL works. The options mirror the production defaults
	// (config/config_solana.yaml): confirmed commitment, version ceiling
	// one, rewards on. No API key: the mock is headerless. Connect stays
	// out of the freeze, matching the tron and evm specs, which also build
	// pre-connected clients.
	client, err := solana.NewClient(context.Background(), solana.ClientOptions{
		RPCURL:                         serverURL,
		Commitment:                     "confirmed",
		MaxSupportedTransactionVersion: 1,
		Rewards:                        true,
	})
	require.NoError(t, err)
	return solana.NewFetcher(client)
}

func (solanaSpec) converter(t *testing.T, cfg *config.Config) chains.Converter {
	// NewConverter is lenient by design: no error return, and a nil or
	// empty config falls back to the Solana default prefix.
	return solana.NewConverter(cfg)
}

func (solanaSpec) assertCoverage(t *testing.T, raw any) {
	block, ok := raw.(*solana.Block)
	require.True(t, ok, "expected *solana.Block, got %T", raw)
	assertSolanaBundleCoverage(t, block)
}

func (solanaSpec) goldens() []goldenCollectionSpec {
	return solanaGoldenCollections
}

// loadSolanaFixtures loads every captured getBlock fixture from a chain's
// blocks folder. Solana has no receipts twin: the metadata and rewards ride
// the getBlock response, so the loader reads blocks/ only and leaves the
// fixture's receiptsRaw empty. The slot is the decimal file stem, because
// the getBlock result names no slot of its own; the capture script
// guarantees the stem matches the request it answered. captureHint tells a
// missing or unreadable fixture set how to fix itself, so a chain that has
// not been captured yet fails with its own capture instructions.
func loadSolanaFixtures(t *testing.T, chainDir, captureHint string) []chainFixture {
	t.Helper()

	blocksDir := filepath.Join(chainDir, "testdata", "blocks")
	entries, err := os.ReadDir(blocksDir)
	require.NoError(t, err, "read block fixtures at %s (%s)", blocksDir, captureHint)

	var fixtures []chainFixture
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), ".json")
		slot, err := strconv.ParseInt(stem, 10, 64)
		require.NoError(t, err, "solana fixture stem %q must be a decimal slot", stem)

		blockRaw := loadFixtureResult(t, filepath.Join(blocksDir, entry.Name()))
		var meta struct {
			Blockhash string `json:"blockhash"`
		}
		require.NoError(t, json.Unmarshal(blockRaw, &meta),
			"solana block fixture %s: missing or malformed blockhash", entry.Name())
		require.NotEmpty(t, meta.Blockhash,
			"solana block fixture %s: no blockhash field", entry.Name())

		fixtures = append(fixtures, chainFixture{
			height:   slot,
			hash:     meta.Blockhash,
			blockRaw: blockRaw,
		})
	}
	require.NotEmpty(t, fixtures, "no block fixtures found in %s; %s", blocksDir, captureHint)
	return fixtures
}

// solanaRPCServer replays the captured getBlock bodies verbatim over
// JSON-RPC: blocks are addressed by the requested decimal slot, the same
// key the production client fetches by. Unknown slots and unexpected
// methods answer a JSON-RPC error so stray traffic fails loudly instead of
// returning junk.
type solanaRPCServer struct {
	*httptest.Server
	bySlot map[int64]chainFixture
}

func newSolanaRPCServer(fixtures []chainFixture) *solanaRPCServer {
	bySlot := make(map[int64]chainFixture, len(fixtures))
	for _, fixture := range fixtures {
		bySlot[fixture.height] = fixture
	}
	s := &solanaRPCServer{bySlot: bySlot}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *solanaRPCServer) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		ID     json.RawMessage `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	id := req.ID
	if len(id) == 0 {
		id = json.RawMessage("null")
	}

	result, rpcErr := s.serve(req.Method, req.Params)
	w.Header().Set("Content-Type", "application/json")
	var resp map[string]any
	if rpcErr != nil {
		resp = map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"error":   map[string]any{"code": -32000, "message": rpcErr.Error()},
		}
	} else {
		resp = map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  result,
		}
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// serve resolves one RPC method against the captured fixtures.
func (s *solanaRPCServer) serve(method string, params json.RawMessage) (json.RawMessage, error) {
	if method != methodGetBlock {
		return nil, fmt.Errorf("unexpected RPC method %q: the golden flow only exercises the getBlock fetch", method)
	}

	var args []json.RawMessage
	if err := json.Unmarshal(params, &args); err != nil || len(args) == 0 {
		return nil, fmt.Errorf("malformed params for %s", method)
	}

	var slot int64
	if err := json.Unmarshal(args[0], &slot); err != nil {
		return nil, fmt.Errorf("%s: expected a decimal slot parameter", method)
	}

	fixture, ok := s.bySlot[slot]
	if !ok {
		return nil, fmt.Errorf("no fixture captured for slot %d", slot)
	}
	return fixture.blockRaw, nil
}

// assertSolanaBundleCoverage pins the fixture coverage the freeze requires:
// both transaction outcomes, CPI inner instructions, a real pre/post token
// balance pair, ALT-loaded addresses, log messages, and rewards, so the
// goldens can only be generated from output that populates every frozen
// collection with real content.
func assertSolanaBundleCoverage(t *testing.T, block *solana.Block) {
	t.Helper()

	require.NotNil(t, block, "fetched block is nil")
	require.NotEmpty(t, block.Blockhash, "fetched block has no blockhash")
	require.NotEqual(t, "11111111111111111111111111", block.Blockhash,
		"fetched block carries the all-ones zero blockhash; the capture is not a produced block")
	require.NotEmpty(t, block.Transactions, "fixture slot carries no transactions")

	successful, failed := 0, 0
	inner, tokenBalances, loadedAddresses, logs := false, false, false, false
	for i := range block.Transactions {
		tx := &block.Transactions[i]
		// A failed transaction carries the raw JSON error in Err; a
		// successful one leaves it empty, so key presence, not value,
		// splits the outcomes.
		if tx.Err == "" {
			successful++
		} else {
			failed++
		}
		if len(tx.InnerInstructions) > 0 {
			inner = true
		}
		if len(tx.PreTokenBalances) > 0 && len(tx.PostTokenBalances) > 0 {
			tokenBalances = true
		}
		if len(tx.LoadedAddresses) > 0 {
			loadedAddresses = true
		}
		if len(tx.LogMessages) > 0 {
			logs = true
		}
	}

	require.NotZero(t, successful, "fixture slot carries no successful transaction; pick a slot with both outcomes")
	require.NotZero(t, failed, "fixture slot carries no failed transaction; pick a slot with both outcomes")
	require.True(t, inner, "fixture slot carries no CPI inner instruction; pick a slot with program invocations")
	require.True(t, tokenBalances, "fixture slot carries no transaction with both pre and post token balances; pick a slot with SPL token movement")
	require.True(t, loadedAddresses, "fixture slot carries no ALT-loaded address; pick a slot with a versioned transaction using lookup tables")
	require.True(t, logs, "fixture slot carries no log message; pick a slot with program output")
	require.NotEmpty(t, block.Rewards, "fixture slot carries no rewards; capture with the rewards parameter enabled")
}

// solanaGoldenCollections lists the collections the solana freeze reads
// back. The field lists mirror the SDL field sets with relations and
// _docID excluded.
var solanaGoldenCollections = []goldenCollectionSpec{
	{
		name:   "block",
		column: func(c chains.Collections) string { return c.(*solana.CollectionNames).Block },
		fields: []string{
			"hash", "number", "parentHash", "blockTime",
			"transactionCount", "rewardCount",
		},
	},
	{
		name:   "transaction",
		column: func(c chains.Collections) string { return c.(*solana.CollectionNames).Transaction },
		fields: []string{
			"signature", "blockHash", "blockNumber", "version", "err", "fee",
			"computeUnitsConsumed", "logMessages", "preBalances", "postBalances",
			"recentBlockhash", "accountKeys", "loadedAddresses",
		},
	},
	{
		name:   "instruction",
		column: func(c chains.Collections) string { return c.(*solana.CollectionNames).Instruction },
		fields: []string{
			"transactionSignature", "instructionIndex", "innerIndex",
			"programId", "accounts", "data", "stackHeight",
			"blockHash", "blockNumber",
		},
	},
	{
		name:   "tokenBalanceChange",
		column: func(c chains.Collections) string { return c.(*solana.CollectionNames).TokenBalanceChange },
		fields: []string{
			"transactionSignature", "mint", "owner", "tokenAccount",
			"preAmount", "postAmount", "programId", "blockHash", "blockNumber",
		},
	},
	{
		name:   "reward",
		column: func(c chains.Collections) string { return c.(*solana.CollectionNames).Reward },
		fields: []string{
			"pubkey", "lamports", "postBalance", "rewardType",
			"commission", "blockNumber",
		},
	},
}
