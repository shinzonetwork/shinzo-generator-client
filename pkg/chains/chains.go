package chains

// Defines the chain-agnostic abstraction used by the indexer,
// pruner, snapshotter, and schema applier to interact with a specific chain
// backend (e.g. EVM-based chains) without depending on concrete RPC or DefraDB
// implementations.
//
// The Chain interface centralises every operation the rest of the system needs:
//   - fetching and persisting a block at a given height,
//   - querying the on-chain tip and the stored block-number bounds,
//   - listing the docIDs that belong to a block range (used by pruning and
//     snapshotting),
//   - and exposing the schema/collection names for the configured chain.
//
import (
	"context"
	"errors"
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/sourcenetwork/defradb/node"
)

// ErrUnknownCollection is returned by Collections.GetCollection when the given
// role string does not map to a known collection.
var ErrUnknownCollection = errors.New("unknown collection")

// Collection type constants used as arguments to GetCollection.
const (
	TypeBlock             = "block"
	TypeBlockSignature    = "blockSignature"
	TypeSnapshotSignature = "snapshotSignature"
	TypeTransaction       = "transaction"
	TypeAccessListEntry   = "accessListEntry"
	TypeLog               = "log"
)

// DefaultAdapter value to be used in FetcherFactory, ConverterFactory, and CollectionsFactory.
const DefaultAdapter = "evm"

// Collections is the chain-agnostic abstraction over a chain family's named
// collection set. Each chain family (EVM, future Cosmos) implements it so the
// generic BlockHandler, schema loader, and P2P layer can consume collection
// names without naming a chain-specific type.
type Collections interface {
	// Prefix returns the embedded-SDL prefix, e.g. "Ethereum__Mainnet".
	Prefix() string

	// AllCollections returns all collection names in P2P filter order.
	AllCollections() []string

	// SchemaApplyOrder returns the dependency-safe order for AddSchema.
	SchemaApplyOrder() []string

	// CollectionFileForType maps a collection type name to its .graphql filename.
	// e.g. "Ethereum__Mainnet__Block" → "block.graphql"
	// Returns empty string if the type name does not match the default prefix.
	CollectionFileForType(typeName string) string

	// GetCollection returns the collection name for the given type name string
	// (e.g. "block", "transaction", "log", "accessListEntry",
	// "blockSignature", "snapshotSignature"). Returns ErrUnknownCollection
	// when the type is not recognised.
	GetCollection(typeName string) (string, error)
}

// ------ Phase 2 ------

// Fetcher is the RPC I/O layer: fetches raw block data from the chain node.
// Concrete implementations (e.g. EVMFetcher) live in chain-specific packages.
//
// FetchBlock is safe to call concurrently across different heights; the
// orchestration layer is responsible for parallel fan-out.
type Fetcher interface {
	// Connect dials the chain RPC endpoint using the provided context for
	// timeout/cancellation. Called after construction (NewFetcherFromConfig)
	// and before any FetchBlock/FetchHighestBlockNumber calls. If the fetcher
	// was built with a pre-connected client (e.g. via NewFetcher for tests),
	// Connect is a no-op.
	Connect(ctx context.Context) error

	// FetchBlock retrieves the raw block data at the given height from the
	// chain RPC endpoint. The concrete return type is chain-specific (e.g.
	// an EVM block bundle); callers type-assert or pass it to a Converter.
	FetchBlock(ctx context.Context, height int64) (any, error)

	// FetchHighestBlockNumber returns the latest block number known to the
	// chain (the on-chain tip), querying the RPC endpoint directly.
	FetchHighestBlockNumber(ctx context.Context) (int64, error)

	// Close releases the underlying RPC connection.
	Close() error
}

// Converter is the chain-specific knowledge layer: schema generation,
// block-to-document conversion, and progress queries.
//
// It never stores *node.Node — it receives it explicitly on each progress
// call, keeping the Converter stateless and testable without a live DefraDB.
type Converter interface {
	// Convert transforms a raw block (returned by Fetcher.FetchBlock) into
	// a ConversionResult containing DocumentGroups, the block-signature
	// collection name, and a LinkStamper for cross-document link resolution.
	// The rawBlock parameter is typed as any to keep pkg/chains free of
	// chain SDK imports; the concrete Converter type-asserts internally.
	Convert(ctx context.Context, rawBlock any) (ConversionResult, error)

	// GetSchema returns the GraphQL SDL for the configured chain, with
	// collection names adapted to the chain's prefix.
	GetSchema() (string, error)

	// GetCollections returns the names of all collections for the configured
	// chain in dependency-safe order.
	GetCollections() []string

	// Collections returns the Collections interface for collection-name
	// resolution by role (reuses the Phase-1 interface).
	Collections() Collections

	// GetHighestStoredBlockNumber returns the highest block number currently
	// persisted in DefraDB.
	GetHighestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error)

	// GetLowestStoredBlockNumber returns the lowest block number currently
	// persisted in DefraDB. Useful for pruning windows.
	GetLowestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error)

	// GetDocIDsByBlockRange returns the DefraDB docIDs for every relevant
	// collection whose block-number field falls within [from, to] inclusive.
	GetDocIDsByBlockRange(ctx context.Context, n *node.Node, from, to int64) (map[string][]string, error)

	// SignatureCollection returns the collection name used for block
	// signatures (e.g. "Ethereum__Mainnet__BlockSignature") without requiring
	// a ConversionResult. Used by pruner/snapshot to resolve the block
	// signature collection and by the processor's storeWithRetry when
	// calling SignExisting.
	SignatureCollection() string
}

