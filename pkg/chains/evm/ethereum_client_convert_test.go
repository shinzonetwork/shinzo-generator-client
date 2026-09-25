package evm

// Tests for the conversion layer: convertGethBlock, convertTransaction,
// convertGethReceipt, convertGethLog and the get* helper functions they use.

import (
	"fmt"
	"math/big"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- convertGethBlock ---

func TestConvertGethBlock(t *testing.T) {
	t.Parallel()
	header := &ethtypes.Header{
		Number:      big.NewInt(1234567),
		ParentHash:  common.HexToHash("0xparent"),
		Root:        common.HexToHash("0xroot"),
		TxHash:      common.HexToHash("0xtxhash"),
		ReceiptHash: common.HexToHash("0xreceipthash"),
		UncleHash:   common.HexToHash("0xunclehash"),
		Coinbase:    common.HexToAddress("0xcoinbase"),
		Difficulty:  big.NewInt(1000000),
		GasLimit:    8000000,
		GasUsed:     4000000,
		Time:        1600000000,
		Nonce:       ethtypes.BlockNonce{1, 2, 3, 4, 5, 6, 7, 8},
		Extra:       []byte("extra data"),
	}

	tx1 := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), []byte("data"))
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{Transactions: []*ethtypes.Transaction{tx1}}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)

	require.NotNil(t, localBlock)
	assert.Equal(t, gethBlock.Hash().Hex(), localBlock.Hash)
	assert.Equal(t, gethBlock.Number().String(), localBlock.Number)
	assert.Equal(t, 1, len(localBlock.Transactions))
}

func TestConvertGethBlock_NilBlock(t *testing.T) {
	t.Parallel()
	client := &EthereumClient{}
	result := client.convertGethBlock(nil)
	assert.Nil(t, result)
}

func TestConvertGethBlock_WithBaseFee(t *testing.T) {
	t.Parallel()
	header := &ethtypes.Header{
		Number:   big.NewInt(testBlockNumber),
		BaseFee:  big.NewInt(1000000000),
		GasLimit: 8000000,
	}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)

	require.NotNil(t, localBlock)
	assert.Equal(t, "1000000000", localBlock.BaseFeePerGas)
}

func TestConvertGethBlock_WithoutBaseFee(t *testing.T) {
	t.Parallel()
	header := &ethtypes.Header{
		Number:   big.NewInt(testBlockNumber),
		GasLimit: 8000000,
	}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)

	require.NotNil(t, localBlock)
	assert.Equal(t, "", localBlock.BaseFeePerGas)
}

// --- convertTransaction ---

func TestConvertTransaction(t *testing.T) {
	t.Parallel()
	tx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), []byte("test data"))
	header := &ethtypes.Header{Number: big.NewInt(1234567)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx := client.convertTransaction(tx, gethBlock, 0)

	assert.Equal(t, tx.Hash().Hex(), localTx.Hash)
	assert.Equal(t, gethBlock.Number().String(), localTx.BlockNumber)
	assert.Equal(t, tx.To().Hex(), localTx.To)
	assert.Equal(t, tx.Value().String(), localTx.Value)
}

