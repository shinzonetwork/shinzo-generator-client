package defradb

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/keyring"

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
	// Create a copy of the default config to avoid modifying the shared instance
	testConfig := *NewDefaultConfig()
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
	testConfig := NewDefaultConfig()
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
	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	client, err := NewClient(&testConfig)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Nil(t, client.GetNode())           // Should be nil before Start
	require.Nil(t, client.GetNetworkHandler()) // Should be nil before Start
}

func TestClientStartAndStop(t *testing.T) {
	testConfig := *NewDefaultConfig()
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
	testConfig := *NewDefaultConfig()
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
	testConfig := *NewDefaultConfig()
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
	testConfig := *NewDefaultConfig()
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
	testConfig := *NewDefaultConfig()
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
	testConfig := *NewDefaultConfig()
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
	testConfig := *NewDefaultConfig()
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
	tmpDir := t.TempDir()
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: testKeyringSecretAlt,
			Store:         config.DefraDBStoreConfig{Path: filepath.Join(tmpDir, "store")},
		},
	}
	kr, err := OpenKeyring(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, kr)
	// The keyring lives beside the store, not inside it.
	assert.DirExists(t, filepath.Join(tmpDir, "keys"))
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
	// Block the sibling keys directory, where the keyring now lives, with a
	// regular file so its creation fails.
	conflictPath := filepath.Join(tmpDir, "keys")
	err := os.WriteFile(conflictPath, []byte("block"), 0o600)
	require.NoError(t, err)

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: testKeyringSecretAlt,
			Store:         config.DefraDBStoreConfig{Path: filepath.Join(tmpDir, "store")},
		},
	}
	kr, err := OpenKeyring(cfg)
	assert.Error(t, err)
	assert.Nil(t, kr)
	assert.Contains(t, err.Error(), "failed to create keyring directory")
}

// ─── Keyring helpers ────────────────────────────────────────────────────────

// newTestIdentityBytes generates a fresh secp256k1 identity serialized in the
// keyring's "keyType:rawKeyBytes" format.
func newTestIdentityBytes(t *testing.T) []byte {
	t.Helper()
	id, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	full := id
	raw := full.PrivateKey().Raw()
	require.NotEmpty(t, raw)
	return append([]byte(string(full.PrivateKey().Type())+":"), raw...)
}

// seedKeyringAt writes a file keyring holding a node-identity-key encrypted
// with testKeyringSecretAlt, simulating a pre-migration layout.
func seedKeyringAt(t *testing.T, keyringDir string, identityBytes []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(keyringDir, 0o750))
	kr, err := keyring.OpenFileKeyring(keyringDir, []byte(testKeyringSecretAlt))
	require.NoError(t, err)
	require.NoError(t, kr.Set(NodeIdentityKeyName, identityBytes))
}

// ─── Keyring location tests ─────────────────────────────────────────────────

func TestGetOrCreateNodeIdentity_FreshStartStoresKeyBesideStore(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "store")
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: testKeyringSecretAlt,
			Store:         config.DefraDBStoreConfig{Path: storePath},
		},
	}

	id, err := GetOrCreateNodeIdentity(cfg)
	require.NoError(t, err)
	require.NotNil(t, id)

	// The identity lives in the sibling keys directory.
	stored, err := os.ReadFile(filepath.Join(tmpDir, "keys", NodeIdentityKeyName))
	require.NoError(t, err)
	assert.NotEmpty(t, stored)

	// No keyring was ever created inside the store.
	_, err = os.Stat(filepath.Join(storePath, "keys"))
	assert.True(t, os.IsNotExist(err))
}

func TestOpenKeyring_MigratesLegacyKeyring(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "store")
	legacyDir := filepath.Join(storePath, "keys")

	seed := newTestIdentityBytes(t)
	seedKeyringAt(t, legacyDir, seed)

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: testKeyringSecretAlt,
			Store:         config.DefraDBStoreConfig{Path: storePath},
		},
	}

	kr, err := OpenKeyring(cfg)
	require.NoError(t, err)
	assert.DirExists(t, filepath.Join(tmpDir, "keys"))

	// The migrated keyring holds the legacy identity, so the node keeps its
	// DID and peer ID across the upgrade.
	loaded, err := LoadIdentityFromKeyring(kr)
	require.NoError(t, err)
	seededFull, err := LoadIdentityFromBytes(seed)
	require.NoError(t, err)
	loadedFull, ok := loaded.(identity.FullIdentity)
	require.True(t, ok)
	assert.Equal(t, seededFull.PublicKey().String(), loadedFull.PublicKey().String())

	// The legacy directory stays in place as an operator-visible backup.
	legacyData, err := os.ReadFile(filepath.Join(legacyDir, NodeIdentityKeyName))
	require.NoError(t, err)
	assert.NotEmpty(t, legacyData)
}

