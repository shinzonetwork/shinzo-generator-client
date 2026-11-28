package bitcoin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	pkgerrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
)

// The fetcher tests sit on the rawClient seam: the fake is injected directly,
// and only the Connect tests drive the real client over the wire-level mock.

func TestNewFetcherFromConfig_MapsConfig(t *testing.T) {
	t.Parallel()

	_, err := NewFetcherFromConfig(nil)
	require.Error(t, err, "nil config is a constructor bug")
	assert.Contains(t, err.Error(), "config is nil")

	cfg := testFetcherConfig("http://127.0.0.1:8332")
	f, err := NewFetcherFromConfig(cfg)
	require.NoError(t, err)
	assert.Nil(t, f.client, "construction stays network-free; Connect dials")
	assert.Equal(t, int64(6), f.confirmationDepth)
	assert.Equal(t, cfg.Bitcoin, f.cfgBitcoin)
	assert.Equal(t, 10*time.Second, f.dialTimeout)
	assert.Equal(t, "Mainnet", f.network)
}

func TestNewFetcherFromConfig_NetworkDefaults(t *testing.T) {
	t.Parallel()

	cfg := testFetcherConfig("http://127.0.0.1:8332")
	cfg.Chain.Network = ""
	f, err := NewFetcherFromConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, "Mainnet", f.network, "an unset network falls back to the converter's default so the guard and the prefix agree")

	cfg.Chain.Network = "Signet"
	f, err = NewFetcherFromConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, "Signet", f.network, "a configured network passes through")
}

// TestNetworkIdentities pins the identity table Connect verifies against:
// Core's chain spelling and the genesis hash for each supported network.
// The expected values are literals, so an accidental table edit fails here
// before any node sees it.
func TestNetworkIdentities(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		[]string{"Mainnet", "Regtest", "Signet", "Testnet", "Testnet4"},
		SupportedBitcoinNetworks(), "the unsupported-network error lists the networks alphabetically")

	tests := []struct {
		network string
		chain   string
		genesis string
	}{
		{"Mainnet", "main", "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"},
		{"Testnet", "test", "000000000933ea01ad0ee984209779baaec3ced90fa3f408719526f8d77f4943"},
		{"Testnet4", "testnet4", "00000000da84f2bafbbc53dee25a72ae507ff4914b867c565be350b0da8bf043"},
		{"Signet", "signet", "00000008819873e925422c1ff0f99f7cc9bbb232af63a077a480a3633bee1ef6"},
		{"Regtest", "regtest", "0f9188f13cb7b2c71f2a335e3a4fc328bf5beb436012afca590b1a11466e2206"},
	}
	for _, tt := range tests {
		t.Run(tt.network, func(t *testing.T) {
			t.Parallel()
			identity, ok := networkIdentities[tt.network]
			require.True(t, ok, "each supported network must sit in the identity table")
			assert.Equal(t, tt.chain, identity.coreChain)
			assert.Equal(t, tt.genesis, identity.genesisHash)
		})
	}
}

func TestNewFetcherFromConfig_DepthDefaults(t *testing.T) {
	t.Parallel()

	cfg := testFetcherConfig("http://127.0.0.1:8332")
	cfg.Bitcoin.ConfirmationDepth = 0
	f, err := NewFetcherFromConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, int64(6), f.confirmationDepth, "unset depth falls back to the approved default")

	cfg.Bitcoin.ConfirmationDepth = -3
	f, err = NewFetcherFromConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, int64(6), f.confirmationDepth, "nonsense depth falls back to the approved default")

	cfg.Bitcoin.ConfirmationDepth = 42
	f, err = NewFetcherFromConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, int64(42), f.confirmationDepth, "a valid configured depth holds")
}

