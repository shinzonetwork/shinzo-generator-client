package evm

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

// TestEVMFieldNamesMatchCollectionSDL pins the evm-owned document field names
// to the collection SDL they must land in. The converter's builder maps write
// these constants as document keys, and pruning/signing queries embed them by
// name, so a constant drifting from its SDL field would compile fine and only
// fail as a runtime query error. The block document's number/hash fields —
// the generator-host contract — live in pkg/constants and are asserted below
// in TestConstantsMatchCollectionSDL. SDL fields without an evm constant
// (from/totalDifficulty/topics/...) are written as literals and are
// intentionally not asserted; the guarded direction is constant → SDL.
func TestEVMFieldNamesMatchCollectionSDL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file  string
		field string
	}{
		// Block document. The block number/hash fields are the generator-
		// host contract and live in pkg/constants, asserted in
		// TestConstantsMatchCollectionSDL; the rest are JSON-RPC payload
		// carried through.
		{file: blockCollectionFile, field: TimestampFieldName},
		{file: blockCollectionFile, field: ParentHashFieldName},
		{file: blockCollectionFile, field: DifficultyFieldName},
		{file: blockCollectionFile, field: GasUsedFieldName},
		{file: blockCollectionFile, field: GasLimitFieldName},
		{file: blockCollectionFile, field: NonceFieldName},
		{file: blockCollectionFile, field: MinerFieldName},
		{file: blockCollectionFile, field: StateRootFieldName},
		{file: blockCollectionFile, field: Sha3UnclesFieldName},
		{file: blockCollectionFile, field: TransactionsRootFieldName},
		{file: blockCollectionFile, field: ReceiptsRootFieldName},
		{file: blockCollectionFile, field: LogsBloomFieldName},
		{file: blockCollectionFile, field: ExtraDataFieldName},
		{file: blockCollectionFile, field: MixHashFieldName},

		// Transaction document.
		{file: transactionCollectionFile, field: NonceFieldName},
		{file: transactionCollectionFile, field: TransactionIndexFieldName},
		{file: transactionCollectionFile, field: TypeFieldName},
		{file: transactionCollectionFile, field: CumulativeGasUsedFieldName},
		{file: transactionCollectionFile, field: EffectiveGasPriceFieldName},
		{file: transactionCollectionFile, field: StatusFieldName},

		// Log document.
		{file: logCollectionFile, field: AddressFieldName},
		{file: logCollectionFile, field: TransactionHashFieldName},
		{file: logCollectionFile, field: TransactionIndexFieldName},

		// Access list entry document.
		{file: accessListEntryCollectionFile, field: AddressFieldName},
	}

	sdls := make(map[string]string, 4)
	for _, tt := range tests {
		if _, ok := sdls[tt.file]; !ok {
			sdl, err := readCollectionSDL(tt.file)
			require.NoError(t, err, "load SDL %s", tt.file)
			sdls[tt.file] = sdl
		}
	}

	for _, tt := range tests {
		assert.True(t, fieldDefined(sdls[tt.file], tt.field),
			"SDL %s does not define field %q expected from the evm field-name constants — SDL and constants have drifted",
			tt.file, tt.field)
	}
}

// TestConstantsMatchCollectionSDL pins the pkg/constants contract field names
// to the embedded collection SDL. These names are written into documents by
// two independent sites (the evm converter's signature builder and the defra
// BlockHandler) and filtered on by signing queries, so a constant drifting
// from its SDL field would only surface as runtime query failures, never at
// compile time. The block number/hash fields are the generator-host contract
// (pkg/constants owns them per the documented evm boundary). Non-constant SDL
// fields are not asserted: a field defined in the SDL but written from a
// string literal is legal; the guarded direction is constant → SDL.
func TestConstantsMatchCollectionSDL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file  string
		field string
	}{
		// Block document: its own number/hash join fields. The host prunes
		// and bootstraps the Block collection by these names, so they are
		// part of the generator-host contract this file pins.
		{file: blockCollectionFile, field: constants.NumberFieldName},
		{file: blockCollectionFile, field: constants.HashFieldName},

		// Data documents carry the block number/hash payload fields.
		{file: transactionCollectionFile, field: constants.HashFieldName},
		{file: transactionCollectionFile, field: constants.BlockNumberFieldName},
		{file: transactionCollectionFile, field: constants.BlockHashFieldName},
		{file: logCollectionFile, field: constants.BlockNumberFieldName},
		{file: logCollectionFile, field: constants.BlockHashFieldName},
		{file: accessListEntryCollectionFile, field: constants.BlockNumberFieldName},

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

	sdls := make(map[string]string, 6)
	for _, tt := range tests {
		if _, ok := sdls[tt.file]; !ok {
			sdl, err := readCollectionSDL(tt.file)
			require.NoError(t, err, "load SDL %s", tt.file)
			sdls[tt.file] = sdl
		}
	}

	for _, tt := range tests {
		assert.True(t, fieldDefined(sdls[tt.file], tt.field),
			"SDL %s does not define field %q expected from pkg/constants — SDL and constants have drifted",
			tt.file, tt.field)
	}
}
