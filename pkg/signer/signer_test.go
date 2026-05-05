package signer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	pb "github.com/libp2p/go-libp2p/core/crypto/pb"
	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/defradb"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/keyring"
	"github.com/sourcenetwork/defradb/node"
	"github.com/sourcenetwork/immutable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockKeyring implements keyring.Keyring for testing.
type mockKeyring struct {
	data   map[string][]byte
	getErr error
}

func (m *mockKeyring) Get(name string) ([]byte, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	v, ok := m.data[name]
	if !ok {
		return nil, keyring.ErrNotFound
	}
	return v, nil
}

func (m *mockKeyring) Set(name string, data []byte) error {
	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	m.data[name] = data
	return nil
}

func (m *mockKeyring) Delete(name string) error {
	delete(m.data, name)
	return nil
}

func (m *mockKeyring) List() ([]string, error) {
	keys := make([]string, 0, len(m.data))
	for k := range m.data {
		keys = append(keys, k)
	}
	return keys, nil
}

// mockPrivateKey implements crypto.PrivateKey for testing edge cases.
type mockPrivateKey struct {
	rawBytes []byte
	signErr  error
	signData []byte
	pubKey   crypto.PublicKey
}

func (m *mockPrivateKey) Equal(crypto.Key) bool       { return false }
func (m *mockPrivateKey) Raw() []byte                 { return m.rawBytes }
func (m *mockPrivateKey) String() string              { return "mock-private-key" }
func (m *mockPrivateKey) Type() crypto.KeyType        { return crypto.KeyTypeSecp256k1 }
func (m *mockPrivateKey) Underlying() any             { return nil }
func (m *mockPrivateKey) GetPublic() crypto.PublicKey { return m.pubKey }
func (m *mockPrivateKey) Sign(data []byte) ([]byte, error) {
	if m.signErr != nil {
		return nil, m.signErr
	}
	return m.signData, nil
}

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

// mockFullIdentity implements identity.FullIdentity for testing.
type mockFullIdentity struct {
	privKey crypto.PrivateKey
	pubKey  crypto.PublicKey
}

func (m *mockFullIdentity) PublicKey() crypto.PublicKey { return m.pubKey }
func (m *mockFullIdentity) DID() string                 { return "did:key:mock" }
func (m *mockFullIdentity) ToPublicRawIdentity() identity.PublicRawIdentity {
	return identity.PublicRawIdentity{}
}
func (m *mockFullIdentity) BearerToken() string           { return "" }
func (m *mockFullIdentity) PrivateKey() crypto.PrivateKey { return m.privKey }
func (m *mockFullIdentity) IntoRawIdentity() identity.RawIdentity {
	return identity.RawIdentity{}
}
func (m *mockFullIdentity) NewToken(
	duration time.Duration,
	audience immutable.Option[string],
	authorizedAccount immutable.Option[string],
) ([]byte, error) {
	return nil, nil
}
func (m *mockFullIdentity) SetBearerToken(token string) {}
func (m *mockFullIdentity) UpdateToken(
	duration time.Duration,
	audience immutable.Option[string],
	authorizedAccount immutable.Option[string],
) error {
	return nil
}

// mockIdentityNotFull implements identity.Identity but NOT FullIdentity.
type mockIdentityNotFull struct{}

func (m *mockIdentityNotFull) PublicKey() crypto.PublicKey { return nil }
func (m *mockIdentityNotFull) DID() string                 { return "did:key:mock" }
func (m *mockIdentityNotFull) ToPublicRawIdentity() identity.PublicRawIdentity {
	return identity.PublicRawIdentity{}
}

// mockLibP2PPrivKey implements libp2pcrypto.PrivKey for testing.
type mockLibP2PPrivKey struct {
	rawBytes []byte
	rawErr   error
	pubKey   libp2pcrypto.PubKey
}

func (m *mockLibP2PPrivKey) Type() pb.KeyType                 { return pb.KeyType_Ed25519 }
func (m *mockLibP2PPrivKey) Raw() ([]byte, error)             { return m.rawBytes, m.rawErr }
func (m *mockLibP2PPrivKey) Equals(libp2pcrypto.Key) bool     { return false }
func (m *mockLibP2PPrivKey) Sign(data []byte) ([]byte, error) { return nil, nil }
func (m *mockLibP2PPrivKey) GetPublic() libp2pcrypto.PubKey   { return m.pubKey }

// mockLibP2PPubKey implements libp2pcrypto.PubKey for testing.
type mockLibP2PPubKey struct {
	rawBytes []byte
	rawErr   error
}