func TestFetcher_NotConnectedGuards(t *testing.T) {
	t.Parallel()
	f := &Fetcher{}
	ctx := context.Background()

	_, err := f.FetchBlock(ctx, 7)
	assert.ErrorContains(t, err, "Connect")
	_, err = f.FetchBlockHeaderByHash(ctx, testBlockHash)
	assert.ErrorContains(t, err, "Connect")
	_, err = f.FetchHighestBlockNumber(ctx)
	assert.ErrorContains(t, err, "Connect")
	_, err = f.FetchFinalizedBlockNumber(ctx)
	assert.ErrorContains(t, err, "Connect")
}

func TestFetchHighestBlockNumber(t *testing.T) {
	t.Parallel()

	fake := newFakeRPCClient()
	fake.blockCount = 184500
	f := NewFetcher(fake, 6)

	count, err := f.FetchHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(184500), count)
	assert.Equal(t, 1, fake.callCount("getblockcount"))

	fake.blockCountErr = &btcjson.RPCError{Code: btcjson.ErrRPCMisc, Message: "boom"}
	_, err = f.FetchHighestBlockNumber(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get block count")
}

func TestFetchFinalizedBlockNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tip   int64
		depth int64
		want  int64
	}{
		{name: "tip well past the depth", tip: 100, depth: 6, want: 94},
		{name: "tip exactly at the depth boundary", tip: 6, depth: 6, want: 0},
		{name: "tip inside the confirmation window clamps at genesis", tip: 3, depth: 6, want: 0},
		{name: "empty chain gives genesis", tip: 0, depth: 6, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeRPCClient()
			fake.blockCount = tt.tip
			f := NewFetcher(fake, tt.depth)

			floor, err := f.FetchFinalizedBlockNumber(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tt.want, floor)
			assert.Equal(t, 1, fake.callCount("getblockcount"), "one tip lookup, never retried")
		})
	}

	fake := newFakeRPCClient()
	fake.blockCountErr = &btcjson.RPCError{Code: btcjson.ErrRPCMisc, Message: "boom"}
	f := NewFetcher(fake, 6)
	_, err := f.FetchFinalizedBlockNumber(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get block count")
}

// TestFetchBlock_HappyPath pins the two round trips: the height resolves to
// the main-chain hash, the body is fetched by that hash, and the returned
// value is the client's own verbosity-3 decode passed through untouched.
func TestFetchBlock_HappyPath(t *testing.T) {
	t.Parallel()

	block7 := fakeBlock(7)
	fake := newFakeRPCClient()
	fake.hashByHeight[7] = block7.Hash
	fake.blockByHash[block7.Hash] = block7
	f := NewFetcher(fake, 6)

	raw, err := f.FetchBlock(context.Background(), 7)
	require.NoError(t, err)
	block, ok := raw.(*Block)
	require.True(t, ok, "the fetcher returns the verbosity-3 decode directly")
	assert.Equal(t, block7, block)
	assert.Equal(t, 1, fake.callCount("getblockhash"))
	assert.Equal(t, 1, fake.callCount("getblock verbosity 3"))
	assert.Equal(t, 0, fake.callCount("getblock verbosity 3 archive"), "the archive stays out of the happy path")
}

// TestFetchBlock_AboveTipBecomesNotFound pins the tip-wait contract: Core's
// -8 from the height lookup must reach the processor as a not-found error so
// the 3-second retry loop engages while the tip advances.
func TestFetchBlock_AboveTipBecomesNotFound(t *testing.T) {
	t.Parallel()

	fake := newFakeRPCClient()
	fake.hashErr = &btcjson.RPCError{
		Code:    btcjson.ErrRPCInvalidParameter,
		Message: "Block height out of range",
	}
	f := NewFetcher(fake, 6)

	_, err := f.FetchBlock(context.Background(), 184501)
	require.Error(t, err)
	assert.True(t, pkgerrors.IsErrNotFound(err), "the above-tip outcome must classify as not-found")
	assert.Contains(t, err.Error(), "not found")
	_, ok := errors.AsType[*btcjson.RPCError](err)
	assert.True(t, ok, "the underlying -8 stays reachable for classification")
	assert.Equal(t, 1, fake.callCount("getblockhash"), "not-found never retries in the fetcher")
	assert.Equal(t, 0, fake.callCount("getblock verbosity 3"))
}