func TestConvertTransaction_ContractCreation(t *testing.T) {
	t.Parallel()
	tx := ethtypes.NewContractCreation(1, big.NewInt(0), 21000, big.NewInt(20000000000), []byte("contract bytecode"))
	header := &ethtypes.Header{Number: big.NewInt(1234567)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx := client.convertTransaction(tx, gethBlock, 0)

	assert.Equal(t, "", localTx.To)
}

func TestConvertTransaction_EIP1559(t *testing.T) {
	t.Parallel()
	chainID := big.NewInt(1)
	key, _ := defaultTestKey()

	inner := &ethtypes.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(1000000000),
		GasFeeCap: big.NewInt(2000000000),
		Gas:       21000,
		To:        new(common.HexToAddress("0xto")),
		Value:     big.NewInt(1000),
		Data:      []byte("data"),
	}

	signer := ethtypes.NewLondonSigner(chainID)
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	header := &ethtypes.Header{Number: big.NewInt(testBlockNumber)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx := client.convertTransaction(tx, gethBlock, 0)

	assert.Equal(t, "2000000000", localTx.MaxFeePerGas)
	assert.Equal(t, "1000000000", localTx.MaxPriorityFeePerGas)
	assert.Equal(t, "1", localTx.ChainID)
}

func TestConvertTransaction_AccessList(t *testing.T) {
	t.Parallel()
	chainID := big.NewInt(1)
	key, _ := defaultTestKey()

	accessList := ethtypes.AccessList{
		{
			Address:     common.HexToAddress("0x1234567890123456789012345678901234567890"),
			StorageKeys: []common.Hash{common.HexToHash("0xkey1"), common.HexToHash("0xkey2")},
		},
	}

	inner := &ethtypes.AccessListTx{
		ChainID:    chainID,
		Nonce:      1,
		GasPrice:   big.NewInt(20000000000),
		Gas:        21000,
		To:         new(common.HexToAddress("0xto")),
		Value:      big.NewInt(1000),
		Data:       []byte("data"),
		AccessList: accessList,
	}

	signer := ethtypes.NewEIP2930Signer(chainID)
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	header := &ethtypes.Header{Number: big.NewInt(testBlockNumber)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx := client.convertTransaction(tx, gethBlock, 0)

	assert.Len(t, localTx.AccessList, 1)
	assert.Len(t, localTx.AccessList[0].StorageKeys, 2)
}

// --- convertGethReceipt ---

func TestConvertGethReceipt_Nil(t *testing.T) {
	t.Parallel()
	client := &EthereumClient{}
	result := client.convertGethReceipt(nil)
	assert.Nil(t, result)
}

func TestConvertGethReceipt_Success(t *testing.T) {
	t.Parallel()
	receipt := &ethtypes.Receipt{
		Status:            ethtypes.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		TxHash:            common.HexToHash("0xtxhash"),
		ContractAddress:   common.Address{},
		GasUsed:           21000,
		BlockHash:         common.HexToHash("0xblockhash"),
		BlockNumber:       big.NewInt(testBlockNumber),
		TransactionIndex:  0,
		Logs:              []*ethtypes.Log{},
	}

	client := &EthereumClient{}
	result := client.convertGethReceipt(receipt)

	require.NotNil(t, result)
	assert.Equal(t, "1", result.Status)
	assert.Equal(t, "", result.ContractAddress) // empty for non-contract-creation
	assert.Equal(t, "21000", result.GasUsed)
}

func TestConvertGethReceipt_FailedStatus(t *testing.T) {
	t.Parallel()
	receipt := &ethtypes.Receipt{
		Status:      ethtypes.ReceiptStatusFailed,
		TxHash:      common.HexToHash("0xtxhash"),
		BlockNumber: big.NewInt(testBlockNumber),
		Logs:        []*ethtypes.Log{},
	}

	client := &EthereumClient{}
	result := client.convertGethReceipt(receipt)

	require.NotNil(t, result)
	assert.Equal(t, "0", result.Status)
}

func TestConvertGethReceipt_ContractCreation(t *testing.T) {
	t.Parallel()
	contractAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	receipt := &ethtypes.Receipt{
		Status:          ethtypes.ReceiptStatusSuccessful,
		ContractAddress: contractAddr,
		TxHash:          common.HexToHash("0xtxhash"),
		BlockNumber:     big.NewInt(testBlockNumber),
		Logs:            []*ethtypes.Log{},
	}

	client := &EthereumClient{}
	result := client.convertGethReceipt(receipt)

	require.NotNil(t, result)
	assert.Equal(t, contractAddr.Hex(), result.ContractAddress)
}

func TestConvertGethReceipt_WithLogs(t *testing.T) {
	t.Parallel()
	receipt := &ethtypes.Receipt{
		Status:      ethtypes.ReceiptStatusSuccessful,
		TxHash:      common.HexToHash("0xtxhash"),
		BlockNumber: big.NewInt(testBlockNumber),
		Logs: []*ethtypes.Log{
			{
				Address:     common.HexToAddress("0xcontract"),
				Topics:      []common.Hash{common.HexToHash("0xtopic1")},
				Data:        []byte("log data"),
				BlockNumber: 100,
				TxHash:      common.HexToHash("0xtxhash"),
				TxIndex:     0,
				BlockHash:   common.HexToHash("0xblockhash"),
				Index:       0,
				Removed:     false,
			},
		},
	}

	client := &EthereumClient{}
	result := client.convertGethReceipt(receipt)

	require.NotNil(t, result)
	assert.Len(t, result.Logs, 1)
	assert.Equal(t, common.HexToAddress("0xcontract").Hex(), result.Logs[0].Address)
}

// --- convertGethLog ---

func TestConvertGethLog(t *testing.T) {
	t.Parallel()
	log := &ethtypes.Log{
		Address:     common.HexToAddress("0xcontract"),
		Topics:      []common.Hash{common.HexToHash("0xtopic1"), common.HexToHash("0xtopic2")},
		Data:        []byte{0x01, 0x02, 0x03},
		BlockNumber: 100,
		TxHash:      common.HexToHash("0xtxhash"),
		TxIndex:     5,
		BlockHash:   common.HexToHash("0xblockhash"),
		Index:       3,
		Removed:     true,
	}

	client := &EthereumClient{}
	result := client.convertGethLog(log)

	assert.Equal(t, common.HexToAddress("0xcontract").Hex(), result.Address)
	assert.Len(t, result.Topics, 2)
	assert.Equal(t, strconv.Itoa(testBlockNumber), result.BlockNumber)
	assert.Equal(t, 5, result.TransactionIndex)
	assert.Equal(t, 3, result.LogIndex)
	assert.True(t, result.Removed)
}

// --- helper functions ---

func TestGetToAddress(t *testing.T) {
	t.Parallel()
	to := common.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := ethtypes.NewTransaction(1, to, big.NewInt(1000), 21000, big.NewInt(20000000000), []byte("data"))
	assert.Equal(t, to.Hex(), getToAddress(tx))

	contractTx := ethtypes.NewContractCreation(1, big.NewInt(0), 21000, big.NewInt(20000000000), []byte("code"))
	assert.Equal(t, "", getToAddress(contractTx))
}

func TestGetBaseFeePerGas(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		baseFee *big.Int
		want    string
	}{
		{name: "Nil", baseFee: nil, want: ""},
		{name: "Set", baseFee: big.NewInt(1000), want: "1000"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			header := &ethtypes.Header{Number: big.NewInt(testBlockNumber), BaseFee: tc.baseFee}
			block := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))
			assert.Equal(t, tc.want, getBaseFeePerGas(block))
		})
	}
}

