package schema_test

import (
	"regexp"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
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

// TestConstantsMatchCollectionSDL pins pkg/constants contract field names to
// the embedded collection SDL. These names are written into documents by two
// independent sites (the evm converter's signature builder and the defra
// BlockHandler) and filtered on by signing queries, so a constant drifting
// from its SDL field would only surface as runtime query failures, never at
// compile time. Non-constant SDL fields are not asserted: a field defined in
// the SDL but written from a string literal is legal; the guarded direction
// is constant → SDL.
func TestConstantsMatchCollectionSDL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file  string
		field string
	}{
		// Data documents carry the block number/hash payload fields.
		{file: "transaction.graphql", field: constants.BlockNumberFieldName},
		{file: "transaction.graphql", field: constants.BlockHashFieldName},
		{file: "log.graphql", field: constants.BlockNumberFieldName},
		{file: "log.graphql", field: constants.BlockHashFieldName},
		{file: "accessListEntry.graphql", field: constants.BlockNumberFieldName},

		// BlockSignature carries the full signature-document contract.
		{file: "blockSignature.graphql", field: constants.BlockNumberFieldName},
		{file: "blockSignature.graphql", field: constants.BlockHashFieldName},
		{file: "blockSignature.graphql", field: constants.MerkleRootFieldName},
		{file: "blockSignature.graphql", field: constants.CIDCountFieldName},
		{file: "blockSignature.graphql", field: constants.CIDsFieldName},
		{file: "blockSignature.graphql", field: constants.SignatureTypeFieldName},
		{file: "blockSignature.graphql", field: constants.SignatureIdentityFieldName},
		{file: "blockSignature.graphql", field: constants.SignatureValueFieldName},
		{file: "blockSignature.graphql", field: constants.CreatedAtFieldName},

		// SnapshotSignature shares the signature fields; block identity comes
		// from startBlock/endBlock instead of the shared pair.
		{file: "snapshotSignature.graphql", field: constants.MerkleRootFieldName},
		{file: "snapshotSignature.graphql", field: constants.SignatureTypeFieldName},
		{file: "snapshotSignature.graphql", field: constants.SignatureIdentityFieldName},
		{file: "snapshotSignature.graphql", field: constants.SignatureValueFieldName},
		{file: "snapshotSignature.graphql", field: constants.CreatedAtFieldName},
	}

	sdls := make(map[string]string, 5)
	for _, tt := range tests {
		if _, ok := sdls[tt.file]; !ok {
			sdl, err := schema.LoadCollectionSDL(tt.file)
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
