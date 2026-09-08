package testutils

import (
	"context"
	"errors"
	"sync"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// ErrMockFetchBlockFnNotSet is returned by MockFetcher.FetchBlock when no
// FetchBlockFn override is configured.
var ErrMockFetchBlockFnNotSet = errors.New("mock: FetchBlockFn not set")

// MockFetcher implements the chains.Fetcher interface with configurable
// per-method functions and call recording. It follows the same pattern as
// MockChain: each method dispatches to an optional Fn field; if a Fn is nil
// the method returns its zero value. Call counters and argument slices are
// updated under a mutex so the mock is safe for concurrent use.
type MockFetcher struct {
	mu sync.Mutex

	// Optional overrides. When nil the method returns its zero value.
	FetchBlockFn              func(ctx context.Context, height int64) (any, error)
	FetchHighestBlockNumberFn func(ctx context.Context) (int64, error)
	CloseFn                   func() error

	// Call recording.
	FetchBlockCalls              []int64
	FetchHighestBlockNumberCalls int
	CloseCalls                   int
}

// Compile-time guarantee that MockFetcher implements chains.Fetcher.
var _ chains.Fetcher = (*MockFetcher)(nil)

// FetchBlock records the height and delegates to FetchBlockFn.
func (m *MockFetcher) FetchBlock(ctx context.Context, height int64) (any, error) {
	m.mu.Lock()
	m.FetchBlockCalls = append(m.FetchBlockCalls, height)
	m.mu.Unlock()
	if m.FetchBlockFn != nil {
		return m.FetchBlockFn(ctx, height)
	}
	return nil, ErrMockFetchBlockFnNotSet
}

// FetchHighestBlockNumber records the call and delegates to FetchHighestBlockNumberFn.
func (m *MockFetcher) FetchHighestBlockNumber(ctx context.Context) (int64, error) {
	m.mu.Lock()
	m.FetchHighestBlockNumberCalls++
	m.mu.Unlock()
	if m.FetchHighestBlockNumberFn != nil {
		return m.FetchHighestBlockNumberFn(ctx)
	}
	return 0, nil
}

// Close records the call and delegates to CloseFn.
func (m *MockFetcher) Close() error {
	m.mu.Lock()
	m.CloseCalls++
	m.mu.Unlock()
	if m.CloseFn != nil {
		return m.CloseFn()
	}
	return nil
}
