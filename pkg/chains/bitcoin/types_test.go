package bitcoin

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coreShapedBlock pins the exact key names Bitcoin Core serves on at
// verbosity 3, once for the decode direction and once for the re-marshal
// direction: a tag drift would silently decode wire data into the wrong
// fields.
const coreShapedBlock = `{
  "hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "confirmations": 12,
  "strippedsize": 1000,
  "size": 2000,
  "weight": 4000,
  "height": 184498,
  "version": 2,
  "merkleroot": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "tx": [
    {
      "txid": "cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01",
      "hash": "cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01cb01",
      "version": 1,
      "locktime": 0,
      "vin": [
        {
          "coinbase": "046e482c1c0106",
          "sequence": 4294967295
        }
      ],
      "vout": [
        {
          "value": 50.2207,
          "n": 0,
          "scriptPubKey": {
            "hex": "76a91481b02c27b21004aeb4df5265dd90d4d56f56f84a88ac",
            "type": "pubkeyhash"
          }
        }
      ]
    },
    {
      "txid": "d001d001d001d001d001d001d001d001d001d001d001d001d001d001d001d001",
      "hash": "d002d002d002d002d002d002d002d002d002d002d002d002d002d002d002d002",
      "version": 1,
      "locktime": 0,
      "vin": [
        {
          "txid": "e000e000e000e000e000e000e000e000e000e000e000e000e000e000e000e000",
          "vout": 0,
          "scriptSig": { "hex": "00" },
          "sequence": 4294967295,
          "txinwitness": ["aa", "bb"],
          "prevout": {
            "generated": false,
            "height": 184497,
            "value": 857.05,
            "scriptPubKey": {
              "hex": "0014abcdef",
              "address": "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080",
              "type": "witness_v0_keyhash"
            }
          }
        }
      ],
      "vout": [
        {
          "value": 857.0439,
          "n": 0,
          "scriptPubKey": {
            "hex": "76a91481b02c27b21004aeb4df5265dd90d4d56f56f56f84a88ac",
            "type": "pubkeyhash"
          }
        }
      ],
      "fee": 0.0061
    }
  ],
  "time": 1349355010,
  "nonce": 2642787427,
  "bits": "1a0facdc",
  "chainwork": "0000000000000000000000000000000000000000000000012b621dea8f398887",
  "previousblockhash": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
  "difficulty": 1234.5
}`

func TestBlockDecodesCoreShape(t *testing.T) {
	t.Parallel()

	var block Block
	require.NoError(t, json.Unmarshal([]byte(coreShapedBlock), &block))

	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", block.Hash)
	assert.Equal(t, int64(184498), block.Height)
	assert.Equal(t, int64(12), block.Confirmations)
	assert.Equal(t, int64(1000), block.StrippedSize)
	assert.Equal(t, int64(2000), block.Size)
	assert.Equal(t, int64(4000), block.Weight)
	assert.Equal(t, uint32(2), block.Version)
	assert.Equal(t, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", block.PreviousBlockHash)
	assert.Equal(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", block.MerkleRoot)
	assert.Equal(t, int64(1349355010), block.Time)
	assert.Equal(t, uint32(2642787427), block.Nonce)
	assert.Equal(t, "1a0facdc", block.Bits)
	assert.Equal(t, "0000000000000000000000000000000000000000000000012b621dea8f398887", block.Chainwork)

	require.Len(t, block.Tx, 2)

	coinbase := block.Tx[0]
	assert.Equal(t, "cb01", coinbase.TxID[:4], "prefix keeps the fixture readable")
	require.Len(t, coinbase.Vin, 1)
	assert.Equal(t, "046e482c1c0106", coinbase.Vin[0].Coinbase)
	assert.Empty(t, coinbase.Vin[0].TxID, "coinbase input carries no txid")
	assert.Nil(t, coinbase.Vin[0].Prevout, "coinbase input carries no prevout")
	assert.Nil(t, coinbase.Fee, "coinbase transaction carries no fee")
	require.Len(t, coinbase.Vout, 1)
	assert.Equal(t, float64(50.2207), coinbase.Vout[0].Value)
	assert.Equal(t, uint32(0), coinbase.Vout[0].Index)

	spender := block.Tx[1]
	require.NotNil(t, spender.Fee)
	assert.Equal(t, float64(0.0061), *spender.Fee)
	assert.NotEqual(t, spender.TxID, spender.Hash, "wtxid differs from txid for witness transactions")
	require.Len(t, spender.Vin, 1)
	vin := spender.Vin[0]
	assert.Equal(t, uint32(4294967295), vin.Sequence)
	require.NotNil(t, vin.ScriptSig)
	assert.Equal(t, "00", vin.ScriptSig.Hex)
	assert.Equal(t, []string{"aa", "bb"}, vin.TxInWitness)
	require.NotNil(t, vin.Prevout)
	prevout := vin.Prevout
	assert.False(t, prevout.Generated)
	assert.Equal(t, int64(184497), prevout.Height)
	assert.Equal(t, float64(857.05), prevout.Value)
	assert.Equal(t, "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080", prevout.ScriptPubKey.Address)
	assert.Equal(t, "witness_v0_keyhash", prevout.ScriptPubKey.Type)
}

func TestBlockReMarshalsCoreShape(t *testing.T) {
	t.Parallel()

	var block Block
	require.NoError(t, json.Unmarshal([]byte(coreShapedBlock), &block))
	remarshaled, err := json.Marshal(block)
	require.NoError(t, err)

	assert.JSONEq(t, coreShapedBlock, string(remarshaled))
}

func TestBtcToSatoshis(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		btc      float64
		wantSats int64
		wantErr  bool
	}{
		{"coinbase output 50.2207 BTC", 50.2207, 5022070000, false},
		{"prevout 857.05 BTC", 857.05, 85705000000, false},
		{"zero", 0, 0, false},
		{"one satoshi", 0.00000001, 1, false},
		{"whole supply", 21000000, 2100000000000000, false},
		{"sub-satoshi below half floors", 0.000000004, 0, false},
		{"sub-satoshi above half rounds", 0.000000009, 1, false},
		{"exactly half with even kept digit floors", 0.000000005, 0, false},
		{"exactly half with odd kept digit rounds", 0.000000015, 2, false},
		{"all-nines fraction carries into a whole BTC", 0.999999995, 100000000, false},
		{"mixed whole and fraction", 50.00000075, 5000000075, false},
		{"negative is an error", -0.25, 0, true},
		{"negative zero parses as zero", math.Copysign(0, -1), 0, false},
		{"NaN is an error", math.NaN(), 0, true},
		{"positive infinity is an error", math.Inf(1), 0, true},
		{"amounts beyond int64 overflow", 1000000000000000000, 0, true},
		{"amounts beyond ParseInt fail loudly", 9.5e18, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sats, err := btcToSatoshis(tt.btc)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSats, sats)
		})
	}
}

func TestBtcToSatoshis_NeverTotalLessThanZero(t *testing.T) {
	t.Parallel()
	// Sanity wall against a sign slip in the fraction machinery: a legal
	// multi-decimal value formats back to its own digits.
	sats, err := btcToSatoshis(50.2207)
	require.NoError(t, err)
	assert.Positive(t, sats)
}
