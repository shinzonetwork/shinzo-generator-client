//go:build bench

package solanabench

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	solana "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defracontext"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// solana package shims: the benchmark harness lives outside the solana
// package, so it carries its own copies of the fake builders the internal
// tests use (helpers_test.go there is unavailable to external code) and
// prefixes every adapter type with the package alias.

func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}

// benchConfig mirrors the internal test config: production-shaped solana
// defaults the store path never dials.
func benchConfig() *config.Config {
	return &config.Config{
		Chain: config.ChainConfig{
			Name:    "Solana",
			Network: "Mainnet",
		},
		Solana: config.SolanaConfig{
			RPCURL:                         "https://api.mainnet-beta.solana.com",
			Commitment:                     config.DefaultSolanaCommitment,
			MaxSupportedTransactionVersion: config.DefaultSolanaMaxSupportedTxVersion,
		},
		Indexer: config.IndexerConfig{
			MaxDocsPerTxn: 100,
		},
	}
}

// fakePubkey derives a deterministic base58-looking key from a seed. The
// characters stay within the base58 alphabet so the values read like real
// pubkeys; only uniqueness across seeds is guaranteed.
func fakePubkey(seed string) string {
	out := make([]byte, 0, len(seed)*2)
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	for i := 0; i < len(seed); i++ {
		out = append(out, alphabet[int(seed[i])%len(alphabet)])
		out = append(out, alphabet[(int(seed[i])+i)%len(alphabet)])
	}
	return string(out)
}

// fakeSignature derives a deterministic 88-char base58-looking signature.
func fakeSignature(seed string) string {
	sig := fakePubkey(seed)
	for len(sig) < 88 {
		tail := sig
		if len(tail) > 8 {
			tail = tail[len(tail)-8:]
		}
		sig += fakePubkey("pad" + tail)
	}
	return sig[:88]
}

func fakeInstruction(outerIdx, innerIdx int, seed string) solana.Instruction {
	return solana.Instruction{
		ProgramID:        fakePubkey("prog-" + seed),
		Accounts:         []string{fakePubkey("acct-a-" + seed), fakePubkey("acct-b-" + seed)},
		Data:             fakePubkey("data-" + seed),
		InstructionIndex: outerIdx,
		InnerIndex:       innerIdx,
	}
}

// fakeTransaction builds a transaction with one outer instruction, one inner
// instruction group (one CPI instruction under the outer), and a matched
// pre/post token-balance pair resolvable against the committed keys.
func fakeTransaction(slot uint64, txIndex int, seed string) solana.Transaction {
	outer := fakeInstruction(0, 0, seed+"-outer")
	outer.StackHeight = nil

	inner := fakeInstruction(0, 0, seed+"-inner")
	stackHeight := uint16(2)
	inner.StackHeight = &stackHeight

	return solana.Transaction{
		Signature:               fakeSignature(seed),
		Slot:                    slot,
		TransactionIndex:        txIndex,
		Version:                 "legacy",
		Failed:                  false,
		Err:                     "",
		Fee:                     5000,
		ComputeUnitsConsumed:    nil,
		LogMessages:             []string{"Program log: entrypoint exited"},
		PreBalances:             []uint64{100000, 200000},
		PostBalances:            []uint64{95000, 200000},
		RecentBlockhash:         fakePubkey("recent-" + seed),
		AccountKeys:             []string{fakePubkey("acct-0-" + seed), fakePubkey("acct-1-" + seed)},
		LoadedAddressesWritable: nil,
		LoadedAddressesReadonly: nil,
		Instructions:            []solana.Instruction{outer},
		InnerInstructions: []solana.InnerInstructionGroup{
			{Index: 0, Instructions: []solana.Instruction{inner}},
		},
		PreTokenBalances: []solana.TokenBalance{
			{
				AccountIndex: 1,
				Mint:         fakePubkey("mint-" + seed),
				Owner:        fakePubkey("owner-" + seed),
				ProgramID:    fakePubkey("tokenprog-" + seed),
				Amount:       "100",
				Decimals:     6,
			},
		},
		PostTokenBalances: []solana.TokenBalance{
			{
				AccountIndex: 1,
				Mint:         fakePubkey("mint-" + seed),
				Owner:        fakePubkey("owner-" + seed),
				ProgramID:    fakePubkey("tokenprog-" + seed),
				Amount:       "150",
				Decimals:     6,
			},
		},
	}
}

func fakeReward(seed string, lamports int64, commission *uint8) solana.Reward {
	return solana.Reward{
		Pubkey:      fakePubkey("reward-" + seed),
		Lamports:    lamports,
		PostBalance: 987654321,
		RewardType:  "Voting",
		Commission:  commission,
	}
}

func fakeBlock(slot uint64) *solana.Block {
	return &solana.Block{
		Slot:              slot,
		Blockhash:         fakePubkey(fmt.Sprintf("blockhash-%d", slot)),
		PreviousBlockhash: fakePubkey(fmt.Sprintf("prev-%d", slot)),
		ParentSlot:        slot - 1,
		BlockHeight:       nil,
		BlockTime:         nil,
		Transactions:      nil,
		Rewards:           nil,
	}
}

