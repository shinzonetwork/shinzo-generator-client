package evm

import (
	"fmt"
	"slices"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// DefaultCollectionPrefix is the prefix the embedded SDL files are written with;
// CollectionFiles replaces it with the configured chain's prefix.
const DefaultCollectionPrefix = "Ethereum__Mainnet"

// Collection names for Ethereum Mainnet, built from DefaultCollectionPrefix.
const (
	DefaultCollectionBlock             = DefaultCollectionPrefix + "__Block"
	DefaultCollectionTransaction       = DefaultCollectionPrefix + "__Transaction"
	DefaultCollectionLog               = DefaultCollectionPrefix + "__Log"
	DefaultCollectionAccessListEntry   = DefaultCollectionPrefix + "__AccessListEntry"
	DefaultCollectionBlockSignature    = DefaultCollectionPrefix + "__BlockSignature"
	DefaultCollectionSnapshotSignature = DefaultCollectionPrefix + "__SnapshotSignature"
)

// CollectionNames holds the dynamically generated EVM collection names for a
// chain.
//
// It implements chains.Collections for the generic schema loader and future
// generic BlockHandler.
//
// In tokens mode only Header, BlockSignature and ContractLogs are set, and
// Header takes Block's place as the table the chain's heights are read from.
//
// Every collection except SnapshotSignature must declare an @index on its
// block-number field (number on Block and Header, blockNumber elsewhere): the
// pruner orders and filters by it, and GetDocIDsByBlockRange queries it.
// Enforced by pruner.TestSchemaIndexesBlockNumberField.
type CollectionNames struct {
	prefix            string
	Block             string
	BlockSignature    string
	SnapshotSignature string
	Transaction       string
	AccessListEntry   string
	Log               string

	Header       string
	ContractLogs []ContractLogCollection
}

// ContractLogCollection is the tokens-mode table holding one contract's logs.
type ContractLogCollection struct {
	Address string // 40 lower-case hex digits without 0x
	Name    string // <prefix>__ContractLog_<Address>
}

// Compile-time guarantee that CollectionNames implements chains.Collections.
var _ chains.Collections = (*CollectionNames)(nil)

// NewCollectionNames creates EVM collection names using the given prefix
// (e.g. "Arbitrum__Mainnet").
func NewCollectionNames(prefix string) *CollectionNames {
	return &CollectionNames{
		prefix:            prefix,
		Block:             fmt.Sprintf("%s__Block", prefix),
		BlockSignature:    fmt.Sprintf("%s__BlockSignature", prefix),
		SnapshotSignature: fmt.Sprintf("%s__SnapshotSignature", prefix),
		Transaction:       fmt.Sprintf("%s__Transaction", prefix),
		AccessListEntry:   fmt.Sprintf("%s__AccessListEntry", prefix),
		Log:               fmt.Sprintf("%s__Log", prefix),
	}
}

// NewTokenCollectionNames creates the tokens-mode collection names: Header,
// BlockSignature and one ContractLog table per contract. The tables are sorted
// by address, so the schema does not depend on the order the contracts are
// listed in. It returns config.ErrInvalidContractAddress for an address that
// is not 20 bytes of hex.
func NewTokenCollectionNames(prefix string, contracts []string) (*CollectionNames, error) {
	c := &CollectionNames{
		prefix:         prefix,
		Header:         fmt.Sprintf("%s__Header", prefix),
		BlockSignature: fmt.Sprintf("%s__BlockSignature", prefix),
	}
	for _, raw := range contracts {
		addr, err := config.ContractAddress(raw)
		if err != nil {
			return nil, err
		}
		c.ContractLogs = append(c.ContractLogs, ContractLogCollection{
			Address: addr,
			Name:    fmt.Sprintf("%s__ContractLog_%s", prefix, addr),
		})
	}
	slices.SortFunc(c.ContractLogs, func(a, b ContractLogCollection) int { return strings.Compare(a.Address, b.Address) })
	return c, nil
}

// collectionNamesFromConfig returns the collection names for cfg's chain and
// mode. It returns config.ErrChainUnset when cfg names no chain.
func collectionNamesFromConfig(cfg *config.Config) (*CollectionNames, error) {
	prefix, err := chainPrefixFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Chain.Mode == config.ChainModeTokens {
		return NewTokenCollectionNames(prefix, cfg.Chain.Contracts)
	}
	return NewCollectionNames(prefix), nil
}

// tokenMode reports whether the names are tokens-mode names.
func (c *CollectionNames) tokenMode() bool {
	return c.Header != ""
}

// Prefix returns the chain prefix (e.g. "Ethereum__Mainnet").
func (c *CollectionNames) Prefix() string {
	return c.prefix
}

// AllCollections returns all collection names as a slice in P2P filter order.
func (c *CollectionNames) AllCollections() []string {
	if c.tokenMode() {
		names := []string{c.Header, c.BlockSignature}
		for _, cl := range c.ContractLogs {
			names = append(names, cl.Name)
		}
		return names
	}
	return []string{
		c.Block,
		c.BlockSignature,
		c.SnapshotSignature,
		c.Transaction,
		c.AccessListEntry,
		c.Log,
	}
}

// BlockCollection implements chains.Collections. It returns the collection
// name that stores block documents: Header in tokens mode, Block otherwise.
func (c *CollectionNames) BlockCollection() string {
	if c.tokenMode() {
		return c.Header
	}
	return c.Block
}

// SnapshotSignatureCollection implements chains.Collections. It returns the
// collection name that stores snapshot signature documents, empty in tokens
// mode, which has no snapshots.
func (c *CollectionNames) SnapshotSignatureCollection() string {
	return c.SnapshotSignature
}

// BlockSignatureCollection implements chains.Collections. It returns the
// collection name that stores block signature documents.
func (c *CollectionNames) BlockSignatureCollection() string {
	return c.BlockSignature
}

// DefaultCollections returns the Ethereum Mainnet collection names as a slice.
func DefaultCollections() []string {
	return []string{
		DefaultCollectionBlock,
		DefaultCollectionBlockSignature,
		DefaultCollectionSnapshotSignature,
		DefaultCollectionTransaction,
		DefaultCollectionAccessListEntry,
		DefaultCollectionLog,
	}
}
