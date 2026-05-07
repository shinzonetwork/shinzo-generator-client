package signer

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/defradb"
	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/node"
)

const keyFileName string = "defra_identity.key"

// Function variables for dependency injection in tests.
var (
	loadIdentityFromStoreFn   = loadIdentityFromStore
	ed25519PubKeyFromStringFn = func(hex string) (crypto.PublicKey, error) {
		return crypto.PublicKeyFromString(crypto.KeyTypeEd25519, hex)
	}
)

// loadIdentityFromFile loads the DefraDB identity from a file.
func loadIdentityFromFile(storePath string) (identity.FullIdentity, error) {
	keyPath := filepath.Join(storePath, keyFileName)

	// Read the stored key file
	keyHex, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read key file at %s: %w", keyPath, err)
	}

	// Decode hex string to bytes
	keyBytes, err := hex.DecodeString(strings.TrimSpace(string(keyHex)))
	if err != nil {
		return nil, fmt.Errorf("failed to decode key hex: %w", err)
	}

	// Reconstruct private key from bytes
	privateKey, err := crypto.PrivateKeyFromBytes(crypto.KeyTypeSecp256k1, keyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to reconstruct private key: %w", err)
	}

	// Reconstruct identity from private key
	fullIdentity, err := identity.FromPrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to reconstruct identity from private key: %w", err)
	}

	return fullIdentity, nil
}

// loadIdentityFromStore loads the DefraDB identity, trying the keyring first
// (via pkg/defradb's shared helpers) and falling back to the file-based store
// if the keyring is unconfigured, missing the entry, or unreadable. The keyring
// path is the modern DefraDB default; the file fallback supports older
// installations that pre-date keyring storage
func loadIdentityFromStore(cfg *config.Config, storePath string) (identity.FullIdentity, error) {
	// Try keyring first if config is available
	if cfg != nil {
		if kr, err := defradb.OpenKeyring(cfg); err == nil {
			if id, err := defradb.LoadIdentityFromKeyring(kr); err == nil {
				if full, ok := id.(identity.FullIdentity); ok {
					return full, nil
				}
			}
		}
	}

	// Fall back to file-based storage
	return loadIdentityFromFile(storePath)
}

