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
		CollectionTransaction,
		CollectionAccessListEntry,
		CollectionLog,
		CollectionBlockSignature,
		CollectionSnapshotSignature,
	}

	for i, exp := range expected {
		if collections[i] != exp {
			t.Errorf("collections[%d]: expected %q, got %q", i, exp, collections[i])
		}
	}
}
