package solana

import (
	"regexp"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fieldDefined reports whether the SDL defines a field with the given name.
// The check is line-anchored on purpose: a naive substring match for "hash:"
// would pass on a "blockHash:" line, hiding exactly the constant-vs-field
// drift this file exists to catch.
func fieldDefined(sdl, name string) bool {
	return regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s*:`).MatchString(sdl)
}

// TestSolanaFieldNamesMatchCollectionSDL pins the solana-owned document field
// names to the collection SDL they must land in. The converter's builders
// write these constants as document keys, and pruning/signing queries embed
// them by name, so a constant drifting from its SDL field would compile fine
// and only fail as a runtime query error. SDL fields without a solana
// constant (such as layer-relation fields) are written as literals or carry
// the host contract and are asserted below; the guarded direction is
// constant to SDL.
func TestSolanaFieldNamesMatchCollectionSDL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file  string
		field string
	}{
		// Block document. number/hash/parentHash carry the continuity and
		// host contract; parentHash is asserted because the generic reorg
		// walk-back reads it from the block group.
		{file: blockCollectionFile, field: ParentHashFieldName},
		{file: blockCollectionFile, field: BlockTimeFieldName},
		{file: blockCollectionFile, field: TransactionCountFieldName},
		{file: blockCollectionFile, field: RewardCountFieldName},

		// Transaction document.
		{file: transactionCollectionFile, field: SignatureFieldName},
		{file: transactionCollectionFile, field: VersionFieldName},
		{file: transactionCollectionFile, field: ErrFieldName},
		{file: transactionCollectionFile, field: FeeFieldName},
		{file: transactionCollectionFile, field: ComputeUnitsConsumedFieldName},
		{file: transactionCollectionFile, field: LogMessagesFieldName},
		{file: transactionCollectionFile, field: PreBalancesFieldName},
		{file: transactionCollectionFile, field: PostBalancesFieldName},
		{file: transactionCollectionFile, field: RecentBlockhashFieldName},
		{file: transactionCollectionFile, field: AccountKeysFieldName},
		{file: transactionCollectionFile, field: LoadedAddressesFieldName},

		// Instruction document.
		{file: instructionCollectionFile, field: TransactionSignatureFieldName},
		{file: instructionCollectionFile, field: ProgramIDFieldName},
		{file: instructionCollectionFile, field: AccountsFieldName},
		{file: instructionCollectionFile, field: DataFieldName},
		{file: instructionCollectionFile, field: StackHeightFieldName},

		// TokenBalanceChange document.
		{file: tokenBalanceChangeCollectionFile, field: TransactionSignatureFieldName},
		{file: tokenBalanceChangeCollectionFile, field: MintFieldName},
		{file: tokenBalanceChangeCollectionFile, field: OwnerFieldName},
		{file: tokenBalanceChangeCollectionFile, field: TokenAccountFieldName},
		{file: tokenBalanceChangeCollectionFile, field: PreAmountFieldName},
		{file: tokenBalanceChangeCollectionFile, field: PostAmountFieldName},
		{file: tokenBalanceChangeCollectionFile, field: ProgramIDFieldName},

		// Reward document.
		{file: rewardCollectionFile, field: PubkeyFieldName},
		{file: rewardCollectionFile, field: LamportsFieldName},
		{file: rewardCollectionFile, field: PostBalanceFieldName},
		{file: rewardCollectionFile, field: RewardTypeFieldName},
		{file: rewardCollectionFile, field: CommissionFieldName},
	}

	sdls := make(map[string]string, 5)
	for _, tt := range tests {
		if _, ok := sdls[tt.file]; !ok {
			sdl, err := readCollectionSDL(tt.file)
			require.NoError(t, err, "load SDL %s", tt.file)
			sdls[tt.file] = sdl
		}
	}

	for _, tt := range tests {
		assert.True(t, fieldDefined(sdls[tt.file], tt.field),
			"SDL %s does not define field %q expected from the solana field-name constants, SDL and constants have drifted",
			tt.file, tt.field)
	}
}

// TestConstantsMatchCollectionSDL pins the pkg/constants contract field names
// to the embedded collection SDL. These names are written into documents by
// two independent sites (the solana converters' builders and the defra
// BlockHandler) and filtered on by pruning, signing, reorg, and snapshot
// queries, so a constant drifting from its SDL field would only surface as
// runtime query failures, never at compile time. The block number/hash
// fields are the generator-host contract (pkg/constants owns them per the
// documented solana boundary): number holds the slot value and hash holds
// the blockhash. Non-constant SDL fields are not asserted: a field defined
// in the SDL but written from a string literal is legal; the guarded
// direction is constant to SDL.
func TestConstantsMatchCollectionSDL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file  string
		field string
	}{
		// Block document: its own number/hash join fields. The host prunes,
		// queries stored hashes, and bootstraps the Block collection by
		// these names, so they are part of the generator-host contract this
		// file pins.
		{file: blockCollectionFile, field: constants.NumberFieldName},
		{file: blockCollectionFile, field: constants.HashFieldName},

		// Data documents carry the block number/hash payload fields.
		{file: transactionCollectionFile, field: constants.BlockNumberFieldName},
		{file: transactionCollectionFile, field: constants.BlockHashFieldName},
		{file: instructionCollectionFile, field: constants.BlockNumberFieldName},
		{file: instructionCollectionFile, field: constants.BlockHashFieldName},
		{file: tokenBalanceChangeCollectionFile, field: constants.BlockNumberFieldName},
		{file: tokenBalanceChangeCollectionFile, field: constants.BlockHashFieldName},
		{file: rewardCollectionFile, field: constants.BlockNumberFieldName},

		// BlockSignature carries the full signature-document contract.
		{file: blockSignatureCollectionFile, field: constants.BlockNumberFieldName},
		{file: blockSignatureCollectionFile, field: constants.BlockHashFieldName},
		{file: blockSignatureCollectionFile, field: constants.MerkleRootFieldName},
		{file: blockSignatureCollectionFile, field: constants.CIDCountFieldName},
		{file: blockSignatureCollectionFile, field: constants.CIDsFieldName},
		{file: blockSignatureCollectionFile, field: constants.SignatureTypeFieldName},
		{file: blockSignatureCollectionFile, field: constants.SignatureIdentityFieldName},
		{file: blockSignatureCollectionFile, field: constants.SignatureValueFieldName},
		{file: blockSignatureCollectionFile, field: constants.CreatedAtFieldName},

		// SnapshotSignature shares the signature fields; block identity comes
		// from startBlock/endBlock instead of the shared pair.
		{file: snapshotSignatureCollectionFile, field: constants.MerkleRootFieldName},
		{file: snapshotSignatureCollectionFile, field: constants.SignatureTypeFieldName},
		{file: snapshotSignatureCollectionFile, field: constants.SignatureIdentityFieldName},
		{file: snapshotSignatureCollectionFile, field: constants.SignatureValueFieldName},
		{file: snapshotSignatureCollectionFile, field: constants.CreatedAtFieldName},
	}

	sdls := make(map[string]string, 7)
	for _, tt := range tests {
		if _, ok := sdls[tt.file]; !ok {
			sdl, err := readCollectionSDL(tt.file)
			require.NoError(t, err, "load SDL %s", tt.file)
			sdls[tt.file] = sdl
		}
	}

	for _, tt := range tests {
		assert.True(t, fieldDefined(sdls[tt.file], tt.field),
			"SDL %s does not define field %q expected from pkg/constants, SDL and constants have drifted",
			tt.file, tt.field)
	}
}
