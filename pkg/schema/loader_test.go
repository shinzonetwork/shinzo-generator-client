package schema

import (
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

func TestListCollections(t *testing.T) {
	t.Parallel()

	entries := ListCollections("Arbitrum__Sepolia")

	expectedNames := []string{"block", "blockSignature", "snapshotSignature", "transaction", "accessListEntry", "log"}
	expectedTypeNames := []string{
		"Arbitrum__Sepolia__Block",
		"Arbitrum__Sepolia__BlockSignature",
		"Arbitrum__Sepolia__SnapshotSignature",
		"Arbitrum__Sepolia__Transaction",
		"Arbitrum__Sepolia__AccessListEntry",
		"Arbitrum__Sepolia__Log",
	}

	if len(entries) != len(expectedNames) {
		t.Fatalf("expected %d entries, got %d", len(expectedNames), len(entries))
	}

	for i, e := range entries {
		if e.Name != expectedNames[i] {
			t.Errorf("entries[%d].Name = %q, want %q", i, e.Name, expectedNames[i])
		}
		if e.TypeName != expectedTypeNames[i] {
			t.Errorf("entries[%d].TypeName = %q, want %q", i, e.TypeName, expectedTypeNames[i])
		}
	}
}

func TestListCollections_DefaultPrefix(t *testing.T) {
	t.Parallel()

	entries := ListCollections(constants.DefaultCollectionPrefix)

	order := constants.SchemaApplyOrder()
	if len(entries) != len(order) {
		t.Fatalf("expected %d entries, got %d", len(order), len(entries))
	}

	for i, e := range entries {
		if e.TypeName != order[i] {
			t.Errorf("entries[%d].TypeName = %q, want %q (SchemaApplyOrder()[%d])", i, e.TypeName, order[i], i)
		}
	}
}

func TestIsValidCollection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		expect bool
	}{
		{"block", "block", true},
		{"blockSignature", "blockSignature", true},
		{"snapshotSignature", "snapshotSignature", true},
		{"transaction", "transaction", true},
		{"accessListEntry", "accessListEntry", true},
		{"log", "log", true},
		{"unknown", "unknown", false},
		{"empty", "", false},
		{"uppercase", "Block", false},
		{"with_extension", "block.graphql", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidCollection(tt.input)
			if got != tt.expect {
				t.Errorf("IsValidCollection(%q) = %v, want %v", tt.input, got, tt.expect)
			}
		})
	}
}
