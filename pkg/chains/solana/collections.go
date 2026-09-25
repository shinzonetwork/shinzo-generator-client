package solana

import (
	"embed"
	"fmt"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
)

// DefaultCollectionPrefix is the default collection prefix for the Solana adapter.
const DefaultCollectionPrefix = "Solana__Mainnet"

// Collection name constants for the default Solana Mainnet chain.
const (
	CollectionBlock              = DefaultCollectionPrefix + "__Block"
	CollectionBlockSignature     = DefaultCollectionPrefix + "__BlockSignature"
	CollectionSnapshotSignature  = DefaultCollectionPrefix + "__SnapshotSignature"
	CollectionTransaction        = DefaultCollectionPrefix + "__Transaction"
	CollectionInstruction        = DefaultCollectionPrefix + "__Instruction"
	CollectionTokenBalanceChange = DefaultCollectionPrefix + "__TokenBalanceChange"
	CollectionReward             = DefaultCollectionPrefix + "__Reward"
)

const (
	// defaultChainName is the fallback chain name when config is empty.
	defaultChainName = "Solana"

	// defaultNetwork is the fallback network name when config is empty.
	defaultNetwork = "Mainnet"
)

//go:embed collections/*.graphql
var collectionFS embed.FS

// CollectionNames holds the dynamically generated Solana collection names for
// a chain.
//
// It implements chains.Collections for the generic schema loader, BlockHandler,
// and P2P layer, and schema.CollectionSDLProvider so the generic loader sources
// this adapter's own embedded SDL instead of the shared EVM SDL.
type CollectionNames struct {
	prefix             string
	Block              string
	BlockSignature     string
	SnapshotSignature  string
	Transaction        string
	Instruction        string
	TokenBalanceChange string
	Reward             string
}

// Compile-time guarantees that CollectionNames implements the interfaces the
// generic machinery consumes.
var (
	_ chains.Collections           = (*CollectionNames)(nil)
	_ schema.CollectionSDLProvider = (*CollectionNames)(nil)
)

// NewCollectionNames creates Solana collection names using the given prefix
// (e.g. "Solana__Devnet").
func NewCollectionNames(prefix string) *CollectionNames {
	return &CollectionNames{
		prefix:             prefix,
		Block:              fmt.Sprintf("%s__Block", prefix),
		BlockSignature:     fmt.Sprintf("%s__BlockSignature", prefix),
		SnapshotSignature:  fmt.Sprintf("%s__SnapshotSignature", prefix),
		Transaction:        fmt.Sprintf("%s__Transaction", prefix),
		Instruction:        fmt.Sprintf("%s__Instruction", prefix),
		TokenBalanceChange: fmt.Sprintf("%s__TokenBalanceChange", prefix),
		Reward:             fmt.Sprintf("%s__Reward", prefix),
	}
}

// chainPrefixFromConfig derives the collection prefix (e.g. "Solana__Mainnet")
// from the chain config, applying the same defaults as the EVM adapter.
func chainPrefixFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return DefaultCollectionPrefix
	}
	name := cfg.Chain.Name
	network := cfg.Chain.Network
	if name == "" {
		name = defaultChainName
	}
	if network == "" {
		network = defaultNetwork
	}
	return fmt.Sprintf("%s__%s", name, network)
}

// Prefix returns the chain prefix (e.g. "Solana__Mainnet").
func (c *CollectionNames) Prefix() string {
	return c.prefix
}

// AllCollections returns all collection names as a slice in P2P filter order.
func (c *CollectionNames) AllCollections() []string {
	return []string{
		c.Block,
		c.BlockSignature,
		c.SnapshotSignature,
		c.Transaction,
		c.Instruction,
		c.TokenBalanceChange,
		c.Reward,
	}
}

// SchemaApplyOrder returns collection type names in dependency-safe order
// for per-file AddSchema calls.
func (c *CollectionNames) SchemaApplyOrder() []string {
	return []string{
		c.Block,
		c.BlockSignature,
		c.SnapshotSignature,
		c.Transaction,
		c.Instruction,
		c.TokenBalanceChange,
		c.Reward,
	}
}

// CollectionFileForType maps a collection type name to its .graphql filename.
// e.g. "Solana__Mainnet__Block" → "block.graphql"
// Returns empty string if the type name does not match this chain's prefix.
func (c *CollectionNames) CollectionFileForType(typeName string) string {
	prefix := c.prefix + "__"
	suffix := strings.TrimPrefix(typeName, prefix)
	if suffix == typeName {
		return ""
	}
	return strings.ToLower(suffix[:1]) + suffix[1:] + ".graphql"
}

// GetCollection returns the collection name for the given role string.
// Returns chains.ErrUnknownCollection for unknown roles, including the
// EVM-only roles (accessListEntry, log).
func (c *CollectionNames) GetCollection(role string) (string, error) {
	switch role {
	case chains.TypeBlock:
		return c.Block, nil
	case chains.TypeBlockSignature:
		return c.BlockSignature, nil
	case chains.TypeSnapshotSignature:
		return c.SnapshotSignature, nil
	case chains.TypeTransaction:
		return c.Transaction, nil
	case chains.TypeInstruction:
		return c.Instruction, nil
	case chains.TypeTokenBalanceChange:
		return c.TokenBalanceChange, nil
	case chains.TypeReward:
		return c.Reward, nil
	default:
		return "", fmt.Errorf("%w: %s", chains.ErrUnknownCollection, role)
	}
}

// CollectionSDL implements schema.CollectionSDLProvider: it returns the SDL
// for the given collection .graphql filename from this package's embedded
// files, with the chain's collection prefix already applied.
func (c *CollectionNames) CollectionSDL(filename string) (string, error) {
	data, err := collectionFS.ReadFile("collections/" + filename)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", filename, err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("collection file is empty: %s", filename)
	}
	return strings.ReplaceAll(content, embeddedSDLPrefix, c.prefix), nil
}

// DefaultCollections returns all default collection names as a slice.
func DefaultCollections() []string {
	return []string{
		CollectionBlock,
		CollectionBlockSignature,
		CollectionSnapshotSignature,
		CollectionTransaction,
		CollectionInstruction,
		CollectionTokenBalanceChange,
		CollectionReward,
	}
}

// SchemaApplyOrder returns collection type names in dependency-safe order
// for per-file AddSchema calls, using the default prefix.
func SchemaApplyOrder() []string {
	return []string{
		CollectionBlock,
		CollectionBlockSignature,
		CollectionSnapshotSignature,
		CollectionTransaction,
		CollectionInstruction,
		CollectionTokenBalanceChange,
		CollectionReward,
	}
}
