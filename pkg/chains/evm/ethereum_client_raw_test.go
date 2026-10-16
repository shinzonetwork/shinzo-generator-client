package evm

// Tests for the ClientAdapter raw-block path: chains whose adapter declares
// NeedsRawBlock fetch the raw eth_getBlockByNumber response and decode
// transactions through the adapter, while the default adapter keeps the
// typed ethclient path byte-identical.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	pkgerrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
)

// rawBlockHash is a deliberately non-recomputable block hash: no header
// derived from the standard fields could ever produce it, so any assertion
// matching it proves the hash came from the RPC response.
const rawBlockHash = "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

// stubRawAdapter declares NeedsRawBlock and maps a ZKsync-style 0x71
// envelope onto a legacy transaction, the closest standard envelope, so the
// existing gas-price switch keeps working. The sender and yParity are read
// from the raw tx object.
type stubRawAdapter struct{}

func (stubRawAdapter) NeedsRawBlock() bool { return true }

func (stubRawAdapter) DecodeTransactions(rawBlock json.RawMessage) ([]*ethtypes.Transaction, error) {
	var view struct {
		Transactions []json.RawMessage `json:"transactions"`
	}
	if err := json.Unmarshal(rawBlock, &view); err != nil {
		return nil, err
	}

	txs := make([]*ethtypes.Transaction, 0, len(view.Transactions))
	for _, rawTx := range view.Transactions {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawTx, &probe); err != nil {
			return nil, err
		}
		if probe.Type != "0x71" {
			return nil, fmt.Errorf("unexpected transaction type %q", probe.Type)
		}

		var fields struct {
			Nonce    string `json:"nonce"`
			GasPrice string `json:"gasPrice"`
			Gas      string `json:"gas"`
			To       string `json:"to"`
			Value    string `json:"value"`
			Input    string `json:"input"`
		}
		if err := json.Unmarshal(rawTx, &fields); err != nil {
			return nil, err
		}

		nonce, err := hexutil.DecodeUint64(fields.Nonce)
		if err != nil {
			return nil, err
		}
		gasPrice, err := hexutil.DecodeBig(fields.GasPrice)
		if err != nil {
			return nil, err
		}
		gas, err := hexutil.DecodeUint64(fields.Gas)
		if err != nil {
			return nil, err
		}
		value, err := hexutil.DecodeBig(fields.Value)
		if err != nil {
			return nil, err
		}
		input, err := hexutil.Decode(fields.Input)
		if err != nil {
			return nil, err
		}

		to := new(common.HexToAddress(fields.To))
		txs = append(txs, ethtypes.NewTx(&ethtypes.LegacyTx{
			Nonce:    nonce,
			GasPrice: gasPrice,
			Gas:      gas,
			To:       to,
			Value:    value,
			Data:     input,
		}))
	}
	return txs, nil
}

func (stubRawAdapter) ResolveFrom(_ *ethtypes.Transaction, rawTx json.RawMessage) (string, bool) {
	if len(rawTx) == 0 {
		return "", false
	}
	var fields struct {
		From string `json:"from"`
	}
	if err := json.Unmarshal(rawTx, &fields); err != nil {
		return "", false
	}
	if fields.From == "" {
		return "", false
	}
	return fields.From, true
}

func (stubRawAdapter) YParity(_ *ethtypes.Transaction, rawTx json.RawMessage) string {
	if len(rawTx) == 0 {
		return ""
	}
	var fields struct {
		YParity string `json:"yParity"`
	}
	if err := json.Unmarshal(rawTx, &fields); err != nil {
		return ""
	}
	return fields.YParity
}

// fakeTxAdapter is a configurable ClientAdapter for conversion wiring tests.
type fakeTxAdapter struct {
	needsRaw  bool
	from      string
	fromOK    bool
	yParity   string
	decodeErr error
	txs       []*ethtypes.Transaction
}

func (f fakeTxAdapter) NeedsRawBlock() bool { return f.needsRaw }

