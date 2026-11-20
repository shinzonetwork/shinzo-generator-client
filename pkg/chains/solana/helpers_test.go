package solana

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}

func testConfig() *config.Config {
	return &config.Config{
		Chain: config.ChainConfig{
			Name:    "Solana",
			Network: "Mainnet",
		},
		Solana: config.SolanaConfig{
			NodeURL:                        "https://api.mainnet-beta.solana.com",
			Commitment:                     config.DefaultSolanaCommitment,
			MaxSupportedTransactionVersion: config.DefaultSolanaMaxSupportedTxVersion,
		},
		Indexer: config.IndexerConfig{
			MaxDocsPerTxn: 1000,
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

func fakeInstruction(seed string) Instruction {
	return Instruction{
		ProgramID: fakePubkey("prog-" + seed),
		Accounts:  []string{fakePubkey("acct-a-" + seed), fakePubkey("acct-b-" + seed)},
		Data:      fakePubkey("data-" + seed),
	}
}

// fakeInnerInstruction builds an inner (CPI) instruction carrying the
// stackHeight key, mirroring the wire rule that only inner instructions
// report a stack depth.
func fakeInnerInstruction(seed string, stackHeight uint16) Instruction {
	instr := fakeInstruction(seed)
	instr.StackHeight = &stackHeight
	return instr
}

// fakeTransaction builds a transaction with one outer instruction, one inner
// instruction group (one CPI instruction under the outer), and a matched
// pre/post token-balance pair resolvable against the committed keys.
func fakeTransaction(seed string) Transaction {
	return Transaction{
		Signature:            fakeSignature(seed),
		Version:              "legacy",
		Err:                  "",
		Fee:                  5000,
		ComputeUnitsConsumed: nil,
		LogMessages:          []string{"Program log: entrypoint exited"},
		PreBalances:          []uint64{100000, 200000},
		PostBalances:         []uint64{95000, 200000},
		RecentBlockhash:      fakePubkey("recent-" + seed),
		AccountKeys:          []string{fakePubkey("acct-0-" + seed), fakePubkey("acct-1-" + seed)},
		LoadedAddresses:      nil,
		Instructions:         []Instruction{fakeInstruction(seed + "-outer")},
		InnerInstructions: []InnerInstructionGroup{
			{Index: 0, Instructions: []Instruction{fakeInnerInstruction(seed+"-inner", 2)}},
		},
		PreTokenBalances: []TokenBalance{
			{
				AccountIndex: 1,
				Mint:         fakePubkey("mint-" + seed),
				Owner:        fakePubkey("owner-" + seed),
				ProgramID:    fakePubkey("tokenprog-" + seed),
				Amount:       "100",
				Decimals:     6,
			},
		},
		PostTokenBalances: []TokenBalance{
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

// fakeFailedTransaction builds a transaction whose meta.err is set. Failure
// is expressed by the err key only: the wire has no boolean marker.
func fakeFailedTransaction(seed string) Transaction {
	tx := fakeTransaction(seed)
	tx.Err = `{"InstructionError":[0,{"Custom":1}]}`
	return tx
}

func fakeReward(seed string, lamports int64, commission *uint8) Reward {
	return Reward{
		Pubkey:      fakePubkey("reward-" + seed),
		Lamports:    lamports,
		PostBalance: 987654321,
		RewardType:  "Voting",
		Commission:  commission,
	}
}

func fakeBlock(slot uint64) *Block {
	return &Block{
		Slot:              slot,
		Blockhash:         fakePubkey(fmt.Sprintf("blockhash-%d", slot)),
		PreviousBlockhash: fakePubkey(fmt.Sprintf("prev-%d", slot)),
		BlockTime:         nil,
		Transactions:      nil,
		Rewards:           nil,
	}
}

// fakeBlockWithTxs builds a block carrying the given transactions and two
// rewards (a fee reward without commission, one voting reward with
// commission).
func fakeBlockWithTxs(slot uint64, txs ...Transaction) *Block {
	b := fakeBlock(slot)
	b.Transactions = txs
	commission := uint8(5)
	b.Rewards = []Reward{
		fakeReward("fee", -25000, nil),
		fakeReward("vote", 405, &commission),
	}
	b.Rewards[0].RewardType = "Fee"
	return b
}

// testFetcher wires a fetcher with a pre-connected fake client.
func testFetcher(client *fakeSlotClient) *Fetcher {
	return NewFetcher(client)
}

// NewFetcherFromConfigForTest mirrors NewFetcherFromConfig with a minimal
// config, used where only the disconnected state matters.
func NewFetcherFromConfigForTest() *Fetcher {
	f, _ := NewFetcherFromConfig(&config.Config{
		Solana: config.SolanaConfig{NodeURL: "https://r", Commitment: "confirmed"},
	})
	return f
}

// testHeader is shorthand for the cached header shape.
func testHeader(slot uint64) chains.BlockHeader {
	return chains.BlockHeader{
		Number:     int64(slot), //nolint:gosec // test slots are small
		Hash:       fakePubkey(fmt.Sprintf("blockhash-%d", slot)),
		ParentHash: fakePubkey(fmt.Sprintf("prev-%d", slot)),
	}
}

// requireSlotHeader asserts a cached header's identity fields.
func requireSlotHeader(t *testing.T, got, want chains.BlockHeader) {
	t.Helper()
	require.Equal(t, want.Number, got.Number)
	require.Equal(t, want.Hash, got.Hash)
	require.Equal(t, want.ParentHash, got.ParentHash)
}

// ---------------------------------------------------------------------------
// Fake rpcClient (test seam).
// ---------------------------------------------------------------------------

type fakeSlotClient struct {
	mu sync.Mutex

	blockFn                                                    func(ctx context.Context, slot uint64) (*Block, error)
	archiveFn                                                  func(ctx context.Context, slot uint64) (*Block, error)
	tip                                                        uint64
	tipErr                                                     error
	finalized                                                  uint64
	finalErr                                                   error
	blockCalls, archiveCalls, tipCalls, finalCalls, closeCalls int
}

func (f *fakeSlotClient) GetBlock(ctx context.Context, slot uint64) (*Block, error) {
	f.mu.Lock()
	f.blockCalls++
	f.mu.Unlock()
	return f.blockFn(ctx, slot)
}

func (f *fakeSlotClient) GetBlockFromArchive(ctx context.Context, slot uint64) (*Block, error) {
	f.mu.Lock()
	f.archiveCalls++
	f.mu.Unlock()
	if f.archiveFn == nil {
		return nil, errArchiveNotConfigured
	}
	return f.archiveFn(ctx, slot)
}

func (f *fakeSlotClient) GetSlot(context.Context) (uint64, error) {
	f.mu.Lock()
	f.tipCalls++
	f.mu.Unlock()
	if f.tipErr != nil {
		return 0, f.tipErr
	}
	return f.tip, nil
}

func (f *fakeSlotClient) GetFinalizedSlot(context.Context) (uint64, error) {
	f.mu.Lock()
	f.finalCalls++
	f.mu.Unlock()
	if f.finalErr != nil {
		return 0, f.finalErr
	}
	return f.finalized, nil
}

func (f *fakeSlotClient) Close() error {
	f.mu.Lock()
	f.closeCalls++
	f.mu.Unlock()
	return nil
}

func (f *fakeSlotClient) blockCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blockCalls
}

func (f *fakeSlotClient) archiveCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.archiveCalls
}

func (f *fakeSlotClient) tipCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tipCalls
}

func (f *fakeSlotClient) finalizedCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.finalCalls
}

func (f *fakeSlotClient) closeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCalls
}
