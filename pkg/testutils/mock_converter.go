package testutils

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/sourcenetwork/defradb/node"
)

// ErrMockConvertFnNotSet is returned by MockConverter.Convert when no
// ConvertFn override is configured.
var ErrMockConvertFnNotSet = errors.New("mock: ConvertFn not set")

// BlockRange records a (from, to) block range passed to GetDocIDsByBlockRange.
type BlockRange struct {
	From int64
	To   int64
}

// MockConverter implements the chains.Converter interface with configurable
// per-method functions and call recording. It follows the same pattern as
// MockFetcher and MockChain: each method dispatches to an optional Fn field;
// if a Fn is nil the method returns its zero value. Call counters and
// argument slices are updated under a mutex so the mock is safe for
// concurrent use.
type MockConverter struct {
	mu sync.Mutex

	// Optional overrides. When nil the method returns its zero value.
	ConvertFn                     func(ctx context.Context, rawBlock any) (chains.ConversionResult, error)
	GetSchemaFn                   func() (string, error)
	GetCollectionsFn              func() []string
	CollectionsFn                 func() chains.Collections
	SignatureCollectionFn         func() string
	GetHighestStoredBlockNumberFn func(ctx context.Context, n *node.Node) (int64, error)
	GetLowestStoredBlockNumberFn  func(ctx context.Context, n *node.Node) (int64, error)
	GetDocIDsByBlockRangeFn       func(ctx context.Context, n *node.Node, from, to int64) (map[string][]string, error)

	// Call recording.
	ConvertCalls                     []any
	GetSchemaCalls                   int
	GetCollectionsCalls              int
	CollectionsCalls                 int
	SignatureCollectionCalls         int
	GetHighestStoredBlockNumberCalls int
	GetLowestStoredBlockNumberCalls  int
	GetDocIDsByBlockRangeCalls       []BlockRange
}

// Compile-time guarantee that MockConverter implements chains.Converter.
var _ chains.Converter = (*MockConverter)(nil)

// Convert records the rawBlock and delegates to ConvertFn.
func (m *MockConverter) Convert(ctx context.Context, rawBlock any) (chains.ConversionResult, error) {
	m.mu.Lock()
	m.ConvertCalls = append(m.ConvertCalls, rawBlock)
	m.mu.Unlock()
	if m.ConvertFn != nil {
		return m.ConvertFn(ctx, rawBlock)
	}
	return chains.ConversionResult{}, ErrMockConvertFnNotSet
}

// GetSchema records the call and delegates to GetSchemaFn.
func (m *MockConverter) GetSchema() (string, error) {
	m.mu.Lock()
	m.GetSchemaCalls++
	m.mu.Unlock()
	if m.GetSchemaFn != nil {
		return m.GetSchemaFn()
	}
	return "", nil
}

// GetCollections records the call and delegates to GetCollectionsFn.
func (m *MockConverter) GetCollections() []string {
	m.mu.Lock()
	m.GetCollectionsCalls++
	m.mu.Unlock()
	if m.GetCollectionsFn != nil {
		return m.GetCollectionsFn()
	}
	return nil
}

// Collections records the call and delegates to CollectionsFn.
func (m *MockConverter) Collections() chains.Collections {
	m.mu.Lock()
	m.CollectionsCalls++
	m.mu.Unlock()
	if m.CollectionsFn != nil {
		return m.CollectionsFn()
	}
	c, err := chains.NewCollections(nil)
	if err != nil {
		panic(fmt.Sprintf("MockConverter.Collections: chains.NewCollections failed: %v", err))
	}
	return c
}

// SignatureCollection records the call and delegates to SignatureCollectionFn.
func (m *MockConverter) SignatureCollection() string {
	m.mu.Lock()
	m.SignatureCollectionCalls++
	m.mu.Unlock()
	if m.SignatureCollectionFn != nil {
		return m.SignatureCollectionFn()
	}
	c, err := chains.NewCollections(nil)
	if err != nil {
		panic(fmt.Sprintf("MockConverter.SignatureCollection: chains.NewCollections failed: %v", err))
	}
	name, err := c.GetCollection(chains.TypeBlockSignature)
	if err != nil {
		panic(fmt.Sprintf("MockConverter.SignatureCollection: GetCollection failed: %v", err))
	}
	return name
}

// GetHighestStoredBlockNumber records the call and delegates to GetHighestStoredBlockNumberFn.
func (m *MockConverter) GetHighestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	m.mu.Lock()
	m.GetHighestStoredBlockNumberCalls++
	m.mu.Unlock()
	if m.GetHighestStoredBlockNumberFn != nil {
		return m.GetHighestStoredBlockNumberFn(ctx, n)
	}
	return 0, nil
}

// GetLowestStoredBlockNumber records the call and delegates to GetLowestStoredBlockNumberFn.
func (m *MockConverter) GetLowestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	m.mu.Lock()
	m.GetLowestStoredBlockNumberCalls++
	m.mu.Unlock()
	if m.GetLowestStoredBlockNumberFn != nil {
		return m.GetLowestStoredBlockNumberFn(ctx, n)
	}
	return 0, nil
}

// GetDocIDsByBlockRange records the range and delegates to GetDocIDsByBlockRangeFn.
func (m *MockConverter) GetDocIDsByBlockRange(ctx context.Context, n *node.Node, from, to int64) (map[string][]string, error) {
	m.mu.Lock()
	m.GetDocIDsByBlockRangeCalls = append(m.GetDocIDsByBlockRangeCalls, BlockRange{From: from, To: to})
	m.mu.Unlock()
	if m.GetDocIDsByBlockRangeFn != nil {
		return m.GetDocIDsByBlockRangeFn(ctx, n, from, to)
	}
	return make(map[string][]string), nil
}
