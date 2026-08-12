package chains

import (
	"fmt"
	"strings"
)

// StubCollections is a test stub implementing the Collections interface,
// used by tests that cannot import pkg/chains/evm due to cyclic dependencies.
// TODO: Remove once cyclic dependencies are broken at the end of the Phase 2 migration.
type StubCollections struct {
	prefix            string
	block             string
	blockSignature    string
	snapshotSignature string
	transaction       string
	accessListEntry   string
	log               string
}

var _ Collections = (*StubCollections)(nil)

// NewStubCollections creates a StubCollections with the given prefix.
func NewStubCollections(prefix string) *StubCollections {
	return &StubCollections{
		prefix:            prefix,
		block:             fmt.Sprintf("%s__Block", prefix),
		blockSignature:    fmt.Sprintf("%s__BlockSignature", prefix),
		snapshotSignature: fmt.Sprintf("%s__SnapshotSignature", prefix),
		transaction:       fmt.Sprintf("%s__Transaction", prefix),
		accessListEntry:   fmt.Sprintf("%s__AccessListEntry", prefix),
		log:               fmt.Sprintf("%s__Log", prefix),
	}
}

// Prefix returns the chain prefix.
func (s *StubCollections) Prefix() string { return s.prefix }

// AllCollections returns all collection names in P2P filter order.
func (s *StubCollections) AllCollections() []string {
	return []string{
		s.block,
		s.blockSignature,
		s.snapshotSignature,
		s.transaction,
		s.accessListEntry,
		s.log,
	}
}

// SchemaApplyOrder returns collection names in dependency-safe order.
func (s *StubCollections) SchemaApplyOrder() []string {
	return []string{
		s.block,
		s.blockSignature,
		s.snapshotSignature,
		s.transaction,
		s.accessListEntry,
		s.log,
	}
}

// CollectionFileForType maps a collection type name to its .graphql filename.
func (s *StubCollections) CollectionFileForType(typeName string) string {
	prefix := s.prefix + "__"
	suffix := strings.TrimPrefix(typeName, prefix)
	if suffix == typeName {
		return ""
	}
	return strings.ToLower(suffix[:1]) + suffix[1:] + ".graphql"
}

// GetCollection returns the collection name for the given role.
func (s *StubCollections) GetCollection(role string) (string, error) {
	switch role {
	case "block":
		return s.block, nil
	case "blockSignature":
		return s.blockSignature, nil
	case "snapshotSignature":
		return s.snapshotSignature, nil
	case "transaction":
		return s.transaction, nil
	case "accessListEntry":
		return s.accessListEntry, nil
	case "log":
		return s.log, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownCollection, role)
	}
}
