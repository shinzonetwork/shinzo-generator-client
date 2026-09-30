package solana

import (
	"fmt"
	"os"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
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
			RPCURL:                         "https://api.mainnet-beta.solana.com",
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

func fakeInstruction(outerIdx, innerIdx int, seed string) Instruction {
	return Instruction{
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
func fakeTransaction(slot uint64, txIndex int, seed string) Transaction {
	outer := fakeInstruction(0, 0, seed+"-outer")
	outer.StackHeight = nil

	inner := fakeInstruction(0, 0, seed+"-inner")
	stackHeight := uint16(2)
	inner.StackHeight = &stackHeight

	return Transaction{
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
		Instructions:            []Instruction{outer},
		InnerInstructions: []InnerInstructionGroup{
			{Index: 0, Instructions: []Instruction{inner}},
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

// fakeFailedTransaction builds a transaction whose meta.err is set.
func fakeFailedTransaction(slot uint64, txIndex int, seed string) Transaction {
	tx := fakeTransaction(slot, txIndex, seed)
	tx.Failed = true
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