func TestOpenKeyring_NoMigrationWhenKeyAlreadyAtNewLocation(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "store")
	legacyDir := filepath.Join(storePath, "keys")
	siblingDir := filepath.Join(tmpDir, "keys")

	newSeed := newTestIdentityBytes(t)
	legacySeed := newTestIdentityBytes(t)
	seedKeyringAt(t, siblingDir, newSeed)
	seedKeyringAt(t, legacyDir, legacySeed)

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: testKeyringSecretAlt,
			Store:         config.DefraDBStoreConfig{Path: storePath},
		},
	}

	kr, err := OpenKeyring(cfg)
	require.NoError(t, err)

	// The key already beside the store wins; the legacy key is never copied
	// over it.
	loaded, err := LoadIdentityFromKeyring(kr)
	require.NoError(t, err)
	newFull, err := LoadIdentityFromBytes(newSeed)
	require.NoError(t, err)
	legacyFull, err := LoadIdentityFromBytes(legacySeed)
	require.NoError(t, err)
	loadedFull, ok := loaded.(identity.FullIdentity)
	require.True(t, ok)
	assert.Equal(t, newFull.PublicKey().String(), loadedFull.PublicKey().String())
	assert.NotEqual(t, legacyFull.PublicKey().String(), loadedFull.PublicKey().String())
}

// ─── Plaintext identity import tests ────────────────────────────────────────

func TestGetOrCreateNodeIdentity_ImportsPlaintextKey(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "store")
	require.NoError(t, os.MkdirAll(storePath, 0o750))

	id, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	full := id
	plaintext := hex.EncodeToString(full.PrivateKey().Raw())
	require.NoError(t, os.WriteFile(filepath.Join(storePath, "defra_identity.key"), []byte(plaintext), 0o600))

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: testKeyringSecretAlt,
			Store:         config.DefraDBStoreConfig{Path: storePath},
		},
	}

	// The legacy plaintext key is imported, so the node keeps its identity
	// instead of generating a new one.
	loaded, err := GetOrCreateNodeIdentity(cfg)
	require.NoError(t, err)
	loadedFull, ok := loaded.(identity.FullIdentity)
	require.True(t, ok)
	assert.Equal(t, full.PublicKey().String(), loadedFull.PublicKey().String())

	// The imported identity now lives in the keyring beside the store.
	kr, err := OpenKeyring(cfg)
	require.NoError(t, err)
	stored, err := kr.Get(NodeIdentityKeyName)
	require.NoError(t, err)
	persisted, err := LoadIdentityFromBytes(stored)
	require.NoError(t, err)
	persistedFull, ok := persisted.(identity.FullIdentity)
	require.True(t, ok)
	assert.Equal(t, full.PublicKey().String(), persistedFull.PublicKey().String())
}

func TestGetOrCreateNodeIdentity_CorruptPlaintextFileIsSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "store")
	require.NoError(t, os.MkdirAll(storePath, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(storePath, "defra_identity.key"), []byte("not-valid-hex!"), 0o600))

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: testKeyringSecretAlt,
			Store:         config.DefraDBStoreConfig{Path: storePath},
		},
	}

	// An unparsable legacy file must not block startup; a fresh identity is
	// generated instead.
	id, err := GetOrCreateNodeIdentity(cfg)
	require.NoError(t, err)
	require.NotNil(t, id)

	kr, err := OpenKeyring(cfg)
	require.NoError(t, err)
	stored, err := kr.Get(NodeIdentityKeyName)
	require.NoError(t, err)
	assert.NotEmpty(t, stored)
}

func TestOpenKeyring_PlaintextIgnoredWhenKeyringHasKey(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "store")
	require.NoError(t, os.MkdirAll(storePath, 0o750))

	keyringSeed := newTestIdentityBytes(t)
	seedKeyringAt(t, filepath.Join(tmpDir, "keys"), keyringSeed)

	plaintextID, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	plaintextFull := plaintextID
	require.NoError(t, os.WriteFile(
		filepath.Join(storePath, "defra_identity.key"),
		[]byte(hex.EncodeToString(plaintextFull.PrivateKey().Raw())),
		0o600,
	))

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: testKeyringSecretAlt,
			Store:         config.DefraDBStoreConfig{Path: storePath},
		},
	}

	// The keyring entry wins; the plaintext file is never imported over it.
	kr, err := OpenKeyring(cfg)
	require.NoError(t, err)
	loaded, err := LoadIdentityFromKeyring(kr)
	require.NoError(t, err)
	keyringFull, err := LoadIdentityFromBytes(keyringSeed)
	require.NoError(t, err)
	loadedFull, ok := loaded.(identity.FullIdentity)
	require.True(t, ok)
	assert.Equal(t, keyringFull.PublicKey().String(), loadedFull.PublicKey().String())
}

