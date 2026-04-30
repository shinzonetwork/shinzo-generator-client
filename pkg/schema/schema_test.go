//go:build !branchable
// +build !branchable

package schema

import (
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

func TestGetSchema(t *testing.T) {
	t.Parallel()
	s := GetSchema()
	if s == "" {
		t.Fatal("GetSchema() returned empty string")
	}
	expectedType := constants.DefaultCollectionPrefix + "__Block"
	if !strings.Contains(s, expectedType) {
		t.Errorf("schema should contain %s type", expectedType)
	}
}