func TestFetchBlock_UnknownHashKeepsNotFound(t *testing.T) {
	t.Parallel()

	block7 := fakeBlock(7)
	fake := newFakeRPCClient()
	fake.hashByHeight[7] = block7.Hash
	fake.blockErr = &btcjson.RPCError{
		Code:    btcjson.ErrRPCBlockNotFound,
		Message: "Block not found",
	}
	f := NewFetcher(fake, 6)

	_, err := f.FetchBlock(context.Background(), 7)
	require.Error(t, err)
	assert.True(t, pkgerrors.IsErrNotFound(err), "the unknown-hash outcome keeps its not-found text")
	assert.NotErrorIs(t, err, errBlockPruned, "the unknown-hash -5 never wraps into the pruned-block sentinel")
	assert.NotErrorIs(t, err, errArchiveNotConfigured)
	assert.Equal(t, 0, fake.callCount("getblock verbosity 3 archive"))
}

// TestFetchBlock_PrunedWithoutArchive pins the operator error: the pruned
// outcome names BITCOIN_ARCHIVE_RPC_URL and never classifies as not-found,
// which would send the processor into an infinite retry.
func TestFetchBlock_PrunedWithoutArchive(t *testing.T) {
	t.Parallel()

	fake := newFakeRPCClient()
	block := fakeBlock(7)
	fake.hashFn = func(_ context.Context, _ int64) (string, error) {
		return block.Hash, nil
	}
	fake.blockFn = func(_ context.Context, _ string) (*Block, error) {
		return nil, errBlockPruned
	}
	fake.archiveFn = func(_ context.Context, _ string) (*Block, error) {
		return nil, errArchiveNotConfigured
	}
	f := NewFetcher(fake, 6)

	_, err := f.FetchBlock(context.Background(), 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BITCOIN_ARCHIVE_RPC_URL")
	assert.False(t, pkgerrors.IsErrNotFound(err), "the pruned outcome must not read as not-found")
	assert.Equal(t, 1, fake.callCount("getblock verbosity 3"))
	assert.Equal(t, 1, fake.callCount("getblock verbosity 3 archive"))
}

func TestFetchBlock_PrunedRoutesToArchive(t *testing.T) {
	t.Parallel()

	primary := fakeBlock(7)
	archived := fakeBlock(6)
	fake := newFakeRPCClient()
	fake.hashByHeight[7] = primary.Hash
	fake.blockFn = func(_ context.Context, _ string) (*Block, error) {
		return nil, errBlockPruned
	}
	// The archive is keyed by the same content hash the primary endpoint was
	// asked for: both endpoints address the same block, they only differ in
	// which heights they still store.
	fake.archiveByHash[primary.Hash] = archived
	f := NewFetcher(fake, 6)

	raw, err := f.FetchBlock(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, archived, raw, "the archive answer is the bundle primary could not serve")
	assert.Equal(t, 1, fake.callCount("getblockhash"))
	assert.Equal(t, 1, fake.callCount("getblock verbosity 3"))
	assert.Equal(t, 1, fake.callCount("getblock verbosity 3 archive"))
}

func TestFetchBlock_ArchiveAlsoPruned(t *testing.T) {
	t.Parallel()

	fake := newFakeRPCClient()
	fake.hashFn = func(_ context.Context, _ int64) (string, error) {
		return fakeBitcoinHash("block-7"), nil
	}
	fake.blockFn = func(_ context.Context, _ string) (*Block, error) {
		return nil, errBlockPruned
	}
	fake.archiveFn = func(_ context.Context, _ string) (*Block, error) {
		return nil, errBlockPruned
	}
	f := NewFetcher(fake, 6)

	_, err := f.FetchBlock(context.Background(), 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "archive fallback")
	assert.False(t, pkgerrors.IsErrNotFound(err), "both endpoints pruned is an operator problem, not a missing block")
	assert.Equal(t, 1, fake.callCount("getblock verbosity 3"), "no retry loop in the fetcher")
	assert.Equal(t, 1, fake.callCount("getblock verbosity 3 archive"))
}

func TestFetchBlockHeaderByHash(t *testing.T) {
	t.Parallel()

	blocked := fakeBitcoinHash("block-7")
	parent := fakeBitcoinHash("block-6")
	fake := newFakeRPCClient()
	fake.headersByHash[blocked] = fakeHeader(7, parent)
	// A body fetch during the walk-back would be a cost and branch-safety
	// bug, so the fake counts every attempt.
	fake.blockFn = func(_ context.Context, _ string) (*Block, error) {
		return nil, &btcjson.RPCError{Code: btcjson.ErrRPCMisc, Message: "body fetch must never happen"}
	}
	f := NewFetcher(fake, 6)

	header, err := f.FetchBlockHeaderByHash(context.Background(), blocked)
	require.NoError(t, err)
	assert.Equal(t, chains.BlockHeader{
		Number:     int64(7),
		Hash:       blocked,
		ParentHash: parent,
	}, header, "the hash echoes the requested value and the parent hash bridges the walk-back")
	assert.Equal(t, 1, fake.callCount("getblockheader"))
	assert.Equal(t, 0, fake.callCount("getblock verbosity 3"), "the walk-back is header-only")
}

func TestFetchBlockHeaderByHash_NotFoundPassesThrough(t *testing.T) {
	t.Parallel()

	blocked := fakeBitcoinHash("block-unknown")
	fake := newFakeRPCClient()
	fake.headerErr = &btcjson.RPCError{
		Code:    btcjson.ErrRPCBlockNotFound,
		Message: "Block not found",
	}
	f := NewFetcher(fake, 6)

	_, err := f.FetchBlockHeaderByHash(context.Background(), blocked)
	require.Error(t, err)
	assert.True(t, pkgerrors.IsErrNotFound(err), "an unknown hash keeps Core's not-found text for the walk-back")
	assert.Equal(t, 1, fake.callCount("getblockheader"), "not-found never retries in the fetcher")
}

func TestFetchBlock_ContextPrechecks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		usage func(f *Fetcher, ctx context.Context) error
	}{
		{
			name: "FetchBlock",
			usage: func(f *Fetcher, ctx context.Context) error {
				_, err := f.FetchBlock(ctx, 7)
				return err
			},
		},
		{
			name: "FetchFinalizedBlockNumber",
			usage: func(f *Fetcher, ctx context.Context) error {
				_, err := f.FetchFinalizedBlockNumber(ctx)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeRPCClient()
			fake.hashByHeight[7] = fakeBitcoinHash("block-7")
			fake.blockByHash[fakeBitcoinHash("block-7")] = fakeBlock(7)
			f := NewFetcher(fake, 6)

			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			err := tt.usage(f, canceled)
			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			assert.Zero(t, len(fake.calls), "a caller-aborted fetch makes zero RPC attempts")
		})
	}
}