func (m *mockLibP2PPubKey) Type() pb.KeyType                             { return pb.KeyType_Ed25519 }
func (m *mockLibP2PPubKey) Raw() ([]byte, error)                         { return m.rawBytes, m.rawErr }
func (m *mockLibP2PPubKey) Equals(libp2pcrypto.Key) bool                 { return false }
func (m *mockLibP2PPubKey) Verify(data []byte, sig []byte) (bool, error) { return false, nil }

// swapLoadIdentity replaces the identity loader and restores it on cleanup.
func swapLoadIdentity(t *testing.T, fn func(*config.Config, string) (identity.FullIdentity, error)) {
	t.Helper()
	orig := loadIdentityFromStoreFn
	loadIdentityFromStoreFn = fn
	t.Cleanup(func() { loadIdentityFromStoreFn = orig })
}

// swapCreateLibP2PKey replaces the libp2p key creator and restores it on cleanup.
func swapCreateLibP2PKey(t *testing.T, fn func(identity.Identity) (libp2pcrypto.PrivKey, error)) {
	t.Helper()
	orig := createLibP2PKeyFromIdentFn
	createLibP2PKeyFromIdentFn = fn
	t.Cleanup(func() { createLibP2PKeyFromIdentFn = orig })
}

// setupTestNode creates a DefraDB node with keyring for testing
func setupTestNode(t *testing.T) (*node.Node, *config.Config) {
	testConfig := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url:           "http://localhost:0",
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

// ─── openKeyring tests ──────────────────────────────────────────────────────

func TestOpenKeyring_NilConfig(t *testing.T) {
	kr, err := openKeyring(nil)
	assert.NoError(t, err)
	assert.Nil(t, kr)
}

func TestOpenKeyring_EmptySecret(t *testing.T) {
	kr, err := openKeyring(&config.Config{})
	assert.NoError(t, err)
	assert.Nil(t, kr)
}

func TestOpenKeyring_WithStorePath(t *testing.T) {
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret",
			Store:         config.DefraDBStoreConfig{Path: t.TempDir()},
		},
	}
	kr, err := openKeyring(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, kr)
}

func TestOpenKeyring_EmptyStorePath(t *testing.T) {
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret",
			Store:         config.DefraDBStoreConfig{Path: ""},
		},
	}
	kr, err := openKeyring(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, kr)
	os.RemoveAll("keys")
}

func TestOpenKeyring_MkdirAllFails(t *testing.T) {
	tmpDir := t.TempDir()
	conflictPath := filepath.Join(tmpDir, "notadir")
	os.WriteFile(conflictPath, []byte("block"), 0644)

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret",
			Store:         config.DefraDBStoreConfig{Path: conflictPath},
		},
	}
	kr, err := openKeyring(cfg)
	assert.Error(t, err)
	assert.Nil(t, kr)
	assert.Contains(t, err.Error(), "failed to create keyring directory")
}

// ─── loadIdentityFromKeyring tests ──────────────────────────────────────────

func TestLoadIdentityFromKeyring_ErrNotFound(t *testing.T) {
	kr := &mockKeyring{data: map[string][]byte{}}
	_, err := loadIdentityFromKeyring(kr)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "node identity not found in keyring")
}

func TestLoadIdentityFromKeyring_GenericError(t *testing.T) {
	kr := &mockKeyring{getErr: errors.New("disk failure")}
	_, err := loadIdentityFromKeyring(kr)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get identity from keyring")
}

func TestLoadIdentityFromKeyring_OldFormatWithoutColon(t *testing.T) {
	// Build raw key bytes guaranteed to NOT contain 0x3A (':')
	// Generate keys until we find one without ':' or construct one
	var keyBytes []byte
	for i := 0; i < 100; i++ {
		ident, err := identity.Generate(crypto.KeyTypeSecp256k1)
		require.NoError(t, err)
		keyBytes = ident.PrivateKey().Raw()
		hasColon := false
		for _, b := range keyBytes {
			if b == ':' {
				hasColon = true
				break
			}
		}
		if !hasColon {
			break
		}
	}

	kr := &mockKeyring{data: map[string][]byte{
		nodeIdentityKeyName: keyBytes,
	}}
	loaded, err := loadIdentityFromKeyring(kr)
	assert.NoError(t, err)
	assert.NotNil(t, loaded)
}

func TestLoadIdentityFromKeyring_InvalidKeyBytes(t *testing.T) {
	data := append([]byte(string(crypto.KeyTypeSecp256k1)+":"), []byte("bad")...)
	kr := &mockKeyring{data: map[string][]byte{
		nodeIdentityKeyName: data,
	}}
	_, err := loadIdentityFromKeyring(kr)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reconstruct private key")
}

