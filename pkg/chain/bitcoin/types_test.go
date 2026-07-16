package bitcoin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rpcEnvelope mirrors the {"result": …} wrapper Bitcoin Core returns at the
// HTTP layer. The fixture is a captured raw RPC response so we decode through
// the same envelope the live client uses.
type rpcEnvelope struct {
	Result Block `json:"result"`
}

func loadFixtureBlock(t *testing.T) Block {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "block_184498.json"))
	require.NoError(t, err, "read fixture")
	var env rpcEnvelope
	require.NoError(t, json.Unmarshal(data, &env), "decode fixture")
	return env.Result
}

func TestDecodeBlockHeader(t *testing.T) {
	t.Parallel()
	b := loadFixtureBlock(t)

	assert.Equal(t, "0000000000000219b44d3d7b6d7d7f03b0f3d63c95442dde11cec26b14c0d9c6", b.Hash)
	assert.Equal(t, int64(184498), b.Height)
	assert.Equal(t, int32(1), b.Version)
	assert.Equal(t, "0000000000000336fa0157b700d553f5bbcda3e05cf13eff9f7d1db9258e41e4", b.PreviousBlockHash)
	assert.Equal(t, "056c710fc9372ba422fd09cf3c751ec029ec5819dc936f5147c125df0fb44955", b.MerkleRoot)
	assert.Equal(t, int64(1339679543), b.Time)
	assert.Equal(t, "1a0a98d6", b.Bits)
	assert.Equal(t, uint32(1738797167), b.Nonce)
	assert.Equal(t, "0000000000000000000000000000000000000000000000133638d9ff0f0e2d39", b.Chainwork)
}

func TestDecodeBlockTransactionCount(t *testing.T) {
	t.Parallel()
	b := loadFixtureBlock(t)
	assert.Len(t, b.Tx, 327)
}

func TestDecodeCoinbaseTransaction(t *testing.T) {
	t.Parallel()
	b := loadFixtureBlock(t)
	cb := b.Tx[0]

	assert.True(t, cb.IsCoinbase(), "Tx[0] should be coinbase")
	const wantTxid = "d2564428f5c462e825c960b9c1e473d725c8b663d74522bc4327c30c4a2d1afa"
	assert.Equal(t, wantTxid, cb.Txid)
	// For a non-witness tx, hash equals txid.
	assert.Equal(t, wantTxid, cb.Hash)
	assert.Nil(t, cb.Fee, "coinbase has no fee")

	require.Len(t, cb.Vin, 1)
	v := cb.Vin[0]
	assert.NotEmpty(t, v.Coinbase, "coinbase input should carry coinbase script")
	assert.Empty(t, v.Txid, "coinbase input has no prev txid")
	assert.Nil(t, v.PrevOut, "coinbase input has no prevout")
	assert.Nil(t, v.ScriptSig, "coinbase input has no scriptSig")
	assert.Equal(t, uint32(4294967295), v.Sequence)

	require.Len(t, cb.Vout, 1)
	o := cb.Vout[0]
	assert.Equal(t, 50.2207, o.Value)
	assert.Equal(t, "pubkeyhash", o.ScriptPubKey.Type)
	assert.Equal(t, "14MyvXqxuYzCnKZbWdzbCimey9feCGVwkG", o.ScriptPubKey.Address)
	assert.Equal(t, "76a91424e036a6d03d171fc17156947b3a03c30b91067a88ac", o.ScriptPubKey.Hex)
}

func TestDecodeRegularTransactionWithPrevout(t *testing.T) {
	t.Parallel()
	b := loadFixtureBlock(t)
	tx := b.Tx[1]

	assert.False(t, tx.IsCoinbase(), "Tx[1] should not be coinbase")
	assert.Equal(t, "bf8db663aa69ad5754d43c344f8ad65c2e5de09b4450b5ef9696c1e6a12d7452", tx.Txid)

	require.Len(t, tx.Vin, 1)
	v := tx.Vin[0]
	assert.Empty(t, v.Coinbase, "non-coinbase input should not carry coinbase script")
	assert.Equal(t, "f83a35c46fbfc6a45b78a9d9441398feb0f63f257160c2698cf781ea3f389ba6", v.Txid)
	assert.Equal(t, uint32(0), v.Vout)
	require.NotNil(t, v.ScriptSig)
	assert.NotEmpty(t, v.ScriptSig.Hex)

	require.NotNil(t, v.PrevOut, "verbosity 3 should populate prevout")
	assert.Equal(t, 857.05, v.PrevOut.Value)
	assert.Equal(t, "pubkeyhash", v.PrevOut.ScriptPubKey.Type)
	assert.Equal(t, "1bidUhzLJVydrQF1ki4y4S8J1yRsxVPcc", v.PrevOut.ScriptPubKey.Address)

	require.Len(t, tx.Vout, 2)
	require.NotNil(t, tx.Fee, "verbosity 3 has undo data, fee should be present")
	assert.Equal(t, 0.0, *tx.Fee)
}
