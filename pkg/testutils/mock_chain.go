package testutils

import (
	"context"
	"sync"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// BlockRange records a (from, to) block range passed to GetDocIDsByBlockRange.
type BlockRange struct {
	From int64
	To   int64
}

// MockChain implements the chain.Chain interface with configurable per-method
// functions and call recording. It intentionally does NOT import pkg/chain so
// that it can be used by the tests of packages that pkg/chain depends on
// (e.g. pkg/defra) without introducing an import cycle. Go's structural typing
// means *MockChain satisfies chain.Chain wherever it is assigned to that
// interface, as long as the method set below stays in sync with chain.Chain.
//
// Each method dispatches to an optional Fn field; if a Fn is nil the method
// returns its zero value. Call counters and argument slices are updated under a
// mutex so the mock is safe for concurrent use.
type MockChain struct {
	mu sync.Mutex

	// Optional overrides. When nil the method returns its zero value.
	FetchAndStoreBlockFn          func(ctx context.Context, height int64) (string, error)
	FetchHighestBlockNumberFn     func(ctx context.Context) (int64, error)
	GetHighestStoredBlockNumberFn func(ctx context.Context) (int64, error)
	GetLowestStoredBlockNumberFn  func(ctx context.Context) (int64, error)
	GetDocIDsByBlockRangeFn       func(ctx context.Context, from, to int64) (map[string][]string, error)
	GetSchemaFn                   func() (string, error)
	GetCollectionsFn              func() []string
	CollectionsFn                 func() chains.Collections

	// Call recording.
	FetchAndStoreBlockCalls          []int64
	FetchHighestBlockNumberCalls     int
	GetHighestStoredBlockNumberCalls int
	GetLowestStoredBlockNumberCalls  int
	GetDocIDsByBlockRangeCalls       []BlockRange
	GetSchemaCalls                   int
	GetCollectionsCalls              int
	CollectionsCalls                 int
}

// FetchAndStoreBlock records the height and delegates to FetchAndStoreBlockFn.
func (m *MockChain) FetchAndStoreBlock(ctx context.Context, height int64) (string, error) {
	m.mu.Lock()
	m.FetchAndStoreBlockCalls = append(m.FetchAndStoreBlockCalls, height)
	m.mu.Unlock()
	if m.FetchAndStoreBlockFn != nil {
		return m.FetchAndStoreBlockFn(ctx, height)
	}
	return "", nil
}

// FetchHighestBlockNumber records the call and delegates to FetchHighestBlockNumberFn.
func (m *MockChain) FetchHighestBlockNumber(ctx context.Context) (int64, error) {
	m.mu.Lock()
	m.FetchHighestBlockNumberCalls++
	m.mu.Unlock()
	if m.FetchHighestBlockNumberFn != nil {
		return m.FetchHighestBlockNumberFn(ctx)
	}
	return 0, nil
}

// GetHighestStoredBlockNumber records the call and delegates to its Fn.
func (m *MockChain) GetHighestStoredBlockNumber(ctx context.Context) (int64, error) {
	m.mu.Lock()
	m.GetHighestStoredBlockNumberCalls++
	m.mu.Unlock()
	if m.GetHighestStoredBlockNumberFn != nil {
		return m.GetHighestStoredBlockNumberFn(ctx)
	}
	return 0, nil
}

// GetLowestStoredBlockNumber records the call and delegates to its Fn.
func (m *MockChain) GetLowestStoredBlockNumber(ctx context.Context) (int64, error) {
	m.mu.Lock()
	m.GetLowestStoredBlockNumberCalls++
	m.mu.Unlock()
	if m.GetLowestStoredBlockNumberFn != nil {
		return m.GetLowestStoredBlockNumberFn(ctx)
	}
	return 0, nil
}

// GetDocIDsByBlockRange records the range and delegates to its Fn.
func (m *MockChain) GetDocIDsByBlockRange(ctx context.Context, from, to int64) (map[string][]string, error) {
	m.mu.Lock()
	m.GetDocIDsByBlockRangeCalls = append(m.GetDocIDsByBlockRangeCalls, BlockRange{From: from, To: to})
	m.mu.Unlock()
	if m.GetDocIDsByBlockRangeFn != nil {
		return m.GetDocIDsByBlockRangeFn(ctx, from, to)
	}
	return make(map[string][]string), nil
}

// GetSchema records the call and delegates to GetSchemaFn.
func (m *MockChain) GetSchema() (string, error) {
	m.mu.Lock()
	m.GetSchemaCalls++
	m.mu.Unlock()
	if m.GetSchemaFn != nil {
		return m.GetSchemaFn()
	}
	return "", nil
}

// GetCollections records the call and delegates to GetCollectionsFn.
func (m *MockChain) GetCollections() []string {
	m.mu.Lock()
	m.GetCollectionsCalls++
	m.mu.Unlock()
	if m.GetCollectionsFn != nil {
		return m.GetCollectionsFn()
	}
	return nil
}

// Collections records the call and delegates to CollectionsFn.
// Returns chains.NewStubCollections("Ethereum__Mainnet") by default.
func (m *MockChain) Collections() chains.Collections {
	m.mu.Lock()
	m.CollectionsCalls++
	m.mu.Unlock()
	if m.CollectionsFn != nil {
		return m.CollectionsFn()
	}
	return chains.NewStubCollections("Ethereum__Mainnet")
}