func TestLoadIdentityFromKeyring_InvalidKeyType(t *testing.T) {
	data := append([]byte("invalidtype:"), []byte("somebytes1234567890123456789012")...)
	kr := &mockKeyring{data: map[string][]byte{
		nodeIdentityKeyName: data,
	}}
	_, err := loadIdentityFromKeyring(kr)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reconstruct private key")
}

// ─── loadIdentityFromFile tests ─────────────────────────────────────────────

func TestLoadIdentityFromFile_NotFound(t *testing.T) {
	_, err := loadIdentityFromFile(t.TempDir())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read key file")
}

func TestLoadIdentityFromFile_InvalidHex(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, keyFileName), []byte("not-valid-hex!"), 0644)
	_, err := loadIdentityFromFile(tmpDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode key hex")
}

func TestLoadIdentityFromFile_InvalidKeyData(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, keyFileName), []byte("deadbeef"), 0644)
	_, err := loadIdentityFromFile(tmpDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reconstruct private key")
}

func TestLoadIdentityFromFile_ValidKeyFile(t *testing.T) {
	ident, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	keyBytes := ident.PrivateKey().Raw()

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, keyFileName), []byte(hex.EncodeToString(keyBytes)), 0644)

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
	os.MkdirAll(".defra", 0755)
	os.WriteFile(filepath.Join(".defra", keyFileName), []byte("dummy"), 0644)
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

// ─── createLibP2PKeyFromIdentity tests ──────────────────────────────────────

func TestCreateLibP2PKeyFromIdentity_NotFullIdentity(t *testing.T) {
	_, err := createLibP2PKeyFromIdentity(&mockIdentityNotFull{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "identity is not a FullIdentity")
}

func TestCreateLibP2PKeyFromIdentity_NilPrivateKey(t *testing.T) {
	mock := &mockFullIdentity{privKey: nil}
	_, err := createLibP2PKeyFromIdentity(mock)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get private key from identity")
}

func TestCreateLibP2PKeyFromIdentity_EmptyRawBytes(t *testing.T) {
	mock := &mockFullIdentity{privKey: &mockPrivateKey{rawBytes: []byte{}}}
	_, err := createLibP2PKeyFromIdentity(mock)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "private key has no raw bytes")
}

func TestCreateLibP2PKeyFromIdentity_WrongKeyLength(t *testing.T) {
	mock := &mockFullIdentity{privKey: &mockPrivateKey{rawBytes: make([]byte, 16)}}
	_, err := createLibP2PKeyFromIdentity(mock)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected 32-byte secp256k1 key, got 16 bytes")
}

func TestCreateLibP2PKeyFromIdentity_ValidKey(t *testing.T) {
	// 32-byte key should work
	mock := &mockFullIdentity{privKey: &mockPrivateKey{rawBytes: make([]byte, 32)}}
	key, err := createLibP2PKeyFromIdentity(mock)
	assert.NoError(t, err)
	assert.NotNil(t, key)
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
		return &mockFullIdentity{privKey: nil}, nil
	})
	_, err := SignWithDefraKeys("msg", nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "identity does not have a private key")
}

func TestSignWithDefraKeys_SignError(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return &mockFullIdentity{
			privKey: &mockPrivateKey{
				rawBytes: make([]byte, 32),
				signErr:  fmt.Errorf("signing hardware failure"),
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

func TestSignWithP2PKeys_CreateLibP2PKeyFails(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return &mockFullIdentity{privKey: &mockPrivateKey{rawBytes: make([]byte, 32)}}, nil
	})
	swapCreateLibP2PKey(t, func(_ identity.Identity) (libp2pcrypto.PrivKey, error) {
		return nil, fmt.Errorf("injected key error")
	})
	_, err := SignWithP2PKeys("msg", nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create LibP2P key from identity")
}

func TestSignWithP2PKeys_RawKeyError(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return &mockFullIdentity{privKey: &mockPrivateKey{rawBytes: make([]byte, 32)}}, nil
	})
	swapCreateLibP2PKey(t, func(_ identity.Identity) (libp2pcrypto.PrivKey, error) {
		return &mockLibP2PPrivKey{rawErr: fmt.Errorf("raw error")}, nil
	})
	_, err := SignWithP2PKeys("msg", nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get raw key bytes from LibP2P key")
}

func TestSignWithP2PKeys_UnexpectedKeyLength(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return &mockFullIdentity{privKey: &mockPrivateKey{rawBytes: make([]byte, 32)}}, nil
	})
	swapCreateLibP2PKey(t, func(_ identity.Identity) (libp2pcrypto.PrivKey, error) {
		return &mockLibP2PPrivKey{rawBytes: make([]byte, 48)}, nil
	})
	_, err := SignWithP2PKeys("msg", nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected Ed25519 key length")
}

