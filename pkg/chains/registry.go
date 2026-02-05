package chains

import (
	"fmt"
	"sync"
)

// ClientFactory is a function that creates a BlockchainClient from configuration
type ClientFactory func(config ChainConfig) (BlockchainClient, error)

// DocumentBuilderFactory is a function that creates a ChainDocumentBuilder
type DocumentBuilderFactory func(chain ChainType, network NetworkType) ChainDocumentBuilder

// Registry holds registered chain implementations
type Registry struct {
	mu                      sync.RWMutex
	clientFactories         map[ChainType]ClientFactory
	documentBuilderFactories map[ChainType]DocumentBuilderFactory
}

// Global registry instance
var globalRegistry = &Registry{
	clientFactories:         make(map[ChainType]ClientFactory),
	documentBuilderFactories: make(map[ChainType]DocumentBuilderFactory),
}

// GetRegistry returns the global registry instance
func GetRegistry() *Registry {
	return globalRegistry
}

// RegisterClientFactory registers a client factory for a chain type
func (r *Registry) RegisterClientFactory(chainType ChainType, factory ClientFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clientFactories[chainType] = factory
}

// RegisterDocumentBuilderFactory registers a document builder factory for a chain type
func (r *Registry) RegisterDocumentBuilderFactory(chainType ChainType, factory DocumentBuilderFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.documentBuilderFactories[chainType] = factory
}

// CreateClient creates a BlockchainClient for the given configuration
func (r *Registry) CreateClient(config ChainConfig) (BlockchainClient, error) {
	r.mu.RLock()
	factory, ok := r.clientFactories[config.Type]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no client factory registered for chain type: %s", config.Type)
	}

	return factory(config)
}

// CreateDocumentBuilder creates a ChainDocumentBuilder for the given chain and network
func (r *Registry) CreateDocumentBuilder(chainType ChainType, network NetworkType) (ChainDocumentBuilder, error) {
	r.mu.RLock()
	factory, ok := r.documentBuilderFactories[chainType]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no document builder factory registered for chain type: %s", chainType)
	}

	return factory(chainType, network), nil
}

// GetSupportedChains returns a list of registered chain types
func (r *Registry) GetSupportedChains() []ChainType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	chains := make([]ChainType, 0, len(r.clientFactories))
	for chainType := range r.clientFactories {
		chains = append(chains, chainType)
	}
	return chains
}

// IsChainSupported checks if a chain type is registered
func (r *Registry) IsChainSupported(chainType ChainType) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.clientFactories[chainType]
	return ok
}

// Package-level convenience functions that use the global registry

// RegisterClientFactory registers a client factory in the global registry
func RegisterClientFactory(chainType ChainType, factory ClientFactory) {
	globalRegistry.RegisterClientFactory(chainType, factory)
}

// RegisterDocumentBuilderFactory registers a document builder factory in the global registry
func RegisterDocumentBuilderFactory(chainType ChainType, factory DocumentBuilderFactory) {
	globalRegistry.RegisterDocumentBuilderFactory(chainType, factory)
}

// CreateClient creates a client using the global registry
func CreateClient(config ChainConfig) (BlockchainClient, error) {
	return globalRegistry.CreateClient(config)
}

// CreateDocumentBuilder creates a document builder using the global registry
func CreateDocumentBuilder(chainType ChainType, network NetworkType) (ChainDocumentBuilder, error) {
	return globalRegistry.CreateDocumentBuilder(chainType, network)
}

// GetSupportedChains returns supported chains from the global registry
func GetSupportedChains() []ChainType {
	return globalRegistry.GetSupportedChains()
}

// IsChainSupported checks if a chain is supported in the global registry
func IsChainSupported(chainType ChainType) bool {
	return globalRegistry.IsChainSupported(chainType)
}
