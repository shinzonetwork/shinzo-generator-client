package defradb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/utils"
	"github.com/sourcenetwork/defradb/crypto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testURL              = "127.0.0.1:0"
	testKeyringSecretAlt = "test-secret"
)

// --- MOCKS ---

// --- TESTS ---.
func TestStartDefra(t *testing.T) {
	// Create a copy of DefaultConfig to avoid modifying the shared instance
	testConfig := *DefaultConfig
	testConfig.DefraDB.URL = testURL
	testConfig.DefraDB.Store.Path = t.TempDir() // Use isolated temp directory for each test
	testConfig.DefraDB.KeyringSecret = testKeyringSecret
	myNode, _, err := StartDefraInstance(&testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)
	_ = myNode.Close(context.Background())
}

func TestStartDefraUsingConfig(t *testing.T) {
	configPath, err := utils.FindFile("config.yaml")
	require.NoError(t, err)

	testConfig, err := config.LoadConfig(configPath)
	require.NoError(t, err)

	testConfig.DefraDB.Store.Path = t.TempDir() // Use isolated temp directory for each test
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	myNode, _, err := StartDefraInstance(testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)
	_ = myNode.Close(context.Background())
}

func TestSubsequentRestartsYieldTheSameIdentity(t *testing.T) {
	testConfig := DefaultConfig
	testConfig.DefraDB.KeyringSecret = testKeyringSecret
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
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	client, err := NewClient(&testConfig)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Nil(t, client.GetNode())           // Should be nil before Start
	require.Nil(t, client.GetNetworkHandler()) // Should be nil before Start
}

func TestClientStartAndStop(t *testing.T) {
	testConfig := *DefaultConfig
	testConfig.DefraDB.URL = testURL
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

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
	testConfig.DefraDB.URL = testURL
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

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
	testConfig.DefraDB.URL = testURL
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	client, err := NewClient(&testConfig)
	require.NoError(t, err)

	ctx := context.Background()

	// Start the client
	err = client.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Stop(ctx) }()

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
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

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
	testConfig.DefraDB.URL = testURL
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	client, err := NewClient(&testConfig)
	require.NoError(t, err)

	ctx := context.Background()

	// Start the client
	err = client.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Stop(ctx) }()

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
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

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
	testConfig.DefraDB.URL = testURL
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

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

// ─── OpenKeyring tests ──────────────────────────────────────────────────────

func TestOpenKeyring_WithStorePath(t *testing.T) {
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: testKeyringSecretAlt,
			Store:         config.DefraDBStoreConfig{Path: t.TempDir()},
		},
	}
	kr, err := OpenKeyring(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, kr)
}

func TestOpenKeyring_EmptyStorePath(t *testing.T) {
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: testKeyringSecretAlt,
			Store:         config.DefraDBStoreConfig{Path: ""},
		},
	}
	kr, err := OpenKeyring(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, kr)
	_ = os.RemoveAll("keys")
}

func TestOpenKeyring_MkdirAllFails(t *testing.T) {
	tmpDir := t.TempDir()
	conflictPath := filepath.Join(tmpDir, "notadir")
	err := os.WriteFile(conflictPath, []byte("block"), 0o644)
	require.NoError(t, err)

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: testKeyringSecretAlt,
			Store:         config.DefraDBStoreConfig{Path: conflictPath},
		},
	}
	kr, err := OpenKeyring(cfg)
	assert.Error(t, err)
	assert.Nil(t, kr)
	assert.Contains(t, err.Error(), "failed to create keyring directory")
}

// ─── CreateLibP2PKeyFromIdentity tests ──────────────────────────────────────

func TestCreateLibP2PKeyFromIdentity_NotFullIdentity(t *testing.T) {
	_, err := CreateLibP2PKeyFromIdentity(&testutils.MockIdentityNotFull{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "identity is not a FullIdentity")
}

func TestCreateLibP2PKeyFromIdentity_NilPrivateKey(t *testing.T) {
	mock := &testutils.MockFullIdentity{PrivKey: nil}
	_, err := CreateLibP2PKeyFromIdentity(mock)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get private key from identity")
}

func TestCreateLibP2PKeyFromIdentity_EmptyRawBytes(t *testing.T) {
	mock := &testutils.MockFullIdentity{PrivKey: &testutils.MockPrivateKey{RawBytes: []byte{}}}
	_, err := CreateLibP2PKeyFromIdentity(mock)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "private key has no raw bytes")
}

func TestCreateLibP2PKeyFromIdentity_WrongKeyLength(t *testing.T) {
	mock := &testutils.MockFullIdentity{PrivKey: &testutils.MockPrivateKey{RawBytes: make([]byte, 16)}}
	_, err := CreateLibP2PKeyFromIdentity(mock)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected 32-byte secp256k1 key, got 16 bytes")
}

func TestCreateLibP2PKeyFromIdentity_ValidKey(t *testing.T) {
	// 32-byte key should work
	mock := &testutils.MockFullIdentity{PrivKey: &testutils.MockPrivateKey{RawBytes: make([]byte, 32)}}
	key, err := CreateLibP2PKeyFromIdentity(mock)
	assert.NoError(t, err)
	assert.NotNil(t, key)
}

// ───LoadIdentityFromKeyring tests ──────────────────────────────────────────

func TestLoadIdentityFromKeyring_GenericError(t *testing.T) {
	kr := &testutils.MockKeyring{GetErr: errors.New("disk failure")}
	_, err := LoadIdentityFromKeyring(kr)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get identity from keyring")
}

func TestLoadIdentityFromKeyring_InvalidKeyBytes(t *testing.T) {
	data := append([]byte(string(crypto.KeyTypeSecp256k1)+":"), []byte("bad")...)
	kr := &testutils.MockKeyring{Data: map[string][]byte{
		NodeIdentityKeyName: data,
	}}
	_, err := LoadIdentityFromKeyring(kr)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reconstruct private key")
}

func TestLoadIdentityFromKeyring_InvalidKeyType(t *testing.T) {
	data := append([]byte("invalidtype:"), []byte("somebytes1234567890123456789012")...)
	kr := &testutils.MockKeyring{Data: map[string][]byte{
		NodeIdentityKeyName: data,
	}}
	_, err := LoadIdentityFromKeyring(kr)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reconstruct private key")
}
