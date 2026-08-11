package testutils

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

const (
	MockBlockCol       = "Ethereum__Mainnet__Block"
	MockTxCol          = "Ethereum__Mainnet__Transaction"
	MockLogCol         = "Ethereum__Mainnet__Log"
	MockAleCol         = "Ethereum__Mainnet__AccessListEntry"
	MockBlockSigCol    = "Ethereum__Mainnet__BlockSignature"
	MockSnapshotSigCol = "Ethereum__Mainnet__SnapshotSignature"
)

var mockTypeToFile = map[string]string{
	MockBlockCol:       "block.graphql",
	MockBlockSigCol:    "blockSignature.graphql",
	MockSnapshotSigCol: "snapshotSignature.graphql",
	MockTxCol:          "transaction.graphql",
	MockAleCol:         "accessListEntry.graphql",
	MockLogCol:         "log.graphql",
}

type MockCollections struct {
	prefix string
}

func NewMockCollections() *MockCollections {
	return &MockCollections{prefix: "Ethereum__Mainnet"}
}

func (m *MockCollections) Prefix() string { return m.prefix }

func (m *MockCollections) AllCollections() []string {
	return []string{
		MockBlockCol,
		MockBlockSigCol,
		MockSnapshotSigCol,
		MockTxCol,
		MockAleCol,
		MockLogCol,
	}
}

func (m *MockCollections) SchemaApplyOrder() []string {
	return []string{
		MockBlockCol,
		MockBlockSigCol,
		MockSnapshotSigCol,
		MockTxCol,
		MockAleCol,
		MockLogCol,
	}
}

func (m *MockCollections) CollectionFileForType(typeName string) string {
	return mockTypeToFile[typeName]
}

func (m *MockCollections) GetCollection(role string) (string, error) {
	switch role {
	case "block":
		return MockBlockCol, nil
	case "blockSignature":
		return MockBlockSigCol, nil
	case "snapshotSignature":
		return MockSnapshotSigCol, nil
	case "transaction":
		return MockTxCol, nil
	case "accessListEntry":
		return MockAleCol, nil
	case "log":
		return MockLogCol, nil
	default:
		return "", fmt.Errorf("%w: %s", chains.ErrUnknownCollection, role)
	}
}

var _ chains.Collections = (*MockCollections)(nil)