func TestGetMaxFeePerGas(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tx   *ethtypes.Transaction
		want string
	}{
		{
			name: "LegacyTx",
			tx:   ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil),
			want: "",
		},
		{
			name: "DynamicFeeTx",
			tx: ethtypes.NewTx(&ethtypes.DynamicFeeTx{
				ChainID:   big.NewInt(1),
				GasFeeCap: big.NewInt(2000000000),
				GasTipCap: big.NewInt(1000000000),
				Gas:       21000,
			}),
			want: "2000000000",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, getMaxFeePerGas(tc.tx))
		})
	}
}

func TestGetMaxPriorityFeePerGas(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tx   *ethtypes.Transaction
		want string
	}{
		{
			name: "LegacyTx",
			tx:   ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil),
			want: "",
		},
		{
			name: "DynamicFeeTx",
			tx: ethtypes.NewTx(&ethtypes.DynamicFeeTx{
				ChainID:   big.NewInt(1),
				GasFeeCap: big.NewInt(2000000000),
				GasTipCap: big.NewInt(1000000000),
				Gas:       21000,
			}),
			want: "1000000000",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, getMaxPriorityFeePerGas(tc.tx))
		})
	}
}

func TestGetChainId(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		tx           *ethtypes.Transaction
		want         string
		wantNonEmpty bool
	}{
		{
			name: "LegacyTx",
			// Legacy transactions derive chain ID from signature; for unsigned legacy txs
			// ChainId() returns a derived value, not nil
			tx:           ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil),
			wantNonEmpty: true,
		},
		{
			name: "Set",
			tx: ethtypes.NewTx(&ethtypes.DynamicFeeTx{
				ChainID:   big.NewInt(137),
				GasFeeCap: big.NewInt(2000000000),
				GasTipCap: big.NewInt(1000000000),
				Gas:       21000,
			}),
			want: "137",
		},
		{
			name: "NilChainID",
			// BlobTx with a nil ChainID field to test the nil check in getChainID.
			// This is an edge case that shouldn't happen in practice, but the code guards against it.
			// An unsigned BlobTx with ChainID left nil will still return non-nil from tx.ChainId()
			// because go-ethereum returns new(big.Int) for nil. We test the normal path here
			// with a zero-value chainId to ensure at least the zero-value string is returned.
			tx: ethtypes.NewTx(&ethtypes.BlobTx{
				ChainID:    uint256.NewInt(0),
				Nonce:      0,
				GasTipCap:  uint256.NewInt(1000000000),
				GasFeeCap:  uint256.NewInt(2000000000),
				Gas:        21000,
				To:         common.HexToAddress("0xto"),
				Value:      uint256.NewInt(0),
				BlobFeeCap: uint256.NewInt(100),
				BlobHashes: []common.Hash{common.HexToHash("0x01")},
			}),
			// ChainId() returns big.Int(0) which is "0"
			want: "0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := getChainID(tc.tx)
			if tc.wantNonEmpty {
				// Just verify it returns a non-empty string (the exact value depends on go-ethereum internals)
				assert.NotEmpty(t, result)
			} else {
				assert.Equal(t, tc.want, result)
			}
		})
	}
}