func TestConnect_PreConnectedNoOp(t *testing.T) {
	t.Parallel()

	fake := newFakeRPCClient()
	f := NewFetcher(fake, 6)
	require.NoError(t, f.Connect(context.Background()))
	assert.Equal(t, 0, fake.callCount("getblockcount"), "a pre-connected fetcher skips the probe")
}

// TestConnect_FromConfig drives the full config-built path over the wire
// mock: construction, the bounded health probe, the network identity check,
// and the one-time archive floor log.
func TestConnect_FromConfig(t *testing.T) {
	t.Parallel()
	chainInfo := map[string]any{
		"chain":         "main",
		"blocks":        800000,
		"headers":       800000,
		"bestblockhash": fakeBitcoinHash("chain-tip"),
		"pruned":        true,
		"pruneheight":   300000,
		"softforks": map[string]any{
			"segwit": map[string]any{"type": "buried", "height": 481824, "active": true},
		},
	}
	networkInfo := map[string]any{
		"version":         270000,
		"subversion":      "/Satoshi:27.0.0/",
		"protocolversion": 70016,
		"relayfee":        0.00001,
	}
	server := newCoreRPCServer(t, func(call recordedCall) mockResponse {
		switch call.Method {
		case "getblockcount":
			return mockBlockAt(t, json.RawMessage("184500"))
		case "getblockchaininfo":
			return mockBlockAt(t, chainInfo)
		case "getblockhash":
			var params []int64
			if json.Unmarshal(call.Params, &params) != nil || len(params) != 1 || params[0] != 0 {
				return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "unscripted height")
			}
			return mockBlockAt(t, networkIdentities["Mainnet"].genesisHash)
		case "getinfo":
			return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "Method not found")
		case "getnetworkinfo":
			return mockBlockAt(t, networkInfo)
		default:
			return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "unscripted method")
		}
	})

	f, err := NewFetcherFromConfig(testFetcherConfig(server.URL))
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, f.Connect(ctx))

	count, err := f.FetchHighestBlockNumber(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(184500), count)
	assert.GreaterOrEqual(t, server.callCount("getblockcount"), 1)
	assert.Equal(t, 1, server.callCount("getblockchaininfo"), "the archive floor is logged once, not per fetch")

	require.NoError(t, f.Close())
}