func (f fakeTxAdapter) DecodeTransactions(_ json.RawMessage) ([]*ethtypes.Transaction, error) {
	return f.txs, f.decodeErr
}

func (f fakeTxAdapter) ResolveFrom(_ *ethtypes.Transaction, _ json.RawMessage) (string, bool) {
	return f.from, f.fromOK
}

func (f fakeTxAdapter) YParity(_ *ethtypes.Transaction, _ json.RawMessage) string {
	return f.yParity
}

// zksyncStyleTx builds a raw 0x71 transaction object as a node would return
// it inside eth_getBlockByNumber, with an explicit from and yParity.
func zksyncStyleTx() map[string]any {
	return map[string]any{
		"type":     "0x71",
		"hash":     "0x0000000000000000000000000000000000000000000000000000000000000abc",
		"nonce":    "0x1",
		"gasPrice": "0x3b9aca00",
		"gas":      "0x5208",
		"to":       "0x0000000000000000000000000000000000000002",
		"value":    "0x1",
		"input":    "0xdeadbeef",
		"chainId":  "0x144",
		"from":     "0x1111111111111111111111111111111111111111",
		"yParity":  "0x0",
		"v":        "0x1",
		"r":        "0x2",
		"s":        "0x3",
	}
}

// rawZksyncBlockResponse builds a raw block fixture carrying the 0x71 tx.
// The transactions root is non-empty so the typed ethclient path passes its
// list consistency checks and fails precisely on the unknown tx type, and
// the size and total difficulty are set for RPC-provided-value assertions.
func rawZksyncBlockResponse() map[string]any {
	return rawZksyncBlockResponseWithTxs([]any{zksyncStyleTx()})
}

// rawZksyncBlockResponseWithTxs builds the same block fixture with a
// caller-provided transactions array, letting conversion tests exercise
// adapter response shapes the single-tx fixture cannot express.
func rawZksyncBlockResponseWithTxs(txs []any) map[string]any {
	block := fullBlockResponse(testBlockNumberHex, txs)
	block[constants.HashFieldName] = rawBlockHash
	block[TransactionsRootFieldName] = "0x1111111111111111111111111111111111111111111111111111111111111111"
	block["size"] = "0x1a4"
	block["totalDifficulty"] = "0x1f4"
	return block
}

// newRawPathClient builds a client whose adapter declares NeedsRawBlock.
func newRawPathClient(t *testing.T, serverURL string, adapter ClientAdapter) *EthereumClient {
	t.Helper()
	client, err := NewEthereumClient(t.Context(), serverURL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	client.adapter = adapter
	return client
}

// --- GetBlockByNumber raw path ---

func TestGetBlockByNumber_RawPath_NonStandardEnvelope(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		if method == ethGetBlockByNumber {
			var args []json.RawMessage
			var paramsOK bool
			if uerr := json.Unmarshal(params, &args); uerr == nil {
				paramsOK = len(args) == 2
			}
			if paramsOK {
				var full bool
				if ferr := json.Unmarshal(args[1], &full); ferr != nil || !full {
					assert.Fail(t, "raw fetch must request full transaction objects, params: %s", params)
				}
			} else {
				assert.Fail(t, "unexpected eth_getBlockByNumber params: %s", params)
			}
			return rawZksyncBlockResponse(), nil
		}
		return "0x1", nil
	})
	defer server.Close()

	client := newRawPathClient(t, server.URL, stubRawAdapter{})

	block, err := client.GetBlockByNumber(context.Background(), big.NewInt(testBlockNumber))
	require.NoError(t, err)
	require.NotNil(t, block)

	// Block-level values come from the RPC response, not from recomputation.
	assert.Equal(t, rawBlockHash, block.Hash)
	assert.Equal(t, "420", block.Size)
	assert.Equal(t, "500", block.TotalDifficulty)
	assert.Equal(t, strconv.Itoa(testBlockNumber), block.Number)

	require.Len(t, block.Transactions, 1)
	tx := block.Transactions[0]
	assert.Equal(t, "0x1111111111111111111111111111111111111111", tx.From)
	assert.Equal(t, "0x0", tx.YParity)
	assert.Equal(t, "0", tx.Type, "0x71 must map onto the closest standard envelope")
	assert.Equal(t, "1000000000", tx.GasPrice)
	assert.Equal(t, rawBlockHash, tx.BlockHash)
	assert.Equal(t, strconv.Itoa(testBlockNumber), tx.BlockNumber)
}

