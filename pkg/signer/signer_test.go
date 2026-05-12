package signer

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/defradb"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPublicKey implements crypto.PublicKey for testing edge cases.
type mockPublicKey struct {
	rawBytes []byte
}

func (m *mockPublicKey) Equal(crypto.Key) bool               { return false }
func (m *mockPublicKey) Raw() []byte                         { return m.rawBytes }
func (m *mockPublicKey) String() string                      { return "mock-public-key" }
func (m *mockPublicKey) Type() crypto.KeyType                { return crypto.KeyTypeSecp256k1 }
func (m *mockPublicKey) Underlying() any                     { return nil }
func (m *mockPublicKey) Verify([]byte, []byte) (bool, error) { return false, nil }
func (m *mockPublicKey) DID() (string, error)                { return "", nil }

// swapLoadIdentity replaces the identity loader and restores it on cleanup.
func swapLoadIdentity(t *testing.T, fn func(*config.Config, string) (identity.FullIdentity, error)) {
	t.Helper()
	orig := loadIdentityFromStoreFn
	loadIdentityFromStoreFn = fn
	t.Cleanup(func() { loadIdentityFromStoreFn = orig })
}

// setupTestNode creates a DefraDB node with keyring for testing.
func setupTestNode(t *testing.T) (*node.Node, *config.Config) {
	testConfig := &config.Config{
		DefraDB: config.DefraDBConfig{
			URL:           "http://localhost:0",
			KeyringSecret: "test-secret",
			P2P: config.DefraDBP2PConfig{
				BootstrapPeers: []string{},
				ListenAddr:     "/ip4/0.0.0.0/tcp/0",
			},
			Store: config.DefraDBStoreConfig{
				Path: t.TempDir(),
			},
		},
		Logger: config.LoggerConfig{
			Development: true,
		},
	}

	schemaApplier := defradb.NewSchemaApplierFromProvidedSchema(`
		type User {
			name: String
		}
	`)

	defraNode, _, err := defradb.StartDefraInstance(testConfig, schemaApplier, nil, nil)
	require.NoError(t, err)

	return defraNode, testConfig
}

// cfgWithStorePath returns a minimal config pointing to the given store path.
func cfgWithStorePath(path string) *config.Config {
	return &config.Config{
		DefraDB: config.DefraDBConfig{
			Store: config.DefraDBStoreConfig{Path: path},
		},
	}
}

// ─── Integration tests ───────────────────────────────────────────────────────

func TestSignWithDefraKeys(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	signature, err := SignWithDefraKeys("Hello, World!", defraNode, cfg)
	require.NoError(t, err)
	require.NotEmpty(t, signature)
}

func TestSignWithP2PKeys(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	signature, err := SignWithP2PKeys("Hello, World!", defraNode, cfg)
	require.NoError(t, err)
	require.NotEmpty(t, signature)
}

func TestGetDefraPublicKey(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	publicKey, err := GetDefraPublicKey(defraNode, cfg)
	require.NoError(t, err)
	require.NotEmpty(t, publicKey)
}

func TestGetP2PPublicKey(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	publicKey, err := GetP2PPublicKey(defraNode, cfg)
	require.NoError(t, err)
	require.NotEmpty(t, publicKey)
}

func TestSignAndVerifyDefraSignature(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	message := "Test message for DefraDB signature"

	signature, err := SignWithDefraKeys(message, defraNode, cfg)
	require.NoError(t, err)

	publicKey, err := GetDefraPublicKey(defraNode, cfg)
	require.NoError(t, err)

	err = VerifyDefraSignature(publicKey, message, signature)
	require.NoError(t, err)

	err = VerifyDefraSignature(publicKey, "wrong message", signature)
	require.Error(t, err)
	require.Contains(t, err.Error(), "signature verification failed")

	sigBytes, _ := hex.DecodeString(signature)
	sigBytes[len(sigBytes)/2] ^= 0xFF
	err = VerifyDefraSignature(publicKey, message, hex.EncodeToString(sigBytes))
	require.Error(t, err)
}

func TestSignAndVerifyP2PSignature(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	message := "Test message for P2P signature"

	signature, err := SignWithP2PKeys(message, defraNode, cfg)
	require.NoError(t, err)

	publicKey, err := GetP2PPublicKey(defraNode, cfg)
	require.NoError(t, err)

	err = VerifyP2PSignature(publicKey, message, signature)
	require.NoError(t, err)

	err = VerifyP2PSignature(publicKey, "wrong message", signature)
	require.Error(t, err)
	require.Contains(t, err.Error(), "signature verification failed")

	sigBytes, _ := hex.DecodeString(signature)
	sigBytes[len(sigBytes)/2] ^= 0xFF
	err = VerifyP2PSignature(publicKey, message, hex.EncodeToString(sigBytes))
	require.Error(t, err)
}

