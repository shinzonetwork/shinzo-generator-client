package bitcoin

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

// TestBitcoinFieldNamesMatchCollectionSDL pins the bitcoin-owned document
// field names to the collection SDL they must land in. The converter's
// builder maps write these constants as document keys, and stamping/purge
// queries embed TxIDFieldName by name, so a constant drifting from its SDL
// field would compile fine and only fail as a runtime query error. The
// block document's number/hash fields, the blockNumber/blockHash child
// payload fields, and the signature-document fields are the generator-host
// contract and live in pkg/constants, asserted below in
// TestConstantsMatchCollectionSDL. SDL fields without a bitcoin constant
// (version, locktime) are written as literals and are intentionally not
// asserted; the guarded direction is constant → SDL.
func TestBitcoinFieldNamesMatchCollectionSDL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file  string
		field string
	}{
		// Block document.
		{file: blockCollectionFile, field: ParentHashFieldName},
		{file: blockCollectionFile, field: MerkleRootFieldName},
		{file: blockCollectionFile, field: TimeFieldName},
		{file: blockCollectionFile, field: BitsFieldName},
		{file: blockCollectionFile, field: NonceFieldName},
		{file: blockCollectionFile, field: ChainworkFieldName},

		// Transaction document. txid is the join field the link stamper and
		// purge stages resolve, so its drift guard is load-bearing.
		{file: transactionCollectionFile, field: TxIDFieldName},
		{file: transactionCollectionFile, field: TransactionIndexFieldName},
		{file: transactionCollectionFile, field: IsCoinbaseFieldName},
		{file: transactionCollectionFile, field: FeeSatFieldName},

		// Transaction input document.
		{file: txInputCollectionFile, field: TxIDFieldName},
		{file: txInputCollectionFile, field: InputIndexFieldName},
		{file: txInputCollectionFile, field: PrevTxIDFieldName},
		{file: txInputCollectionFile, field: PrevVoutFieldName},
		{file: txInputCollectionFile, field: ScriptSigFieldName},
		{file: txInputCollectionFile, field: SequenceFieldName},
		{file: txInputCollectionFile, field: CoinbaseFieldName},
		{file: txInputCollectionFile, field: WitnessFieldName},
		{file: txInputCollectionFile, field: PrevoutValueSatFieldName},
		{file: txInputCollectionFile, field: PrevoutAddressFieldName},
		{file: txInputCollectionFile, field: PrevoutTypeFieldName},

		// Transaction output document.
		{file: txOutputCollectionFile, field: TxIDFieldName},
		{file: txOutputCollectionFile, field: VoutIndexFieldName},
		{file: txOutputCollectionFile, field: ValueSatFieldName},
		{file: txOutputCollectionFile, field: ScriptPubKeyHexFieldName},
		{file: txOutputCollectionFile, field: ScriptPubKeyAddressFieldName},
		{file: txOutputCollectionFile, field: ScriptPubKeyTypeFieldName},
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
			"SDL %s does not define field %q expected from the bitcoin field-name constants, SDL and constants have drifted",
			tt.file, tt.field)
	}
}

// TestConstantsMatchCollectionSDL pins the pkg/constants contract field
// names to the embedded collection SDL. These names are written into
// documents by two independent sites (the bitcoin converter and the defra
// BlockHandler) and filtered on by signing and pruning queries, so a
// constant drifting from its SDL field would only surface as runtime query
// failures, never at compile time. The block number/hash fields are the
// generator-host contract (pkg/constants owns them per the documented
// bitcoin boundary). Non-constant SDL fields are not asserted: a field
// defined in the SDL but written from a string literal is legal; the
// guarded direction is constant → SDL.
func TestConstantsMatchCollectionSDL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file  string
		field string
	}{
		// Block document: its own number/hash fields. The host prunes and
		// bootstraps the Block collection by these names, so they are part
		// of the generator-host contract this file pins.
		{file: blockCollectionFile, field: constants.NumberFieldName},
		{file: blockCollectionFile, field: constants.HashFieldName},

		// Transaction document carries the block identity payload fields on
		// top of its own hash (the wtxid, sharing the hash field name with
		// the block document).
		{file: transactionCollectionFile, field: constants.HashFieldName},
		{file: transactionCollectionFile, field: constants.BlockNumberFieldName},
		{file: transactionCollectionFile, field: constants.BlockHashFieldName},

		// Input and output documents reach the block through the block
		// number alone (no blockHash field, no block relation): the block
		// association and the owning transaction resolve via the txid and
		// transaction relations.
		{file: txInputCollectionFile, field: constants.BlockNumberFieldName},
		{file: txOutputCollectionFile, field: constants.BlockNumberFieldName},

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
			"SDL %s does not define field %q expected from pkg/constants, SDL and constants have drifted",
			tt.file, tt.field)
	}
}