// DocumentGroup is a batch of documents destined for a single collection.
// Converters produce []DocumentGroup from raw chain data; BlockHandler.Store
// consumes them to persist in the appropriate collections.
type DocumentGroup struct {
	Collection string
	Docs       []map[string]any
	// BatchSize is the max documents per write transaction for this group.
	// When 0, the BlockHandler uses its default maxDocsPerTxn.
	BatchSize int
	// BlockNumField is the field name in each doc that holds the block number
	// (e.g. "number" for block docs, "blockNumber" for tx/log/ale docs).
	// Used by BlockHandler.SignExisting to query stored docIDs by block number.
	BlockNumField string

	// BlockHashField is the field name in each doc that holds the block hash
	// (e.g. "hash" for block docs). Empty for groups that don't carry a block
	// hash (tx/log/ale). Used by generic extractBlockHash to find the block
	// hash without chain-specific collection-name knowledge.
	BlockHashField string
}

// LinkStamper resolves cross-document link fields (_blockID, _transactionID)
// after AddDocument assigns persistent docIDs. It mutates the groups' doc
// maps in-place. Called by BlockHandler.Store in group order (block → tx →
// log → ALE) so the stamper can build internal lookup maps as each group
// is written.
type LinkStamper interface {
	StampLinks(groups []DocumentGroup, writtenCollection string, writtenDocs []map[string]any, writtenDocIDs []string)
}

// ConversionResult is the output of Converter.Convert — it bundles the
// document groups, the signature collection name, and the chain-specific
// LinkStamper needed for link resolution during Store.
type ConversionResult struct {
	Groups              []DocumentGroup
	SignatureCollection string
	LinkStamper         LinkStamper
}

// FetcherFactory is the constructor signature for chain-specific Fetcher
// implementations. Each chain family registers one under
// cfg.Chain.Adapter (e.g. "evm").
type FetcherFactory func(cfg *config.Config) (Fetcher, error)

// ConverterFactory is the constructor signature for chain-specific Converter
// implementations. Each chain family registers one under
// cfg.Chain.Adapter (e.g. "evm").
type ConverterFactory func(cfg *config.Config) (Converter, error)

// CollectionsFactory is the constructor signature for chain-specific Collections
// implementations. Each chain family registers one under
// cfg.Chain.Adapter (e.g. "evm").
type CollectionsFactory func(cfg *config.Config) (Collections, error)

var (
	fetcherFactoryRegistry     = map[string]FetcherFactory{}     //nolint:gochecknoglobals
	converterFactoryRegistry   = map[string]ConverterFactory{}   //nolint:gochecknoglobals
	collectionsFactoryRegistry = map[string]CollectionsFactory{} //nolint:gochecknoglobals
)

// RegisterFetcherFactory registers a Fetcher factory under the given name.
// Called from each chain package's init().
func RegisterFetcherFactory(name string, factory FetcherFactory) {
	fetcherFactoryRegistry[name] = factory
}

// RegisterConverterFactory registers a Converter factory under the given name.
// Called from each chain package's init().
func RegisterConverterFactory(name string, factory ConverterFactory) {
	converterFactoryRegistry[name] = factory
}

// RegisterCollectionsFactory registers a Collections factory under the given name.
// Called from each chain package's init().
func RegisterCollectionsFactory(name string, factory CollectionsFactory) {
	collectionsFactoryRegistry[name] = factory
}

// NewFetcher constructs the chain fetcher for the configured chain backend.
// Dispatch is via the factory registry: each chain package (e.g.
// pkg/chains/evm) calls RegisterFetcherFactory in its init(), and the
// binary blank-imports the package so the registration runs.
func NewFetcher(cfg *config.Config) (Fetcher, error) {
	name := cfg.Chain.Adapter
	if name == "" {
		name = DefaultAdapter
	}
	factory, ok := fetcherFactoryRegistry[name]
	if !ok {
		return nil, fmt.Errorf("fetcher factory %q not registered", name)
	}
	return factory(cfg)
}

// NewConverter constructs the chain converter for the configured chain backend.
// Dispatch is via the factory registry: each chain package (e.g.
// pkg/chains/evm) calls RegisterConverterFactory in its init(), and the
// binary blank-imports the package so the registration runs.
func NewConverter(cfg *config.Config) (Converter, error) {
	name := cfg.Chain.Adapter
	if name == "" {
		name = DefaultAdapter
	}
	factory, ok := converterFactoryRegistry[name]
	if !ok {
		return nil, fmt.Errorf("converter factory %q not registered", name)
	}
	return factory(cfg)
}

// NewCollections constructs the chain Collections for the configured chain backend.
// Dispatch is via the factory registry: each chain package (e.g.
// pkg/chains/evm) calls RegisterCollectionsFactory in its init(), and the
// binary blank-imports the package so the registration runs.
// A nil cfg defaults to the "evm" adapter.
func NewCollections(cfg *config.Config) (Collections, error) {
	name := DefaultAdapter
	if cfg != nil && cfg.Chain.Adapter != "" {
		name = cfg.Chain.Adapter
	}
	factory, ok := collectionsFactoryRegistry[name]
	if !ok {
		return nil, fmt.Errorf("collections factory %q not registered", name)
	}
	return factory(cfg)
}