func TestVerifyDefraSignature_InvalidInputs(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	publicKey, err := GetDefraPublicKey(defraNode, cfg)
	require.NoError(t, err)

	tests := []struct {
		name   string
		pubKey string
		msg    string
		sig    string
		errSub string
	}{
		{"invalid pubkey hex", "deadbeef", "msg", "3006020101020101", "failed to parse public key"},
		{"invalid sig hex", publicKey, "msg", "invalid-hex", "failed to decode signature hex"},
		{"empty pubkey", "", "msg", "3006020101020101", ""},
		{"empty sig", publicKey, "msg", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyDefraSignature(tt.pubKey, tt.msg, tt.sig)
			assert.Error(t, err)
			if tt.errSub != "" {
				assert.Contains(t, err.Error(), tt.errSub)
			}
		})
	}
}

func TestVerifyP2PSignature_InvalidInputs(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	publicKey, err := GetP2PPublicKey(defraNode, cfg)
	require.NoError(t, err)

	tests := []struct {
		name   string
		pubKey string
		msg    string
		sig    string
		errSub string
	}{
		{"invalid pubkey", "deadbeef", "msg", "0000000000000000000000000000000000000000000000000000000000000000", "failed to parse public key"},
		{"invalid sig hex", publicKey, "msg", "invalid-hex", "failed to decode signature hex"},
		{"empty pubkey", "", "msg", "aabb", ""},
		{"empty sig", publicKey, "msg", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyP2PSignature(tt.pubKey, tt.msg, tt.sig)
			assert.Error(t, err)
			if tt.errSub != "" {
				assert.Contains(t, err.Error(), tt.errSub)
			}
		})
	}
}

func TestSignWithDefraKeys_Consistency(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	msg := "Consistent message"
	sig1, err := SignWithDefraKeys(msg, defraNode, cfg)
	require.NoError(t, err)
	sig2, err := SignWithDefraKeys(msg, defraNode, cfg)
	require.NoError(t, err)
	require.Equal(t, sig1, sig2)
}

func TestSignWithP2PKeys_Consistency(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	msg := "Consistent message"
	sig1, err := SignWithP2PKeys(msg, defraNode, cfg)
	require.NoError(t, err)
	sig2, err := SignWithP2PKeys(msg, defraNode, cfg)
	require.NoError(t, err)
	require.Equal(t, sig1, sig2)
}

func TestPublicKeyConsistency(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	pk1, _ := GetDefraPublicKey(defraNode, cfg)
	pk2, _ := GetDefraPublicKey(defraNode, cfg)
	require.Equal(t, pk1, pk2)

	p2p1, _ := GetP2PPublicKey(defraNode, cfg)
	p2p2, _ := GetP2PPublicKey(defraNode, cfg)
	require.Equal(t, p2p1, p2p2)
}

func TestCrossKeyVerification(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	msg := "Test message"
	defraSig, _ := SignWithDefraKeys(msg, defraNode, cfg)
	defraPub, _ := GetDefraPublicKey(defraNode, cfg)
	p2pSig, _ := SignWithP2PKeys(msg, defraNode, cfg)
	p2pPub, _ := GetP2PPublicKey(defraNode, cfg)

	// Cross-verification should fail
	assert.Error(t, VerifyP2PSignature(p2pPub, msg, defraSig))
	assert.Error(t, VerifyDefraSignature(defraPub, msg, p2pSig))
	// Correct verification should pass
	assert.NoError(t, VerifyDefraSignature(defraPub, msg, defraSig))
	assert.NoError(t, VerifyP2PSignature(p2pPub, msg, p2pSig))
}

func TestSignWithDefraKeys_EmptyMessage(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	sig, err := SignWithDefraKeys("", defraNode, cfg)
	require.NoError(t, err)
	pub, _ := GetDefraPublicKey(defraNode, cfg)
	assert.NoError(t, VerifyDefraSignature(pub, "", sig))
}

func TestSignWithP2PKeys_EmptyMessage(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	sig, err := SignWithP2PKeys("", defraNode, cfg)
	require.NoError(t, err)
	pub, _ := GetP2PPublicKey(defraNode, cfg)
	assert.NoError(t, VerifyP2PSignature(pub, "", sig))
}

func TestSignWithDefraKeys_LongMessage(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	longMsg := make([]byte, 10000)
	for i := range longMsg {
		longMsg[i] = byte(i % 256)
	}
	sig, err := SignWithDefraKeys(string(longMsg), defraNode, cfg)
	require.NoError(t, err)
	pub, _ := GetDefraPublicKey(defraNode, cfg)
	assert.NoError(t, VerifyDefraSignature(pub, string(longMsg), sig))
}

func TestSignWithP2PKeys_LongMessage(t *testing.T) {
	defraNode, cfg := setupTestNode(t)
	defer defraNode.Close(context.Background())

	longMsg := make([]byte, 10000)
	for i := range longMsg {
		longMsg[i] = byte(i % 256)
	}
	sig, err := SignWithP2PKeys(string(longMsg), defraNode, cfg)
	require.NoError(t, err)
	pub, _ := GetP2PPublicKey(defraNode, cfg)
	assert.NoError(t, VerifyP2PSignature(pub, string(longMsg), sig))
}

// ─── loadIdentityFromFile tests ─────────────────────────────────────────────

func TestLoadIdentityFromFile_NotFound(t *testing.T) {
	_, err := loadIdentityFromFile(t.TempDir())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read key file")
}