func TestGetContractAddress(t *testing.T) {
	t.Parallel()
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	cases := []struct {
		name    string
		receipt *ethtypes.Receipt
		want    string
	}{
		{name: "Empty", receipt: &ethtypes.Receipt{ContractAddress: common.Address{}}, want: ""},
		{name: "Set", receipt: &ethtypes.Receipt{ContractAddress: addr}, want: addr.Hex()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, getContractAddress(tc.receipt))
		})
	}
}

func TestGetReceiptStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		receipt *ethtypes.Receipt
		want    string
	}{
		{name: "Success", receipt: &ethtypes.Receipt{Status: ethtypes.ReceiptStatusSuccessful}, want: "1"},
		{name: "Failed", receipt: &ethtypes.Receipt{Status: ethtypes.ReceiptStatusFailed}, want: "0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, getReceiptStatus(tc.receipt))
		})
	}
}

// --- convertGethBlock with failed transaction conversion ---

func TestConvertGethBlock_WithUncles(t *testing.T) {
	t.Parallel()
	parentHeader := &ethtypes.Header{Number: big.NewInt(99)}
	uncleHeader := &ethtypes.Header{Number: big.NewInt(98)}

	header := &ethtypes.Header{
		Number:   big.NewInt(testBlockNumber),
		GasLimit: 8000000,
	}

	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{
		Uncles: []*ethtypes.Header{parentHeader, uncleHeader},
	}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)

	require.NotNil(t, localBlock)
	assert.Len(t, localBlock.Uncles, 2)
}

// --- convertTransaction with signed legacy (exercises fromAddr != nil path) ---

func TestConvertTransaction_SignedLegacy(t *testing.T) {
	t.Parallel()
	chainID := big.NewInt(1)
	key, expectedAddr := defaultTestKey()

	inner := &ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       new(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}

	signer := ethtypes.NewEIP155Signer(chainID)
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	header := &ethtypes.Header{Number: big.NewInt(testBlockNumber)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx := client.convertTransaction(tx, gethBlock, 0)
	assert.Equal(t, expectedAddr.Hex(), localTx.From)
	assert.Equal(t, "0", localTx.Type)
}

// --- convertGethBlock with failed tx conversion (warn+continue path) ---

func TestConvertGethBlock_FailedTxConversion(t *testing.T) {
	t.Parallel()
	header := &ethtypes.Header{
		Number:   big.NewInt(testBlockNumber),
		GasLimit: 8000000,
	}

	// Create a block with an unsigned legacy tx (GetFromAddress will fail)
	// and a signed tx (GetFromAddress will succeed)
	key, _ := defaultTestKey()
	signedInner := &ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       new(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}
	signer := ethtypes.NewEIP155Signer(big.NewInt(1))
	signedTx, err := ethtypes.SignNewTx(key, signer, signedInner)
	require.NoError(t, err)

	// Unsigned tx - GetFromAddress will warn but still produce "0x0..." fallback
	unsignedTx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil)

	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{
		Transactions: []*ethtypes.Transaction{unsignedTx, signedTx},
	}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)

	require.NotNil(t, localBlock)
	// Both transactions should be converted (unsigned gets zero address fallback)
	assert.Len(t, localBlock.Transactions, 2)
}