func TestSignWithP2PKeys_32ByteKey(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return &mockFullIdentity{privKey: &mockPrivateKey{rawBytes: make([]byte, 32)}}, nil
	})
	// Return a 32-byte raw key (the seed-only path)
	seed := make([]byte, 32)
	seed[0] = 0x01
	swapCreateLibP2PKey(t, func(_ identity.Identity) (libp2pcrypto.PrivKey, error) {
		return &mockLibP2PPrivKey{rawBytes: seed}, nil
	})
	sig, err := SignWithP2PKeys("msg", nil, cfgWithStorePath(t.TempDir()))
	assert.NoError(t, err)
	assert.NotEmpty(t, sig)
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
		return &mockFullIdentity{pubKey: nil}, nil
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

func TestGetP2PPublicKey_CreateLibP2PKeyFails(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return &mockFullIdentity{privKey: &mockPrivateKey{rawBytes: make([]byte, 32)}}, nil
	})
	swapCreateLibP2PKey(t, func(_ identity.Identity) (libp2pcrypto.PrivKey, error) {
		return nil, fmt.Errorf("injected key error")
	})
	_, err := GetP2PPublicKey(nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create LibP2P key from identity")
}

func TestGetP2PPublicKey_RawPubKeyError(t *testing.T) {
	swapLoadIdentity(t, func(_ *config.Config, _ string) (identity.FullIdentity, error) {
		return &mockFullIdentity{privKey: &mockPrivateKey{rawBytes: make([]byte, 32)}}, nil
	})
	swapCreateLibP2PKey(t, func(_ identity.Identity) (libp2pcrypto.PrivKey, error) {
		return &mockLibP2PPrivKey{
			rawBytes: make([]byte, 64),
			pubKey:   &mockLibP2PPubKey{rawErr: fmt.Errorf("raw pub error")},
		}, nil
	})
	_, err := GetP2PPublicKey(nil, cfgWithStorePath(t.TempDir()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get raw public key bytes")
}

// ─── loadIdentityFromKeyring: identity.FromPrivateKey error ─────────────────

func TestLoadIdentityFromKeyring_FromPrivateKeyFails(t *testing.T) {
	orig := identityFromPrivateKeyFn
	identityFromPrivateKeyFn = func(_ crypto.PrivateKey) (identity.FullIdentity, error) {
		return nil, fmt.Errorf("injected DID error")
	}
	t.Cleanup(func() { identityFromPrivateKeyFn = orig })

	ident, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	raw := ident.PrivateKey().Raw()
	data := append([]byte(string(crypto.KeyTypeSecp256k1)+":"), raw...)

	kr := &mockKeyring{data: map[string][]byte{nodeIdentityKeyName: data}}
	_, err = loadIdentityFromKeyring(kr)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reconstruct identity from private key")
}

// ─── loadIdentityFromFile: identity.FromPrivateKey error ────────────────────

func TestLoadIdentityFromFile_FromPrivateKeyFails(t *testing.T) {
	orig := identityFromPrivateKeyFn
	identityFromPrivateKeyFn = func(_ crypto.PrivateKey) (identity.FullIdentity, error) {
		return nil, fmt.Errorf("injected DID error")
	}
	t.Cleanup(func() { identityFromPrivateKeyFn = orig })

	ident, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	keyBytes := ident.PrivateKey().Raw()

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, keyFileName), []byte(hex.EncodeToString(keyBytes)), 0644)

	_, err = loadIdentityFromFile(tmpDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reconstruct identity from private key")
}

// ─── createLibP2PKeyFromIdentity: GenerateEd25519Key error ──────────────────

func TestCreateLibP2PKeyFromIdentity_GenerateEd25519Fails(t *testing.T) {
	orig := generateEd25519KeyFn
	generateEd25519KeyFn = func(r io.Reader) (libp2pcrypto.PrivKey, libp2pcrypto.PubKey, error) {
		return nil, nil, fmt.Errorf("injected keygen error")
	}
	t.Cleanup(func() { generateEd25519KeyFn = orig })

	mock := &mockFullIdentity{privKey: &mockPrivateKey{rawBytes: make([]byte, 32)}}
	_, err := createLibP2PKeyFromIdentity(mock)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to generate Ed25519 key from identity seed")
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