func TestGetBlockByNumber_TypedPath_FailsOnNonStandardEnvelope(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		if method == ethGetBlockByNumber {
			return rawZksyncBlockResponse(), nil
		}
		return "0x1", nil
	})
	defer server.Close()

	// Default adapter: the typed ethclient decode hits the unknown 0x71
	// envelope and fails, which is exactly the failure class the raw path
	// removes.
	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, err = client.GetBlockByNumber(context.Background(), big.NewInt(testBlockNumber))
	require.Error(t, err)
	assert.True(t, pkgerrors.IsErrUnsupportedTxType(err), "expected an unsupported tx type error, got: %v", err)
}

func TestGetBlockByNumber_RawPath_NotFound(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		if method == ethGetBlockByNumber {
			return nil, nil //nolint:nilnil // JSON-RPC result: null, the node's response for a missing block
		}
		return "0x1", nil
	})
	defer server.Close()

	client := newRawPathClient(t, server.URL, stubRawAdapter{})

	_, err := client.GetBlockByNumber(context.Background(), big.NewInt(testBlockNumber))
	require.Error(t, err)
	assert.ErrorIs(t, err, ethereum.NotFound)
}

func TestGetBlockByNumber_RawPath_DecodeErrorPropagates(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		if method == ethGetBlockByNumber {
			return rawZksyncBlockResponse(), nil
		}
		return "0x1", nil
	})
	defer server.Close()

	client := newRawPathClient(t, server.URL, fakeTxAdapter{
		needsRaw:  true,
		decodeErr: errors.New("boom"),
	})

	_, err := client.GetBlockByNumber(context.Background(), big.NewInt(testBlockNumber))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode raw block transactions")
	assert.Contains(t, err.Error(), "boom")
}

// --- GetLatestBlock raw path ---

func TestGetLatestBlock_RawPath(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		if method == ethGetBlockByNumber {
			// Serves both HeaderByNumber (typed header decode) and the raw
			// block fetch; the extra fields are ignored by the header decode.
			return rawZksyncBlockResponse(), nil
		}
		return "0x1", nil
	})
	defer server.Close()

	client := newRawPathClient(t, server.URL, stubRawAdapter{})

	block, err := client.GetLatestBlock(context.Background())
	require.NoError(t, err)
	require.NotNil(t, block)
	assert.Equal(t, rawBlockHash, block.Hash)
	require.Len(t, block.Transactions, 1)
	assert.Equal(t, "0x1111111111111111111111111111111111111111", block.Transactions[0].From)
	assert.Equal(t, "0x0", block.Transactions[0].YParity)
}

// --- convertRawBlock internals ---