func TestFullIdentityFromHexFile_MissingFile(t *testing.T) {
	_, err := fullIdentityFromHexFile(filepath.Join(t.TempDir(), "missing.key"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read key file")
}

func TestFullIdentityFromHexFile_InvalidHex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defra_identity.key")
	require.NoError(t, os.WriteFile(path, []byte("not-valid-hex!"), 0o600))
	_, err := fullIdentityFromHexFile(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode key hex")
}

func TestFullIdentityFromHexFile_InvalidKeyData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defra_identity.key")
	require.NoError(t, os.WriteFile(path, []byte("deadbeef"), 0o600))
	_, err := fullIdentityFromHexFile(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reconstruct private key")
}

func TestFullIdentityFromHexFile_ValidKeyFile(t *testing.T) {
	id, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	full := id

	path := filepath.Join(t.TempDir(), "defra_identity.key")
	require.NoError(t, os.WriteFile(path, []byte(hex.EncodeToString(full.PrivateKey().Raw())), 0o600))

	loaded, err := fullIdentityFromHexFile(path)
	assert.NoError(t, err)
	require.NotNil(t, loaded)
}

// ─── Node-level migration test ──────────────────────────────────────────────

func TestStartDefraAfterKeyringMigrationKeepsPeerID(t *testing.T) {
	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.URL = testURL
	tmpDir := t.TempDir()
	testConfig.DefraDB.Store.Path = filepath.Join(tmpDir, "store")
	testConfig.DefraDB.KeyringSecret = testKeyringSecretAlt

	seed := newTestIdentityBytes(t)
	seedKeyringAt(t, filepath.Join(testConfig.DefraDB.Store.Path, "keys"), seed)

	node, _, err := StartDefraInstance(&testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)

	peerInfo, err := node.DB.PeerInfo(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, peerInfo)
	originalPeerID := peerInfo[0]
	require.NoError(t, node.Close(context.Background()))

	// The legacy keyring was migrated beside the store.
	migrated, err := os.ReadFile(filepath.Join(tmpDir, "keys", NodeIdentityKeyName))
	require.NoError(t, err)
	assert.NotEmpty(t, migrated)

	// A restart reads the migrated keyring and keeps the same peer ID.
	node, _, err = StartDefraInstance(&testConfig, &MockSchemaApplierThatSucceeds{}, nil, nil)
	require.NoError(t, err)
	defer func() { _ = node.Close(context.Background()) }()

	newPeerInfo, err := node.DB.PeerInfo(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, newPeerInfo)
	require.Equal(t, originalPeerID, newPeerInfo[0])
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

func TestClientApplyCollectionSchemas(t *testing.T) {
	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.URL = testURL
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	client, err := NewClient(&testConfig)
	require.NoError(t, err)

	ctx := context.Background()

	err = client.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Stop(ctx) }()

	err = client.ApplyCollectionSchemas(ctx, evm.NewCollectionNames(evm.DefaultCollectionPrefix))
	require.NoError(t, err)

	for _, typeName := range evm.DefaultCollections() {
		result := client.GetNode().DB.ExecRequest(ctx,
			`query { __type(name: "`+typeName+`") { name } }`)
		assert.Empty(t, result.GQL.Errors, "should find collection %s without errors", typeName)
	}
}

func TestClientApplyCollectionSchemasWithCustomPrefix(t *testing.T) {
	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.URL = testURL
	testConfig.DefraDB.Store.Path = t.TempDir()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	client, err := NewClient(&testConfig)
	require.NoError(t, err)

	ctx := context.Background()

	err = client.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Stop(ctx) }()

	err = client.ApplyCollectionSchemas(ctx, evm.NewCollectionNames("Arbitrum__Mainnet"))
	require.NoError(t, err)

	collections := evm.NewCollectionNames("Arbitrum__Mainnet")
	for _, typeName := range collections.AllCollections() {
		result := client.GetNode().DB.ExecRequest(ctx,
			`query { __type(name: "`+typeName+`") { name } }`)
		assert.Empty(t, result.GQL.Errors, "should find collection %s without errors", typeName)
	}
}

func TestClientApplyCollectionSchemasBeforeStartFails(t *testing.T) {
	testConfig := *NewDefaultConfig()
	testConfig.DefraDB.KeyringSecret = testKeyringSecret

	client, err := NewClient(&testConfig)
	require.NoError(t, err)

	ctx := context.Background()

	err = client.ApplyCollectionSchemas(ctx, evm.NewCollectionNames(evm.DefaultCollectionPrefix))
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be started")
}
