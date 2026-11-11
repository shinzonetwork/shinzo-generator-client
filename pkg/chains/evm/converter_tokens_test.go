package evm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	codecevm "github.com/shinzonetwork/shinzo-evidence-codec/evm"
	"github.com/shinzonetwork/shinzo-evidence-codec/merkle"
	"github.com/shinzonetwork/shinzo-evidence-codec/statement"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// A tokens-mode converter refuses a token address that is not 20 bytes of hex.
func TestNewConverter_InvalidTokenAddress(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Chain.Mode = config.ChainModeTokens
	cfg.Chain.Tokens = []config.Token{{Address: "0x8d0d"}}
	_, err := NewConverter(cfg)
	require.ErrorIs(t, err, config.ErrInvalidContractAddress)
}

// In tokens mode a block converts to its Header, one Evidence document per listed token with
// transactions in the block, and its statement. Each token's proof verifies its records against
// the header's roots.
func TestConvert_TokenMode(t *testing.T) {
	t.Parallel()
	listedA := common.HexToAddress("0xaa00000000000000000000000000000000000001")
	listedB := common.HexToAddress("0xbb00000000000000000000000000000000000002")
	unlisted := common.HexToAddress("0xcc00000000000000000000000000000000000003")
	cfg := testConfig()
	cfg.Chain.Name = "Bsc"
	cfg.Chain.ChainID = 56
	cfg.Chain.Mode = config.ChainModeTokens
	cfg.Chain.Tokens = []config.Token{{Address: listedB.Hex()}, {Address: "0xAA00000000000000000000000000000000000001"}}
	c, err := NewConverter(cfg)
	require.NoError(t, err)

	logsFrom := func(addresses ...common.Address) []*ethtypes.Log {
		logs := make([]*ethtypes.Log, len(addresses))
		for i, a := range addresses {
			logs[i] = &ethtypes.Log{Address: a}
		}
		return logs
	}
	bundle := gethBundle(t, 42,
		logsFrom(listedA),
		logsFrom(unlisted),
		logsFrom(listedB, listedA),
		logsFrom(listedA, listedA),
	)
	header := bundle.Block.Header

	result, err := c.Convert(context.Background(), bundle)
	require.NoError(t, err)
	assert.Equal(t, "Bsc__Mainnet__BlockAttestation", result.SignatureCollection)
	require.Len(t, result.Groups, 2)
	assert.Equal(t, "Bsc__Mainnet__Header", result.Groups[0].Collection)
	assert.Equal(t, header.Hash().Hex(), result.Groups[0].Docs[0][constants.HashFieldName])

	evidence := result.Groups[1]
	assert.Equal(t, "Bsc__Mainnet__Evidence", evidence.Collection)
	require.Len(t, evidence.Docs, 2)
	roots := make([][sha256.Size]byte, len(evidence.Docs))
	for i, want := range []struct {
		asset   string
		indices []uint64
	}{
		{"aa00000000000000000000000000000000000001", []uint64{0, 2, 3}},
		{"bb00000000000000000000000000000000000002", []uint64{2}},
	} {
		doc := evidence.Docs[i]
		assert.Equal(t, want.asset, doc[AssetFieldName])
		assert.Equal(t, int64(42), doc[constants.BlockNumberFieldName])
		assert.Equal(t, header.Hash().Hex(), doc[constants.BlockHashFieldName])
		records, root, err := codecevm.DecodeRecords(decodeHexField(t, doc, RecordsFieldName))
		require.NoError(t, err)
		roots[i] = root
		indices := make([]uint64, len(records))
		for j, r := range records {
			indices[j] = r.Index
		}
		assert.Equal(t, want.indices, indices)
		require.NoError(t, codecevm.VerifyRecords(header.ReceiptHash, header.TxHash, records, decodeHexField(t, doc, ProofFieldName)))
	}

	st, err := statement.Decode(result.Statement)
	require.NoError(t, err)
	assert.Equal(t, "eip155:56", st.Chain)
	assert.Equal(t, uint64(42), st.Number)
	assert.Equal(t, header.Hash().Hex(), st.Hash)
	assert.Equal(t, header.ParentHash.Hex(), st.ParentHash)
	assert.Equal(t, uint64(41), st.ParentNumber)
	assert.Equal(t, header.Time*1000, st.Timestamp)
	assert.Equal(t, statement.StatusFinalized, st.Status)
	assert.Equal(t, config.Version, st.Extractor)
	assert.Equal(t, statement.FilterContractLog, st.Filter)
	assert.Equal(t, sha256.Sum256(decodeHexField(t, result.Groups[0].Docs[0], RawFieldName)), st.HeaderHash)
	assert.Equal(t, uint64(4), st.Items)
	assert.Equal(t, []statement.Claim{
		{Asset: "aa00000000000000000000000000000000000001", Count: 3, Root: roots[0]},
		{Asset: "bb00000000000000000000000000000000000002", Count: 1, Root: roots[1]},
	}, st.Claims)

	// A block with no listed token's logs still lists the Evidence group, empty, and the statement
	// claims each token with count 0 and the empty list's root.
	result, err = c.Convert(context.Background(), gethBundle(t, 43, logsFrom(unlisted)))
	require.NoError(t, err)
	require.Len(t, result.Groups, 2)
	assert.Empty(t, result.Groups[1].Docs)
	st, err = statement.Decode(result.Statement)
	require.NoError(t, err)
	assert.Equal(t, []statement.Claim{
		{Asset: "aa00000000000000000000000000000000000001", Root: merkle.Root(nil)},
		{Asset: "bb00000000000000000000000000000000000002", Root: merkle.Root(nil)},
	}, st.Claims)

	bundle.Block.Header = nil
	_, err = c.Convert(context.Background(), bundle)
	require.Error(t, err)
}