// getStorePath attempts to determine the store path from the config or node.
// If config is provided and has a store path, it uses that.
// Otherwise, it tries common locations and returns the first one that contains the key file.
func getStorePath(_ *node.Node, cfg *config.Config) (string, error) {
	// If config is provided and has a store path, use it
	if cfg != nil && cfg.DefraDB.Store.Path != "" {
		return cfg.DefraDB.Store.Path, nil
	}

	// Try common locations for file-based fallback
	possiblePaths := []string{
		".defra", // Default relative path
		filepath.Join(os.Getenv("HOME"), ".defradb"), // Default DefraDB path
	}

	// Try each path to see if it contains the key file
	for _, path := range possiblePaths {
		keyPath := filepath.Join(path, keyFileName)
		if _, err := os.Stat(keyPath); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("could not find defra_identity.key in any common location. Please ensure the key file exists or provide a store path in config")
}

// SignWithDefraKeys signs a message using the DefraDB identity's private key (secp256k1).
// The signature is returned as a hex-encoded string.
// If cfg is provided and has a KeyringSecret, it will use the keyring; otherwise falls back to file-based storage.
func SignWithDefraKeys(message string, defraNode *node.Node, cfg *config.Config) (string, error) {
	// Get the store path
	storePath, err := getStorePath(defraNode, cfg)
	if err != nil {
		return "", fmt.Errorf("failed to get store path: %w", err)
	}

	// Load the identity from storage (tries keyring first, then file)
	fullIdentity, err := loadIdentityFromStoreFn(cfg, storePath)
	if err != nil {
		return "", fmt.Errorf("failed to load identity: %w", err)
	}

	// Get the private key
	privateKey := fullIdentity.PrivateKey()
	if privateKey == nil {
		return "", fmt.Errorf("identity does not have a private key")
	}

	// Sign the message
	signature, err := privateKey.Sign([]byte(message))
	if err != nil {
		return "", fmt.Errorf("failed to sign message: %w", err)
	}

	// Return hex-encoded signature
	return hex.EncodeToString(signature), nil
}

// SignWithP2PKeys signs a message using the LibP2P private key (Ed25519) derived from the DefraDB identity.
// The signature is returned as a hex-encoded string.
// If cfg is provided and has a KeyringSecret, it will use the keyring; otherwise falls back to file-based storage.
func SignWithP2PKeys(message string, defraNode *node.Node, cfg *config.Config) (string, error) {
	// Get the store path
	storePath, err := getStorePath(defraNode, cfg)
	if err != nil {
		return "", fmt.Errorf("failed to get store path: %w", err)
	}

	// Load the identity from storage (tries keyring first, then file)
	fullIdentity, err := loadIdentityFromStoreFn(cfg, storePath)
	if err != nil {
		return "", fmt.Errorf("failed to load identity: %w", err)
	}

	// Create LibP2P private key from the identity
	libp2pPrivKey, err := defradb.CreateLibP2PKeyFromIdentity(fullIdentity)
	if err != nil {
		return "", fmt.Errorf("failed to create LibP2P key from identity: %w", err)
	}

	// Extract the raw Ed25519 key bytes from LibP2P key
	// LibP2P's Raw() may return 32 or 64 bytes depending on the key type
	rawKeyBytes, err := libp2pPrivKey.Raw()
	if err != nil {
		return "", fmt.Errorf("failed to get raw key bytes from LibP2P key: %w", err)
	}

	// Ed25519.NewKeyFromSeed expects exactly 32 bytes (the seed)
	// If we got 64 bytes, take only the first 32 bytes (the seed portion)
	var ed25519Seed []byte
	if len(rawKeyBytes) == 64 {
		ed25519Seed = rawKeyBytes[:32]
	} else if len(rawKeyBytes) == 32 {
		ed25519Seed = rawKeyBytes
	} else {
		return "", fmt.Errorf("unexpected Ed25519 key length: expected 32 or 64 bytes, got %d", len(rawKeyBytes))
	}

	// Construct full ed25519.PrivateKey from seed (64 bytes: 32-byte seed + 32-byte public key)
	ed25519PrivKey := ed25519.NewKeyFromSeed(ed25519Seed)

	// Sign the message directly with the Ed25519 private key, mirroring the working example
	signature := ed25519.Sign(ed25519PrivKey, []byte(message))

	// Return hex-encoded signature
	return hex.EncodeToString(signature), nil
}

// GetDefraPublicKey returns the DefraDB identity's public key as a hex-encoded string.
// If cfg is provided and has a KeyringSecret, it will use the keyring; otherwise falls back to file-based storage.
func GetDefraPublicKey(defraNode *node.Node, cfg *config.Config) (string, error) {
	// Get the store path
	storePath, err := getStorePath(defraNode, cfg)
	if err != nil {
		return "", fmt.Errorf("failed to get store path: %w", err)
	}

	// Load the identity from storage (tries keyring first, then file)
	fullIdentity, err := loadIdentityFromStoreFn(cfg, storePath)
	if err != nil {
		return "", fmt.Errorf("failed to load identity: %w", err)
	}

	// Get the public key
	publicKey := fullIdentity.PublicKey()
	if publicKey == nil {
		return "", fmt.Errorf("identity does not have a public key")
	}

	// Return hex-encoded public key
	return publicKey.String(), nil
}

// GetP2PPublicKey returns the LibP2P public key (Ed25519) derived from the DefraDB identity as a hex-encoded string.
// If cfg is provided and has a KeyringSecret, it will use the keyring; otherwise falls back to file-based storage.
func GetP2PPublicKey(defraNode *node.Node, cfg *config.Config) (string, error) {
	// Get the store path
	storePath, err := getStorePath(defraNode, cfg)
	if err != nil {
		return "", fmt.Errorf("failed to get store path: %w", err)
	}

	// Load the identity from storage (tries keyring first, then file)
	fullIdentity, err := loadIdentityFromStoreFn(cfg, storePath)
	if err != nil {
		return "", fmt.Errorf("failed to load identity: %w", err)
	}

	// Create LibP2P private key from the identity
	libp2pPrivKey, err := defradb.CreateLibP2PKeyFromIdentity(fullIdentity)
	if err != nil {
		return "", fmt.Errorf("failed to create LibP2P key from identity: %w", err)
	}

	// Get the public key from LibP2P key
	libp2pPubKey := libp2pPrivKey.GetPublic()

	// Extract the raw Ed25519 public key bytes
	ed25519PubKeyBytes, err := libp2pPubKey.Raw()
	if err != nil {
		return "", fmt.Errorf("failed to get raw public key bytes: %w", err)
	}

	// Return hex-encoded public key
	return hex.EncodeToString(ed25519PubKeyBytes), nil
}

// VerifyDefraSignature verifies a signature against a message using a DefraDB public key (secp256k1).
// Parameters:
//   - publicKeyHex: Hex-encoded secp256k1 public key
//   - message: The original message that was signed
//   - signatureHex: Hex-encoded DER signature
//
// Returns:
//   - error: nil if verification succeeds, appropriate error otherwise
func VerifyDefraSignature(publicKeyHex string, message string, signatureHex string) error {
	// Decode hex strings
	signatureBytes, err := hex.DecodeString(signatureHex)
	if err != nil {
		return fmt.Errorf("failed to decode signature hex: %w", err)
	}

	// Parse public key from hex string
	publicKey, err := crypto.PublicKeyFromString(crypto.KeyTypeSecp256k1, publicKeyHex)
	if err != nil {
		return fmt.Errorf("failed to parse public key: %w", err)
	}

	// Verify the signature using the public key's Verify method
	valid, err := publicKey.Verify([]byte(message), signatureBytes)
	if err != nil {
		return fmt.Errorf("failed to verify signature: %w", err)
	}

	if !valid {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}

// VerifyP2PSignature verifies a signature against a message using a LibP2P public key (Ed25519).
// Parameters:
//   - publicKeyHex: Hex-encoded Ed25519 public key
//   - message: The original message that was signed
//   - signatureHex: Hex-encoded Ed25519 signature
//
// Returns:
//   - error: nil if verification succeeds, appropriate error otherwise
func VerifyP2PSignature(publicKeyHex string, message string, signatureHex string) error {
	// Decode hex strings
	signatureBytes, err := hex.DecodeString(signatureHex)
	if err != nil {
		return fmt.Errorf("failed to decode signature hex: %w", err)
	}

	// Parse public key from hex string
	publicKey, err := ed25519PubKeyFromStringFn(publicKeyHex)
	if err != nil {
		return fmt.Errorf("failed to parse public key: %w", err)
	}

	// Get the underlying Ed25519 public key
	ed25519PubKey, ok := publicKey.Underlying().(ed25519.PublicKey)
	if !ok {
		return fmt.Errorf("public key is not Ed25519")
	}

	// Verify the signature using DefraDB's crypto package
	err = crypto.VerifyEd25519(ed25519PubKey, []byte(message), signatureBytes)
	if err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}

	return nil
}