// TestConnect_ProbeHonorsDialTimeout pins the bounded probe: a dead
// endpoint must fail the fetcher start within the configured dial budget,
// well under the library's internal HTTP timeout.
func TestConnect_ProbeHonorsDialTimeout(t *testing.T) {
	t.Parallel()

	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		time.Sleep(3 * time.Second)
		return mockBlockAt(t, json.RawMessage("184500"))
	})
	cfg := testFetcherConfig(server.URL)
	cfg.Bitcoin.DialTimeoutSeconds = 1

	f, err := NewFetcherFromConfig(cfg)
	require.NoError(t, err)

	start := time.Now()
	err = f.Connect(context.Background())
	elapsed := time.Since(start)
	require.Error(t, err, "the sleeping node must fail the probe")
	assert.Contains(t, err.Error(), "timed out")
	assert.Less(t, elapsed, 5*time.Second, "the probe must not ride the library budget")
	_, highErr := f.FetchHighestBlockNumber(context.Background())
	assert.ErrorContains(t, highErr, "Connect", "a timed-out probe leaves the fetcher unconnected")
}

func TestConnect_HealthProbeFailure(t *testing.T) {
	t.Parallel()

	server := newCoreRPCServer(t, func(_ recordedCall) mockResponse {
		return serveErr(int(btcjson.ErrRPCMisc), "unreachable")
	})
	f, err := NewFetcherFromConfig(testFetcherConfig(server.URL))
	require.NoError(t, err)

	err = f.Connect(context.Background())
	require.Error(t, err, "an unhealthy node must abort startup")
	assert.Contains(t, err.Error(), "connect probe")
}