// A header is read as the RLP header, which hashes to the block hash, with the block time in
// milliseconds and BSC's extra milliseconds read from mixHash. Each fixture is a node's
// eth_getBlockByNumber result without its transaction and withdrawal lists.
func TestReadHeader(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file          string
		chainID       uint64
		wantTimestamp uint64
	}{
		{"testdata/ethereum_mainnet_header.json", 1, 1793278739000},
		{"testdata/bsc_mainnet_header.json", 56, 1793279901900},
	} {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(tc.file)
			require.NoError(t, err)
			var node struct {
				Hash common.Hash `json:"hash"`
			}
			require.NoError(t, json.Unmarshal(raw, &node))
			var header ethtypes.Header
			require.NoError(t, json.Unmarshal(raw, &header))
			require.Equal(t, node.Hash, header.Hash(), "the fixture decodes to the node's hash")

			cfg := testConfig()
			cfg.Chain.Mode = config.ChainModeTokens
			cfg.Chain.ChainID = tc.chainID
			c, err := NewConverter(cfg)
			require.NoError(t, err)

			raw, decoded, timestamp, err := c.readHeader(&header)
			require.NoError(t, err)
			assert.Equal(t, node.Hash, decoded.Hash)
			assert.Equal(t, node.Hash, crypto.Keccak256Hash(raw))
			assert.Equal(t, tc.wantTimestamp, timestamp)
		})
	}
}

// decodeHexField returns the bytes of a document's Blob field, stored as hex.
func decodeHexField(t *testing.T, doc map[string]any, field string) []byte {
	t.Helper()
	value, ok := doc[field].(string)
	require.True(t, ok, field)
	b, err := hex.DecodeString(value)
	require.NoError(t, err, field)
	return b
}

func TestTokenMode_ReadsTokenTables(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Chain.Name = "Bsc"
	cfg.Chain.Mode = config.ChainModeTokens
	cfg.Chain.Tokens = []config.Token{{Address: "0x8d0d000ee44948fc98c9b98a4fa4921476f08b0d"}}
	c, err := NewConverter(cfg)
	require.NoError(t, err)
	sdl, err := c.GetSchema()
	require.NoError(t, err)
	td := testutils.SetupTestDefraDBWithSchema(t, sdl)
	ctx := context.Background()

	for _, add := range []string{
		fmt.Sprintf(`%s(input: {number: 7, hash: "0x07"})`, c.collections.Header),
		fmt.Sprintf(`%s(input: {number: 9, hash: "0x09"})`, c.collections.Header),
		fmt.Sprintf(`%s(input: {blockNumber: 7, asset: "8d0d000ee44948fc98c9b98a4fa4921476f08b0d"})`, c.collections.Evidence),
		fmt.Sprintf(`%s(input: {blockNumber: 7, asset: "8d0d000ee44948fc98c9b98a4fa4921476f08b0d"})`, c.collections.StateCapture),
		fmt.Sprintf(`%s(input: {blockNumber: 7})`, c.collections.BlockAttestation),
	} {
		res := td.Node.DB.ExecRequest(ctx, "mutation { add_"+add+" { _docID } }")
		require.Empty(t, res.GQL.Errors)
	}

	lowest, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(7), lowest)
	highest, err := c.GetHighestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(9), highest)

	docIDs, err := c.GetDocIDsByBlockRange(ctx, td.Node, 7, 7)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{c.collections.Header, c.collections.Evidence, c.collections.StateCapture, c.collections.BlockAttestation}, slices.Collect(maps.Keys(docIDs)))
	for name, ids := range docIDs {
		assert.Len(t, ids, 1, name)
	}
}
