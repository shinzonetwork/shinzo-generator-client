package pruner

import (
	"context"
	"fmt"
	"testing"

	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	solana "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defracontext"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// Solana prune round-trip: slots stored through the production path
// (solana converter → LinkStamper → BlockHandler, with signed blocks) are
// pruned by the real pruner code, leaving exactly the retention window and
// no purge residue that would corrupt the lowest/highest slot queries. This
// pins the chain-agnostic pruner against the solana collection model ("slot"
// instead of "number", plus the instruction/token-balance/reward collection
// set).
var solanaPrunerSecKey = crypto.KeyTypeSecp256k1 //nolint:gochecknoglobals

// solanaPruneBlock builds a lean signed-storable block for slot: one tx with
// one outer and one inner instruction, and one reward.
func solanaPruneBlock(slot int64) *solana.Block {
	blockTime := slot
	height := uint64(slot) //nolint:gosec // slots stay small in tests
	stackHeight := uint16(2)
	return &solana.Block{
		Slot:              uint64(slot),
		Blockhash:         fmt.Sprintf("hash-%d", slot),
		PreviousBlockhash: fmt.Sprintf("prev-%d", slot),
		ParentSlot:        uint64(slot - 1),
		BlockHeight:       &height,
		BlockTime:         &blockTime,
		Transactions: []solana.Transaction{{
			Signature:        fmt.Sprintf("sig-%d", slot),
			Slot:             uint64(slot),
			TransactionIndex: 0,
			Version:          "legacy",
			Fee:              5000,
			AccountKeys:      []string{fmt.Sprintf("acct0-%d", slot), fmt.Sprintf("acct1-%d", slot)},
			Instructions: []solana.Instruction{{
				ProgramID: "prog-vote", Accounts: []string{fmt.Sprintf("acct0-%d", slot)},
				Data: fmt.Sprintf("data-%d", slot), InstructionIndex: 0, InnerIndex: 0,
			}},
			InnerInstructions: []solana.InnerInstructionGroup{{
				Index: 0,
				Instructions: []solana.Instruction{{
					ProgramID: "prog-swap", Accounts: []string{fmt.Sprintf("acct1-%d", slot)},
					Data: fmt.Sprintf("idata-%d", slot), InstructionIndex: 0, InnerIndex: 0,
					StackHeight: &stackHeight,
				}},
			}},
		}},
		Rewards: []solana.Reward{
			{Pubkey: fmt.Sprintf("reward-%d", slot), Lamports: 500, PostBalance: 1, RewardType: "Voting"},
		},
	}
}

// solanaPruneFixture carries an embedded DefraDB with the solana schema, the
// solana converter, and the production block handler used for seeding.
type solanaPruneFixture struct {
	td      *testutils.TestDefraDB
	conv    *solana.Converter
	ctx     context.Context
	handler *defra.BlockHandler
}

func newSolanaPruneFixture(t *testing.T) *solanaPruneFixture {
	t.Helper()

	conv := solana.NewConverter(nil)
	sdl, err := conv.GetSchema()
	require.NoError(t, err)
	td := testutils.SetupTestDefraDBWithSchema(t, sdl)

	handler, err := defra.NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	fullIdent, err := identity.Generate(solanaPrunerSecKey)
	require.NoError(t, err)

	return &solanaPruneFixture{
		td:      td,
		conv:    conv,
		ctx:     defracontext.WithIdentity(context.Background(), fullIdent),
		handler: handler,
	}
}

// seedSolanaSlots stores slots [start, end] through the production path.
func (f *solanaPruneFixture) seedSolanaSlots(t *testing.T, start, end int64) {
	t.Helper()
	for slot := start; slot <= end; slot++ {
		result, err := f.conv.Convert(f.ctx, solanaPruneBlock(slot))
		require.NoError(t, err)
		_, err = f.handler.Store(f.ctx, result)
		require.NoError(t, err, "store slot %d", slot)
	}
}

// TestPruner_SolanaStartupCleanupPrunesOldestSlots runs the real
// startup-cleanup prune path against a store of solana slots and verifies
// that exactly the excess is removed across every collection, including the
// block-signature docs, and that the retained range stays fully intact.
func TestPruner_SolanaStartupCleanupPrunesOldestSlots(t *testing.T) {
	fixture := newSolanaPruneFixture(t)
	// Five signed slots; retention of two → prune the three oldest.
	fixture.seedSolanaSlots(t, 200, 204)

	p := NewPruner(&config.PrunerConfig{
		Enabled:         true,
		MaxBlocks:       2,
		DocsPerBlock:    1,
		IntervalSeconds: 3600,
		PruneHistory:    true,
	}, fixture.td.Node, fixture.conv)
	require.NoError(t, p.startupCleanup(fixture.ctx))

	lowest, err := fixture.conv.GetLowestStoredBlockNumber(fixture.ctx, fixture.td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(203), lowest, "pruner must leave the newest max_blocks slots")

	highest, err := fixture.conv.GetHighestStoredBlockNumber(fixture.ctx, fixture.td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(204), highest)

	// Every collection is empty in the pruned range — including the block
	// signatures and purge-residue-free lowest-slot query above.
	prunedIDs, err := fixture.conv.GetDocIDsByBlockRange(fixture.ctx, fixture.td.Node, 200, 202)
	require.NoError(t, err)
	assert.Empty(t, prunedIDs, "no document may survive below the retention floor")

	// The retained range remains complete across every collection.
	retainedIDs, err := fixture.conv.GetDocIDsByBlockRange(fixture.ctx, fixture.td.Node, 203, 204)
	require.NoError(t, err)
	assert.NotEmpty(t, retainedIDs[solanaCollection(solana.DefaultCollections(), "Block")])
	assert.NotEmpty(t, retainedIDs[solanaCollection(solana.DefaultCollections(), "Transaction")])
	assert.NotEmpty(t, retainedIDs[solanaCollection(solana.DefaultCollections(), "Instruction")],
		"both outer and inner instruction docs survive for retained slots")
	assert.NotEmpty(t, retainedIDs[solanaCollection(solana.DefaultCollections(), "BlockSignature")],
		"block signature docs for retained slots survive")
}

// TestPruner_SolanaRunPruneFilterBased exercises the runPrune dispatch (no
// queue configured → filter-based prune) over solana collections.
func TestPruner_SolanaRunPruneFilterBased(t *testing.T) {
	fixture := newSolanaPruneFixture(t)
	// Seven signed slots; retention of three → the run prunes 300..303.
	fixture.seedSolanaSlots(t, 300, 306)

	p := NewPruner(&config.PrunerConfig{
		Enabled:         true,
		MaxBlocks:       3,
		DocsPerBlock:    1,
		IntervalSeconds: 3600,
		PruneHistory:    true,
	}, fixture.td.Node, fixture.conv)
	// No queue set: runPrune must fall through to filterBasedPrune.
	require.NoError(t, p.runPrune(fixture.ctx))

	lowest, err := fixture.conv.GetLowestStoredBlockNumber(fixture.ctx, fixture.td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(304), lowest)

	metrics := p.GetMetrics()
	assert.Equal(t, int64(4), metrics.TotalBlocksPruned)
	assert.Positive(t, metrics.TotalDocsPruned)
	assert.False(t, metrics.LastPruneTime.IsZero())
}

func solanaCollection(cols []string, suffix string) string {
	for _, col := range cols {
		if len(col) > len(suffix) && col[len(col)-len(suffix):] == suffix {
			return col
		}
	}
	return ""
}
