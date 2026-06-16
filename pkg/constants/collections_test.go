package constants

import (
	"testing"
)

func TestCollectionConstants(t *testing.T) {
	t.Parallel()
	prefix := DefaultCollectionPrefix
	tests := []struct {
		name     string
		constant string
		expected string
	}{
		{"Block", CollectionBlock, prefix + "__Block"},
		{"Transaction", CollectionTransaction, prefix + "__Transaction"},
		{"Log", CollectionLog, prefix + "__Log"},
		{"AccessListEntry", CollectionAccessListEntry, prefix + "__AccessListEntry"},
		{"BlockSignature", CollectionBlockSignature, prefix + "__BlockSignature"},
		{"SnapshotSignature", CollectionSnapshotSignature, prefix + "__SnapshotSignature"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.constant != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, tt.constant)
			}
		})
	}
}

func TestAllCollections(t *testing.T) {
	t.Parallel()

	collections := DefaultCollections()

	if collections == nil {
		t.Fatal("AllCollections should not be nil")
	}

	if len(collections) != 6 {
		t.Fatalf("expected 6 collections, got %d", len(collections))
	}

	expected := []string{
		CollectionBlock,
		CollectionBlockSignature,
		CollectionSnapshotSignature,
		CollectionTransaction,
		CollectionAccessListEntry,
		CollectionLog,
	}

	for i, exp := range expected {
		if collections[i] != exp {
			t.Errorf("collections[%d]: expected %q, got %q", i, exp, collections[i])
		}
	}
}

func TestSchemaApplyOrder(t *testing.T) {
	t.Parallel()

	order := SchemaApplyOrder()
	if len(order) != 6 {
		t.Fatalf("expected 6 entries, got %d", len(order))
	}

	expected := []string{
		CollectionBlock,
		CollectionBlockSignature,
		CollectionSnapshotSignature,
		CollectionTransaction,
		CollectionAccessListEntry,
		CollectionLog,
	}

	for i, exp := range expected {
		if order[i] != exp {
			t.Errorf("SchemaApplyOrder()[%d]: expected %q, got %q", i, exp, order[i])
		}
	}
}

func TestCollectionFileForType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		typeName string
		expected string
	}{
		{"Block", CollectionBlock, "block.graphql"},
		{"Transaction", CollectionTransaction, "transaction.graphql"},
		{"Log", CollectionLog, "log.graphql"},
		{"AccessListEntry", CollectionAccessListEntry, "accessListEntry.graphql"},
		{"BlockSignature", CollectionBlockSignature, "blockSignature.graphql"},
		{"SnapshotSignature", CollectionSnapshotSignature, "snapshotSignature.graphql"},
		{"UnknownPrefix", "UnknownPrefix__Block", ""},
		{"NoPrefix", "Block", ""},
		{"Empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CollectionFileForType(tt.typeName)
			if got != tt.expected {
				t.Errorf("CollectionFileForType(%q) = %q, want %q", tt.typeName, got, tt.expected)
			}
		})
	}
}
