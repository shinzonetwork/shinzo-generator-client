package evm

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	codecevm "github.com/shinzonetwork/shinzo-evidence-codec/evm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/shinzonetwork/shinzo-generator-client/config"
)

// A block without transactions has no receipts to tie its hash to the node, so in tokens mode the
// node must know the block by that hash. Full mode does not ask.
func TestFetcher_FetchBlock_NoTransactionsUnknownHash(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		tokensMode bool
	}{
		{"tokens mode", true},
		{"full mode", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			blockNum := int64(100)
			block := fakeBlock(blockNum)
			var asked string
			client := &fakeRPCClient{
				block:          block,
				receiptsByHash: map[string][]*TransactionReceipt{block.Hash: {}},
				headerByHashFn: func(_ context.Context, hash string) (*ethtypes.Header, error) {
					asked = hash
					return nil, ethereum.NotFound
				},
			}
			f := NewFetcher(client, 8)
			f.tokensMode = tc.tokensMode

			_, err := f.FetchBlock(context.Background(), blockNum)
			if !tc.tokensMode {
				require.NoError(t, err)
				assert.Empty(t, asked)
				return
			}
			require.ErrorIs(t, err, ethereum.NotFound)
			assert.Equal(t, block.Hash, asked)
		})
	}
}

// supplyProof is a node's eth_getProof answer for USD1's supply slot on Ethereum at block
// 26307825, with that block's hash and state root and the totalSupply eth_call returned there.
type supplyProof struct {
	BlockHash   string                `json:"blockHash"`
	StateRoot   common.Hash           `json:"stateRoot"`
	Contract    common.Address        `json:"contract"`
	TotalSupply string                `json:"ethCallTotalSupply"`
	Proof       codecevm.AccountProof `json:"proof"`
}

func loadSupplyProof(t *testing.T) supplyProof {
	t.Helper()
	raw, err := os.ReadFile("testdata/ethereum_usd1_supply_proof.json")
	require.NoError(t, err)
	var p supplyProof
	require.NoError(t, json.Unmarshal(raw, &p))
	return p
}

// supplyFetcher returns a tokens-mode fetcher listing the fixture's token with supply slot 0x35,
// capturing at every block, whose node answers eth_getProof with proofFn.
func supplyFetcher(t *testing.T, p supplyProof, proofFn func(context.Context, common.Address, []string, string) (codecevm.AccountProof, error)) *Fetcher {
	t.Helper()
	f, err := NewFetcherFromConfig(&config.Config{Chain: config.ChainConfig{
		Mode:                 config.ChainModeTokens,
		Tokens:               []config.Token{{Address: p.Contract.Hex(), SupplySlot: "0x35"}},
		StateCaptureInterval: 1,
	}})
	require.NoError(t, err)
	f.client = &fakeRPCClient{proofFn: proofFn}
	return f
}

// countSupplyMetrics points f's supply counters at a test reader and returns a function that reads
// them: proof attempts by result, and missed captures under "missed".
func countSupplyMetrics(t *testing.T, f *Fetcher) func() map[string]int64 {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
	var err error
	f.supplyProofs, err = meter.Int64Counter("proofs")
	require.NoError(t, err)
	f.suppliesMissed, err = meter.Int64Counter("missed")
	require.NoError(t, err)
	return func() map[string]int64 {
		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(t.Context(), &rm))
		counts := map[string]int64{}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				for _, dp := range sum.DataPoints {
					key := m.Name
					if result, ok := dp.Attributes.Value("result"); ok {
						key = result.AsString()
					}
					counts[key] += dp.Value
				}
			}
		}
		return counts
	}
}

// At a capture height each listed token's supply proof is asked for by block hash and checked
// against the header's state root. A token that fails captureAttempts times is left out of the
// block. Each attempt is counted by result, and a token left out as missed.
func TestFetcher_CaptureSupply(t *testing.T) {
	t.Parallel()
	p := loadSupplyProof(t)
	supply, ok := new(big.Int).SetString(strings.TrimPrefix(p.TotalSupply, "0x"), 16)
	require.True(t, ok)
	tests := []struct {
		name       string
		stateRoot  common.Hash
		failFirst  int
		wantCalls  int
		wantValue  *big.Int
		wantCounts map[string]int64
	}{
		{
			name: "proof verifies", stateRoot: p.StateRoot, wantCalls: 1, wantValue: supply,
			wantCounts: map[string]int64{"verified": 1},
		},
		{
			name: "second attempt verifies", stateRoot: p.StateRoot, failFirst: 1, wantCalls: 2, wantValue: supply,
			wantCounts: map[string]int64{"rpc_error": 1, "verified": 1},
		},
		{
			name: "every attempt fails", stateRoot: p.StateRoot, failFirst: captureAttempts, wantCalls: captureAttempts,
			wantCounts: map[string]int64{"rpc_error": captureAttempts, "missed": 1},
		},
		{
			name: "proof of another state root", stateRoot: common.Hash{1}, wantCalls: captureAttempts,
			wantCounts: map[string]int64{"invalid_proof": captureAttempts, "missed": 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			f := supplyFetcher(t, p, func(_ context.Context, account common.Address, slots []string, blockHash string) (codecevm.AccountProof, error) {
				calls++
				assert.Equal(t, p.Contract, account)
				assert.Equal(t, []string{"0x35"}, slots)
				assert.Equal(t, p.BlockHash, blockHash)
				if calls <= tt.failFirst {
					return codecevm.AccountProof{}, errors.New("node unavailable")
				}
				return p.Proof, nil
			})
			counts := countSupplyMetrics(t, f)
			block := &Block{Hash: p.BlockHash, Header: &ethtypes.Header{Root: tt.stateRoot}}

			captures, err := f.captureSupply(context.Background(), 26307825, block)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCalls, calls)
			assert.Equal(t, tt.wantCounts, counts())
			if tt.wantValue == nil {
				assert.Empty(t, captures)
				return
			}
			require.Len(t, captures, 1)
			assert.Equal(t, strings.ToLower(strings.TrimPrefix(p.Contract.Hex(), "0x")), captures[0].Asset)
			assert.Equal(t, 0, tt.wantValue.Cmp(captures[0].Value))
			value, err := codecevm.VerifyStateCapture(p.StateRoot, p.Contract, common.BigToHash(big.NewInt(0x35)), captures[0].Proof)
			require.NoError(t, err)
			assert.Equal(t, 0, tt.wantValue.Cmp(value), "the stored proof verifies on its own")
		})
	}
}

