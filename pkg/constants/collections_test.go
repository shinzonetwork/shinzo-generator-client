package constants

import (
	"testing"
)

func TestCollectionConstants(t *testing.T) {
	tests := []struct {
		name     string
		constant string
		expected string
	}{
		{"Block", CollectionBlock, "Ethereum__Mainnet__Block"},
		{"Transaction", CollectionTransaction, "Ethereum__Mainnet__Transaction"},
		{"Log", CollectionLog, "Ethereum__Mainnet__Log"},
		{"AccessListEntry", CollectionAccessListEntry, "Ethereum__Mainnet__AccessListEntry"},
		{"BlockSignature", CollectionBlockSignature, "Ethereum__Mainnet__BlockSignature"},
		{"SnapshotSignature", CollectionSnapshotSignature, "Ethereum__Mainnet__SnapshotSignature"},
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
	if AllCollections == nil {
		t.Fatal("AllCollections should not be nil")
	}

	if len(AllCollections) != 6 {
		t.Fatalf("expected 6 collections, got %d", len(AllCollections))
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
		if AllCollections[i] != exp {
			t.Errorf("AllCollections[%d]: expected %q, got %q", i, exp, AllCollections[i])
		}
	}
}