// TestConnect_ChecksChainNetwork pins the network identity guard: Connect
// reads getblockchaininfo and the height-0 block hash and refuses to
// connect unless both match the configured network, so a generator pointed
// at another chain's node never indexes that chain under its own collection
// names. A failed identity read also fails Connect, since an endpoint that
// cannot answer it cannot be pinned.
func TestConnect_ChecksChainNetwork(t *testing.T) {
	t.Parallel()

	mainnetGenesis := networkIdentities["Mainnet"].genesisHash
	signetGenesis := networkIdentities["Signet"].genesisHash

	tests := []struct {
		name          string
		network       string
		chainReported string
		genesisHash   string
		chainInfoErr  bool
		genesisErr    bool
		wantErr       error
		wantErrText   string
		wantNoRequest bool
		wantConnected bool
	}{
		{
			name:          "endpoint serves the configured network",
			network:       "Mainnet",
			chainReported: "main",
			genesisHash:   mainnetGenesis,
			wantConnected: true,
		},
		{
			name:          "endpoint serves another chain",
			network:       "Mainnet",
			chainReported: "signet",
			genesisHash:   mainnetGenesis,
			wantErr:       errNetworkMismatch,
			wantErrText:   "signet",
			wantConnected: false,
		},
		{
			name:          "genesis hash belongs to another network",
			network:       "Mainnet",
			chainReported: "main",
			genesisHash:   signetGenesis,
			wantErr:       errNetworkMismatch,
			wantErrText:   signetGenesis,
			wantConnected: false,
		},
		{
			name:          "chain info read fails while the probe succeeds",
			network:       "Mainnet",
			chainReported: "main",
			genesisHash:   mainnetGenesis,
			chainInfoErr:  true,
			wantErrText:   "read chain info",
			wantConnected: false,
		},
		{
			name:          "genesis read fails",
			network:       "Mainnet",
			chainReported: "main",
			genesisErr:    true,
			wantErrText:   "read genesis hash",
			wantConnected: false,
		},
		{
			name:          "unsupported network fails closed before any request",
			network:       "Devnet",
			wantErrText:   "Mainnet, Regtest, Signet, Testnet, Testnet4",
			wantNoRequest: true,
			wantConnected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := newCoreRPCServer(t, func(call recordedCall) mockResponse {
				switch call.Method {
				case "getblockcount":
					return mockBlockAt(t, json.RawMessage("184500"))
				case "getblockchaininfo":
					if tt.chainInfoErr {
						return serveErr(int(btcjson.ErrRPCMisc), "chain info unavailable")
					}
					return mockBlockAt(t, map[string]any{
						"chain":         tt.chainReported,
						"blocks":        800000,
						"headers":       800000,
						"bestblockhash": fakeBitcoinHash("chain-tip"),
						"pruned":        false,
						"pruneheight":   int64(0),
						"softforks": map[string]any{
							"segwit": map[string]any{"type": "buried", "height": 481824, "active": true},
						},
					})
				case "getblockhash":
					if tt.genesisErr {
						return serveErr(int(btcjson.ErrRPCMisc), "height 0 unavailable")
					}
					var params []int64
					if json.Unmarshal(call.Params, &params) != nil || len(params) != 1 || params[0] != 0 {
						return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "unscripted height")
					}
					return mockBlockAt(t, tt.genesisHash)
				case "getinfo":
					return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "Method not found")
				case "getnetworkinfo":
					return mockBlockAt(t, map[string]any{
						"version":         270000,
						"subversion":      "/Satoshi:27.0.0/",
						"protocolversion": 70016,
						"relayfee":        0.00001,
					})
				default:
					return serveErr(int(btcjson.ErrRPCMethodNotFound.Code), "unscripted method")
				}
			})
			cfg := testFetcherConfig(server.URL)
			cfg.Chain.Network = tt.network
			f, err := NewFetcherFromConfig(cfg)
			require.NoError(t, err)

			err = f.Connect(context.Background())

			if tt.wantNoRequest {
				assert.Zero(t, server.callCount("getblockcount"), "an unsupported network must fail closed before any request")
			}
			if tt.wantConnected {
				require.NoError(t, err)
				require.NotNil(t, f.client, "a passing identity check must retain the client")
			} else {
				require.Error(t, err)
				if tt.wantErr != nil {
					assert.ErrorIs(t, err, tt.wantErr)
				}
				if tt.wantErrText != "" {
					assert.Contains(t, err.Error(), tt.wantErrText)
				}
				assert.Nil(t, f.client, "a failed Connect must not retain the client, so a retry re-runs the check")
			}
		})
	}
}

func TestFetcher_Close(t *testing.T) {
	t.Parallel()

	fake := newFakeRPCClient()
	f := NewFetcher(fake, 6)
	require.NoError(t, f.Close())
	fake.mu.Lock()
	assert.True(t, fake.closed)
	fake.mu.Unlock()

	require.NoError(t, (&Fetcher{}).Close(), "a never-connected fetcher closes clean")
}