// fakeBlockWithTxs builds a block carrying the given transactions and two
// rewards (a fee reward without commission, one voting reward with
// commission).
func fakeBlockWithTxs(slot uint64, txs ...solana.Transaction) *solana.Block {
	b := fakeBlock(slot)
	b.Transactions = txs
	commission := uint8(5)
	b.Rewards = []solana.Reward{
		fakeReward("fee", -25000, nil),
		fakeReward("vote", 405, &commission),
	}
	b.Rewards[0].RewardType = "Fee"
	return b
}

// ---------------------------------------------------------------------------
// Fake rpc client (test seam).
//
// The unexported rpcClient interface behind NewFetcher cannot be named from
// outside the solana package, so satisfaction is structural: the method set
// below matches it and the NewFetcher call site enforces it at build time.
// Arithmetic values (tip) and errors replicate the behaviour the internal
// fakes rely on; only the paths the benchmarks exercise are implemented.
// ---------------------------------------------------------------------------

type fakeSlotClient struct {
	mu sync.Mutex

	blockFn func(ctx context.Context, slot uint64) (*solana.Block, error)
	tip     uint64
}

func (f *fakeSlotClient) GetBlock(ctx context.Context, slot uint64) (*solana.Block, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blockFn(ctx, slot)
}

func (f *fakeSlotClient) GetBlockFromArchive(context.Context, uint64) (*solana.Block, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return nil, fmt.Errorf("archive endpoint not configured in the bench fake")
}

func (f *fakeSlotClient) GetSlot(context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tip, nil
}

func (f *fakeSlotClient) Close() error {
	return nil
}

// ---------------------------------------------------------------------------
// Bench store: the full production stack (embedded DefraDB + BlockHandler +
// signing identity) the benchmarks run against.
// ---------------------------------------------------------------------------

type benchStore struct {
	td      *testutils.TestDefraDB
	ctx     context.Context
	handler *defra.BlockHandler
	conv    *solana.Converter
}

// benchBackend values name the storage engine the bench node runs on.
type benchBackend int

const (
	benchBackendDisk benchBackend = iota
	benchBackendMemory
	backendBadgerMemory
)

// benchDefraBackend resolves BENCH_DEFRADB_BACKEND: disk (the default,
// matching production) or memory (corekv b-tree, uncapped transactions).
// badger-memory is recognized but fails fast: the pinned defradb build
// hardcodes a 256-byte badger value threshold that rejects every
// in-memory write above it. The legacy BENCH_DEFRADB_IN_MEMORY variable
// fails loudly so a stale invocation cannot silently measure a different
// backend.
func benchDefraBackend(tb testing.TB) benchBackend {
	tb.Helper()
	if raw, ok := os.LookupEnv("BENCH_DEFRADB_IN_MEMORY"); ok {
		tb.Fatalf("BENCH_DEFRADB_IN_MEMORY was replaced by BENCH_DEFRADB_BACKEND=disk|memory|badger-memory (legacy value %q is not honored)", raw)
	}
	switch raw := os.Getenv("BENCH_DEFRADB_BACKEND"); raw {
	case "", "disk":
		return benchBackendDisk
	case "memory":
		return benchBackendMemory
	case "badger-memory":
		tb.Fatalf("BENCH_DEFRADB_BACKEND=badger-memory is not supported by the pinned defradb build: its 256-byte hardcoded badger value threshold rejects every in-memory write above it")
		return benchBackendDisk
	default:
		tb.Fatalf("invalid BENCH_DEFRADB_BACKEND=%q: use disk, memory, or badger-memory", raw)
		return benchBackendDisk
	}
}

func benchBackendName(backend benchBackend) string {
	switch backend {
	case benchBackendMemory:
		return "defra-in-memory"
	default:
		return "defra-disk"
	}
}

// newBenchStore stands up an embedded DefraDB via the shared testutils
// helpers (disk-backed by default; BENCH_DEFRADB_BACKEND selects memory or
// badger-memory) with the real solana schema, a production BlockHandler, and
// a signing identity context — the same stack the indexer runs in
// production. Works for both *testing.T and *testing.B.
func newBenchStore(tb testing.TB, maxDocsPerTxn int) *benchStore {
	tb.Helper()

	cfg := benchConfig()
	if maxDocsPerTxn > 0 {
		cfg.Indexer.MaxDocsPerTxn = maxDocsPerTxn
	}
	conv := solana.NewConverter(cfg)

	sdl, err := conv.GetSchema()
	require.NoError(tb, err)

	backend := benchDefraBackend(tb)
	tb.Logf("bench DefraDB backend: %s", benchBackendName(backend))

	var td *testutils.TestDefraDB
	switch backend {
	case benchBackendMemory:
		td = testutils.SetupTestDefraDBWithSchemaInMemory(tb, sdl)
	default:
		td = testutils.SetupTestDefraDBWithSchema(tb, sdl)
	}

	handler, err := defra.NewBlockHandler(td.Node, maxDocsPerTxn)
	require.NoError(tb, err)

	fullIdent, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(tb, err)
	ctx := defracontext.WithIdentity(context.Background(), fullIdent)

	return &benchStore{td: td, ctx: ctx, handler: handler, conv: conv}
}
