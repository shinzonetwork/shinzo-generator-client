package chains

import (
	"fmt"
	"strings"
)

type StubCollections struct {
	prefix            string
	block             string
	blockSignature     string
	snapshotSignature  string
	transaction       string
	accessListEntry   string
	log               string
}

var _ Collections = (*StubCollections)(nil)

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

func (s *StubCollections) Prefix() string { return s.prefix }

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

func (s *StubCollections) CollectionFileForType(typeName string) string {
	prefix := s.prefix + "__"
	suffix := strings.TrimPrefix(typeName, prefix)
	if suffix == typeName {
		return ""
	}
	return strings.ToLower(suffix[:1]) + suffix[1:] + ".graphql"
}

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