func TestConvertRawBlock_RawTxCountMismatch(t *testing.T) {
	t.Parallel()

	tx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil)

	t.Run("adapter decoded more txs than the response carries", func(t *testing.T) {
		t.Parallel()
		// Two decoded transactions against one raw tx object is a broken
		// adapter contract, so the block fails instead of nil-padding the
		// missing raw txs.
		raw, err := json.Marshal(rawZksyncBlockResponse())
		require.NoError(t, err)

		client := &EthereumClient{adapter: fakeTxAdapter{
			needsRaw: true,
			txs:      []*ethtypes.Transaction{tx, tx},
		}}

		block, err := client.convertRawBlock(raw)
		require.Error(t, err)
		assert.Nil(t, block)
		assert.Contains(t, err.Error(), "adapter decoded 2 transactions but the response carries 1 raw tx objects")
	})

	t.Run("adapter decoded fewer txs than the response carries", func(t *testing.T) {
		t.Parallel()
		// One decoded transaction against two raw tx objects must fail too:
		// index pairing would silently store each later tx's neighbour's
		// from and yParity.
		raw, err := json.Marshal(rawZksyncBlockResponseWithTxs([]any{zksyncStyleTx(), zksyncStyleTx()}))
		require.NoError(t, err)

		client := &EthereumClient{adapter: fakeTxAdapter{
			needsRaw: true,
			txs:      []*ethtypes.Transaction{tx},
		}}

		block, err := client.convertRawBlock(raw)
		require.Error(t, err)
		assert.Nil(t, block)
		assert.Contains(t, err.Error(), "adapter decoded 1 transactions but the response carries 2 raw tx objects")
	})
}

// --- default adapter equivalence ---

func TestConvertGethBlock_NilAdapterMatchesDefaultAdapter(t *testing.T) {
	t.Parallel()
	chainID := big.NewInt(1)
	key, _ := defaultTestKey()
	tx, err := ethtypes.SignNewTx(key, ethtypes.NewLondonSigner(chainID), &ethtypes.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(1000000000),
		GasFeeCap: big.NewInt(2000000000),
		Gas:       21000,
		To:        new(common.HexToAddress("0xto")),
		Value:     big.NewInt(1000),
	})
	require.NoError(t, err)

	header := &ethtypes.Header{Number: big.NewInt(testBlockNumber)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{Transactions: []*ethtypes.Transaction{tx}}, nil, trie.NewStackTrie(nil))

	plain := &EthereumClient{}
	withDefault := &EthereumClient{adapter: defaultAdapter}
	assert.Equal(t, withDefault.convertGethBlock(gethBlock), plain.convertGethBlock(gethBlock))
}

func TestTransaction_JSONOmitsEmptyYParity(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(fakeTx("0xabc"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "yParity",
		"the default adapter leaves yParity empty and the JSON output must stay byte-identical")

	txWithParity := fakeTx("0xabc")
	txWithParity.YParity = "0x1"
	data, err = json.Marshal(txWithParity)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"yParity":"0x1"`)
}

// --- convertTransactionWithRaw adapter hooks ---

func TestConvertTransactionWithRaw_ResolveFromOverride(t *testing.T) {
	t.Parallel()
	chainID := big.NewInt(1)
	key, _ := defaultTestKey()
	tx, err := ethtypes.SignNewTx(key, ethtypes.NewLondonSigner(chainID), &ethtypes.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(1000000000),
		GasFeeCap: big.NewInt(2000000000),
		Gas:       21000,
		To:        new(common.HexToAddress("0xto")),
		Value:     big.NewInt(1000),
	})
	require.NoError(t, err)

	header := &ethtypes.Header{Number: big.NewInt(testBlockNumber)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	explicit := common.HexToAddress("0x9999999999999999999999999999999999999999").Hex()
	client := &EthereumClient{adapter: fakeTxAdapter{from: explicit, fromOK: true, yParity: "0x1"}}

	localTx := client.convertTransaction(tx, gethBlock, 0)
	assert.Equal(t, explicit, localTx.From, "the adapter's explicit sender must override the recovered one")
	assert.Equal(t, "0x1", localTx.YParity)
}

func TestConvertTransactionWithRaw_ResolveFromNotOK_KeepsExistingBehaviour(t *testing.T) {
	t.Parallel()
	unsigned := ethtypes.NewTx(&ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       new(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	})

	header := &ethtypes.Header{Number: big.NewInt(testBlockNumber)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{adapter: fakeTxAdapter{from: "0xignored", fromOK: false}}

	localTx := client.convertTransaction(unsigned, gethBlock, 0)
	assert.Equal(t, ZeroAddress, localTx.From, "ok=false must keep the GetFromAddress recovery and zero address fallback")
	assert.Empty(t, localTx.YParity)
}