// A capture cut short by a stopping generator fails the fetch, so FetchBlock does not return the
// block without a capture it could have made. The interrupted attempt is not counted.
func TestFetcher_CaptureSupplyStops(t *testing.T) {
	t.Parallel()
	p := loadSupplyProof(t)
	ctx, cancel := context.WithCancel(context.Background())
	f := supplyFetcher(t, p, func(context.Context, common.Address, []string, string) (codecevm.AccountProof, error) {
		cancel()
		return codecevm.AccountProof{}, context.Canceled
	})
	counts := countSupplyMetrics(t, f)
	_, err := f.captureSupply(ctx, 26307825, &Block{Hash: p.BlockHash, Header: &ethtypes.Header{Root: p.StateRoot}})
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, counts())
}

// FetchBlock captures supply only at multiples of the capture interval.
func TestFetcher_FetchBlock_CapturesAtInterval(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		height    int64
		wantCalls int
	}{
		{name: "multiple of the interval", height: 6, wantCalls: captureAttempts},
		{name: "between captures", height: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, err := NewFetcherFromConfig(&config.Config{Chain: config.ChainConfig{
				Mode:                 config.ChainModeTokens,
				Tokens:               []config.Token{{Address: "0x8d0d000ee44948fc98c9b98a4fa4921476f08b0d", SupplySlot: "0x35"}},
				StateCaptureInterval: 3,
			}})
			require.NoError(t, err)
			block := gethBundle(t, tt.height).Block
			calls := 0
			f.client = &fakeRPCClient{
				block:        block,
				headerByHash: block.Header,
				proofFn: func(context.Context, common.Address, []string, string) (codecevm.AccountProof, error) {
					calls++
					return codecevm.AccountProof{}, errors.New("node unavailable")
				},
			}

			_, err = f.FetchBlock(context.Background(), tt.height)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

// In tokens mode a block whose transactions or receipts do not rebuild the header's roots fails
// the fetch, so the height is retried. Full mode does not check.
func TestFetcher_FetchBlock_TokensModeChecksRoots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mode    string
		alter   func(block *Block, receipts []*TransactionReceipt) []*TransactionReceipt
		wantErr bool
	}{
		{name: "matching answers", mode: config.ChainModeTokens},
		{
			name: "receipt altered", mode: config.ChainModeTokens, wantErr: true,
			alter: func(_ *Block, receipts []*TransactionReceipt) []*TransactionReceipt {
				receipts[1].GethReceipt.Status = ethtypes.ReceiptStatusFailed
				return receipts
			},
		},
		{
			name: "transaction missing", mode: config.ChainModeTokens, wantErr: true,
			alter: func(block *Block, receipts []*TransactionReceipt) []*TransactionReceipt {
				block.GethTransactions = block.GethTransactions[:1]
				return receipts
			},
		},
		{
			name: "full mode, receipt altered", mode: config.ChainModeFull,
			alter: func(_ *Block, receipts []*TransactionReceipt) []*TransactionReceipt {
				receipts[1].GethReceipt.Status = ethtypes.ReceiptStatusFailed
				return receipts
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bundle := gethBundle(t, 12345, nil, nil)
			block, receipts := bundle.Block, bundle.Receipts
			if tt.alter != nil {
				receipts = tt.alter(block, receipts)
			}
			f, err := NewFetcherFromConfig(&config.Config{Chain: config.ChainConfig{Mode: tt.mode}})
			require.NoError(t, err)
			f.client = &fakeRPCClient{block: block, receiptsByHash: map[string][]*TransactionReceipt{block.Hash: receipts}}

			_, err = f.FetchBlock(context.Background(), 12345)
			if tt.wantErr {
				require.ErrorIs(t, err, errRootMismatch)
				return
			}
			require.NoError(t, err)
		})
	}
}

// In tokens mode the fetcher reports only the finalized tag; otherwise it takes the client's
// finalized-or-safe height.
func TestFetcher_FetchFinalizedBlockNumber_TokensModeHasNoFallback(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{config.ChainModeFull, config.ChainModeTokens} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Chain: config.ChainConfig{Mode: mode}}
			f, err := NewFetcherFromConfig(cfg)
			require.NoError(t, err)
			f.client = &fakeRPCClient{finalizedNum: 90, finalizedHeightErr: errors.New("finalized tag not served")}

			n, err := f.FetchFinalizedBlockNumber(context.Background())
			if mode == config.ChainModeTokens {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, int64(90), n)
		})
	}
}