func TestLoadIdentityFromFile_InvalidHex(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, keyFileName), []byte("not-valid-hex!"), 0o644)
	_, err := loadIdentityFromFile(tmpDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode key hex")
}

func TestLoadIdentityFromFile_InvalidKeyData(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, keyFileName), []byte("deadbeef"), 0o644)
	_, err := loadIdentityFromFile(tmpDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reconstruct private key")
}

func TestLoadIdentityFromFile_ValidKeyFile(t *testing.T) {
	ident, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	keyBytes := ident.PrivateKey().Raw()

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, keyFileName), []byte(hex.EncodeToString(keyBytes)), 0o644)

	loaded, err := loadIdentityFromFile(tmpDir)
	assert.NoError(t, err)
	assert.NotNil(t, loaded)
}

// ─── getStorePath tests ─────────────────────────────────────────────────────

func TestGetStorePath_WithConfig(t *testing.T) {
	tmpDir := t.TempDir()
	path, err := getStorePath(nil, cfgWithStorePath(tmpDir))
	assert.NoError(t, err)
	assert.Equal(t, tmpDir, path)
}

func TestGetStorePath_NilConfig(t *testing.T) {
	_, err := getStorePath(nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "could not find defra_identity.key")
}

func TestGetStorePath_EmptyConfigPath(t *testing.T) {
	_, err := getStorePath(nil, cfgWithStorePath(""))
	assert.Error(t, err)
}

func TestGetStorePath_FindsKeyInCommonLocation(t *testing.T) {
	os.MkdirAll(".defra", 0o755)
	os.WriteFile(filepath.Join(".defra", keyFileName), []byte("dummy"), 0o644)
	defer os.RemoveAll(".defra")

	path, err := getStorePath(nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, ".defra", path)
}

// ─── loadIdentityFromStore tests ────────────────────────────────────────────

func TestLoadIdentityFromStore_NilConfig(t *testing.T) {
	_, err := loadIdentityFromStore(nil, t.TempDir())
	assert.Error(t, err)
}

// ─── No store path error tests ──────────────────────────────────────────────

func TestSignWithDefraKeys_NoStorePath(t *testing.T) {
	_, err := SignWithDefraKeys("test", nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get store path")
}

func TestSignWithP2PKeys_NoStorePath(t *testing.T) {
	_, err := SignWithP2PKeys("test", nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get store path")
}

func TestGetDefraPublicKey_NoStorePath(t *testing.T) {
	_, err := GetDefraPublicKey(nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get store path")
}

func TestGetP2PPublicKey_NoStorePath(t *testing.T) {
	_, err := GetP2PPublicKey(nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get store path")
}

// ─── SignWithDefraKeys error paths via mocked loader ────────────────────────

func TestSignWithDefraKeys_LoadIdentityFails(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return nil, fmt.Errorf("injected load error")
	})
	_, err := SignWithDefraKeys("msg", nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load identity")
}

func TestSignWithDefraKeys_NilPrivateKey(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return &testutils.MockFullIdentity{PrivKey: nil}, nil
	})
	_, err := SignWithDefraKeys("msg", nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "identity does not have a private key")
}

func TestSignWithDefraKeys_SignError(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return &testutils.MockFullIdentity{
			PrivKey: &testutils.MockPrivateKey{
				RawBytes: make([]byte, 32),
				SignErr:  fmt.Errorf("signing hardware failure"),
			},
		}, nil
	})
	_, err := SignWithDefraKeys("msg", nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to sign message")
}

// ─── SignWithP2PKeys error paths via mocked loader/creator ──────────────────

func TestSignWithP2PKeys_LoadIdentityFails(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return nil, fmt.Errorf("injected load error")
	})
	_, err := SignWithP2PKeys("msg", nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load identity")
}

// ─── GetDefraPublicKey error paths ──────────────────────────────────────────

func TestGetDefraPublicKey_LoadIdentityFails(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return nil, fmt.Errorf("injected load error")
	})
	_, err := GetDefraPublicKey(nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load identity")
}

func TestGetDefraPublicKey_NilPublicKey(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return &testutils.MockFullIdentity{PubKey: nil}, nil
	})
	_, err := GetDefraPublicKey(nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "identity does not have a public key")
}

// ─── GetP2PPublicKey error paths ────────────────────────────────────────────

func TestGetP2PPublicKey_LoadIdentityFails(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return nil, fmt.Errorf("injected load error")
	})
	_, err := GetP2PPublicKey(nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load identity")
}

// ─── VerifyP2PSignature: non-Ed25519 underlying key ────────────────────────

func TestVerifyP2PSignature_NotEd25519Underlying(t *testing.T) {
	orig := ed25519PubKeyFromStringFn
	ed25519PubKeyFromStringFn = func(_ string) (crypto.PublicKey, error) {
		// Return a mock whose Underlying() is not ed25519.PublicKey
		return &mockPublicKey{rawBytes: make([]byte, 32)}, nil
	}
	t.Cleanup(func() { ed25519PubKeyFromStringFn = orig })

	err := VerifyP2PSignature("aabb", "msg", "aabb")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "public key is not Ed25519")
}
