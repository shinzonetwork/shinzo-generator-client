package defra

import (
	"context"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-app-sdk/pkg/file"
	"github.com/stretchr/testify/require"
)

func TestStartDefra(t *testing.T) {
	// Create a copy of DefaultConfig to avoid modifying the shared instance
	testConfig := *DefaultConfig
	testConfig.DefraDB.Url = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = t.TempDir() // Use isolated temp directory for each test
	testConfig.DefraDB.KeyringSecret = "testSecret"
	myNode, _, err := StartDefraInstance(&testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)
	myNode.Close(context.Background())
}

func TestStartDefraUsingConfig(t *testing.T) {
	configPath, err := file.FindFile("config.yaml")
	require.NoError(t, err)

	testConfig, err := config.LoadConfig(configPath)
	require.NoError(t, err)

	testConfig.DefraDB.Store.Path = t.TempDir() // Use isolated temp directory for each test
	testConfig.DefraDB.KeyringSecret = "testSecret"

	myNode, _, err := StartDefraInstance(testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)
	myNode.Close(context.Background())
}

func TestSubsequentRestartsYieldTheSameIdentity(t *testing.T) {
	testConfig := DefaultConfig
	testConfig.DefraDB.KeyringSecret = "testSecret"
	myNode, _, err := StartDefraInstance(testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)

	peerInfo, err := myNode.DB.PeerInfo(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, peerInfo, "Peer info should not be empty")
	originalPeerID := peerInfo[0]

	err = myNode.Close(context.Background())
	require.NoError(t, err)

	myNode, _, err = StartDefraInstance(testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)

	newPeerInfo, err := myNode.DB.PeerInfo(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, newPeerInfo, "Peer info should not be empty")
	require.Equal(t, originalPeerID, newPeerInfo[0], "Peer ID should be the same across restarts")

	err = myNode.Close(context.Background())
	require.NoError(t, err)
}

// ====================================================================
// TESTS FOR NEW CLIENT API
// ====================================================================

func TestNewClient(t *testing.T) {
	testConfig := *DefaultConfig
	testConfig.DefraDB.KeyringSecret = "testSecret"

	client, err := NewClient(&testConfig)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Nil(t, client.GetNode())           // Should be nil before Start
	require.Nil(t, client.GetNetworkHandler()) // Should be nil before Start
}

func TestClientStartAndStop(t *testing.T) {
	testConfig := *DefaultConfig
	testConfig.DefraDB.Url = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = "testSecret"

	client, err := NewClient(&testConfig)
	require.NoError(t, err)

	ctx := context.Background()

	// Start the client
	err = client.Start(ctx)
	require.NoError(t, err)
	require.NotNil(t, client.GetNode())
	require.NotNil(t, client.GetNetworkHandler())

	// Stop the client
	err = client.Stop(ctx)
	require.NoError(t, err)
	require.Nil(t, client.GetNode())
	require.Nil(t, client.GetNetworkHandler())
}

func TestClientStartTwiceFails(t *testing.T) {
	testConfig := *DefaultConfig
	testConfig.DefraDB.Url = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = "testSecret"

	client, err := NewClient(&testConfig)
	require.NoError(t, err)

	ctx := context.Background()

	// Start the client once
	err = client.Start(ctx)
	require.NoError(t, err)

	// Try to start again - should fail
	err = client.Start(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already started")

	// Clean up
	err = client.Stop(ctx)
	require.NoError(t, err)
}

func TestClientApplySchema(t *testing.T) {
	testConfig := *DefaultConfig
	testConfig.DefraDB.Url = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = "testSecret"

	client, err := NewClient(&testConfig)
	require.NoError(t, err)

	ctx := context.Background()

	// Start the client
	err = client.Start(ctx)
	require.NoError(t, err)
	defer client.Stop(ctx)

	// Apply a simple schema
	schema := `
		type User {
			name: String
		}
	`

	err = client.ApplySchema(ctx, schema)
	require.NoError(t, err)

	// Test that schema was applied by querying the schema
	result := client.GetNode().DB.ExecRequest(ctx, `query { __schema { types { name } } }`)
	require.Empty(t, result.GQL.Errors)
	require.NotNil(t, result.GQL.Data)
}

func TestClientApplySchemaBeforeStartFails(t *testing.T) {
	testConfig := *DefaultConfig
	testConfig.DefraDB.KeyringSecret = "testSecret"

	client, err := NewClient(&testConfig)
	require.NoError(t, err)

	ctx := context.Background()

	// Try to apply schema before starting - should fail
	schema := `type User { id: ID! }`
	err = client.ApplySchema(ctx, schema)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be started")
}

func TestClientApplyEmptySchemaFails(t *testing.T) {
	testConfig := *DefaultConfig
	testConfig.DefraDB.Url = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = "testSecret"

	client, err := NewClient(&testConfig)
	require.NoError(t, err)

	ctx := context.Background()

	// Start the client
	err = client.Start(ctx)
	require.NoError(t, err)
	defer client.Stop(ctx)

	// Try to apply empty schema - should fail
	err = client.ApplySchema(ctx, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be empty")
}

func TestClientNilConfigFails(t *testing.T) {
	client, err := NewClient(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "config cannot be nil")
	require.Nil(t, client)
}

func TestClientStopBeforeStartSucceeds(t *testing.T) {
	testConfig := *DefaultConfig
	testConfig.DefraDB.KeyringSecret = "testSecret"

	client, err := NewClient(&testConfig)
	require.NoError(t, err)

	ctx := context.Background()

	// Stop before starting should succeed (no-op)
	err = client.Stop(ctx)
	require.NoError(t, err)
	require.Nil(t, client.GetNode())
}

func TestClientIntegration(t *testing.T) {
	testConfig := *DefaultConfig
	testConfig.DefraDB.Url = "127.0.0.1:0"
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = "testSecret"

	client, err := NewClient(&testConfig)
	require.NoError(t, err)

	ctx := context.Background()

	// Full lifecycle test
	err = client.Start(ctx)
	require.NoError(t, err)

	// Verify node is working
	require.NotNil(t, client.GetNode())
	require.NotNil(t, client.GetNetworkHandler())

	// Test basic functionality
	result := client.GetNode().DB.ExecRequest(ctx, `query { __schema { types { name } } }`)
	require.Empty(t, result.GQL.Errors)

	// Apply schema
	schema := `
		type Test {
			value: String
		}
	`
	err = client.ApplySchema(ctx, schema)
	require.NoError(t, err)

	// Clean shutdown
	err = client.Stop(ctx)
	require.NoError(t, err)
}