// --- convertTransaction fromAddr == nil path ---

func TestConvertTransaction_NilFromAddr(t *testing.T) {
	t.Parallel()
	// A transaction where GetFromAddress returns nil, nil is not normally possible
	// with go-ethereum types, but the code handles it. We test via an unsigned
	// legacy tx which takes the error path with zero address fallback.
	tx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil)
	header := &ethtypes.Header{Number: big.NewInt(testBlockNumber)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx := client.convertTransaction(tx, gethBlock, 0)

	// Unsigned tx falls through to either zero address from error path or homestead recovery
	assert.NotEmpty(t, localTx.From)
}

// --- convertTransaction with BlobTx (default switch case) ---

func TestConvertTransaction_BlobTx(t *testing.T) {
	t.Parallel()
	// BlobTx has type 3 which exercises the default case in the gasPrice switch
	chainID := big.NewInt(1)
	key, _ := defaultTestKey()

	to := common.HexToAddress("0x1234567890123456789012345678901234567890")
	inner := &ethtypes.BlobTx{
		ChainID:    uint256.NewInt(uint64(chainID.Int64())), //nolint:gosec
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1000000000),
		GasFeeCap:  uint256.NewInt(2000000000),
		Gas:        21000,
		To:         to,
		Value:      uint256.NewInt(1000),
		Data:       []byte("data"),
		BlobFeeCap: uint256.NewInt(100),
		BlobHashes: []common.Hash{common.HexToHash("0x01")},
	}

	signer := ethtypes.NewCancunSigner(chainID)
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	header := &ethtypes.Header{Number: big.NewInt(testBlockNumber)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx := client.convertTransaction(tx, gethBlock, 0)
	assert.Equal(t, fmt.Sprintf("%d", ethtypes.BlobTxType), localTx.Type)
	// BlobTx.GasPrice() returns GasFeeCap, same as default case
	assert.NotEmpty(t, localTx.GasPrice)
}

// --- convertGethBlock with a block containing a BlobTx ---

func TestConvertGethBlock_WithBlobTx(t *testing.T) {
	t.Parallel()
	chainID := big.NewInt(1)
	key, _ := defaultTestKey()

	to := common.HexToAddress("0x1234567890123456789012345678901234567890")
	inner := &ethtypes.BlobTx{
		ChainID:    uint256.NewInt(uint64(chainID.Int64())), //nolint:gosec
		Nonce:      0,
		GasTipCap:  uint256.NewInt(1000000000),
		GasFeeCap:  uint256.NewInt(2000000000),
		Gas:        21000,
		To:         to,
		Value:      uint256.NewInt(1000),
		Data:       []byte("blob data"),
		BlobFeeCap: uint256.NewInt(100),
		BlobHashes: []common.Hash{common.HexToHash("0x01")},
	}

	signer := ethtypes.NewCancunSigner(chainID)
	blobTx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	header := &ethtypes.Header{
		Number:   big.NewInt(200),
		GasLimit: 8000000,
		BaseFee:  big.NewInt(1000000000),
	}

	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{
		Transactions: []*ethtypes.Transaction{blobTx},
	}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)
	require.NotNil(t, localBlock)
	assert.Len(t, localBlock.Transactions, 1)
	assert.Equal(t, fmt.Sprintf("%d", ethtypes.BlobTxType), localBlock.Transactions[0].Type)
}

// --- convertTransaction where GetFromAddress errors (zero address fallback) ---

func TestConvertTransaction_FromAddrError_ZeroAddressFallback(t *testing.T) {
	t.Parallel()
	// Unsigned legacy tx triggers GetFromAddress error, which falls back to zero address
	inner := &ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       new(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}
	tx := ethtypes.NewTx(inner)

	header := &ethtypes.Header{Number: big.NewInt(testBlockNumber)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx := client.convertTransaction(tx, gethBlock, 0)
	// The error path sets fromAddr to zero address
	assert.Equal(t, ZeroAddress, localTx.From)
}
