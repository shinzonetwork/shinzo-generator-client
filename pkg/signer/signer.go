package signer

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defradb"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/node"
)

// loadIdentityFromStore loads the DefraDB identity from the keyring in the
// keys directory beside the store path. Opening the keyring runs the one-time
// migrations (in-store keyring copy, plaintext key import), so the keyring is
// the only source the signer reads; there is no file fallback.
func loadIdentityFromStore(cfg *config.Config) (identity.FullIdentity, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config with a keyring secret is required to load the identity")
	}

	kr, err := defradb.OpenKeyring(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to open keyring: %w", err)
	}

	id, err := defradb.LoadIdentityFromKeyring(kr)
	if err != nil {
		return nil, fmt.Errorf("failed to load identity from keyring: %w", err)
	}

	full, ok := id.(identity.FullIdentity)
	if !ok {
		return nil, fmt.Errorf("identity from keyring is not a FullIdentity")
	}

	return full, nil
}

// SignWithDefraKeys signs a message using the DefraDB identity's private key (secp256k1).
// The signature is returned as a hex-encoded string. The identity is loaded
// from the keyring beside the store path, so cfg must carry a KeyringSecret.
func SignWithDefraKeys(message string, defraNode *node.Node, cfg *config.Config) (string, error) {
	return signWithDefraKeysInternal(message, defraNode, cfg, loadIdentityFromStore)
}

func signWithDefraKeysInternal(
	message string, _ *node.Node, cfg *config.Config,
	loadFn func(*config.Config) (identity.FullIdentity, error),
) (string, error) {
	// Load the identity from the keyring beside the store path
	fullIdentity, err := loadFn(cfg)
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
// The signature is returned as a hex-encoded string. The identity is loaded
// from the keyring beside the store path, so cfg must carry a KeyringSecret.
func SignWithP2PKeys(message string, defraNode *node.Node, cfg *config.Config) (string, error) {
	return signWithP2PKeysInternal(message, defraNode, cfg, loadIdentityFromStore)
}

func signWithP2PKeysInternal(
	message string, _ *node.Node, cfg *config.Config,
	loadFn func(*config.Config) (identity.FullIdentity, error),
) (string, error) {
	fullIdentity, err := loadFn(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to load identity: %w", err)
	}

	libp2pPrivKey, err := defradb.CreateLibP2PKeyFromIdentity(fullIdentity)
	if err != nil {
		return "", fmt.Errorf("failed to create LibP2P key from identity: %w", err)
	}

	// LibP2P's Raw() may return 32 or 64 bytes depending on the key type.
	rawKeyBytes, err := libp2pPrivKey.Raw()
	if err != nil {
		return "", fmt.Errorf("failed to get raw key bytes from LibP2P key: %w", err)
	}

	// Ed25519.NewKeyFromSeed expects exactly 32 bytes (the seed).
	// If we got 64 bytes, take only the first 32 (the seed portion).
	var ed25519Seed []byte
	switch len(rawKeyBytes) {
	case ed25519.PrivateKeySize:
		ed25519Seed = rawKeyBytes[:ed25519.SeedSize]
	case ed25519.SeedSize:
		ed25519Seed = rawKeyBytes
	default:
		return "", fmt.Errorf("unexpected Ed25519 key length: expected 32 or 64 bytes, got %d", len(rawKeyBytes))
	}

	ed25519PrivKey := ed25519.NewKeyFromSeed(ed25519Seed)
	signature := ed25519.Sign(ed25519PrivKey, []byte(message))
	return hex.EncodeToString(signature), nil
}

// GetDefraPublicKey returns the DefraDB identity's public key as a hex-encoded string.
// The identity is loaded from the keyring beside the store path, so cfg must
// carry a KeyringSecret.
func GetDefraPublicKey(defraNode *node.Node, cfg *config.Config) (string, error) {
	return getDefraPublicKeyInternal(defraNode, cfg, loadIdentityFromStore)
}

func getDefraPublicKeyInternal(
	_ *node.Node, cfg *config.Config,
	loadFn func(*config.Config) (identity.FullIdentity, error),
) (string, error) {
	fullIdentity, err := loadFn(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to load identity: %w", err)
	}

	publicKey := fullIdentity.PublicKey()
	if publicKey == nil {
		return "", fmt.Errorf("identity does not have a public key")
	}

	return publicKey.String(), nil
}

// GetP2PPublicKey returns the LibP2P public key (Ed25519) derived from the DefraDB identity as a hex-encoded string.
// The identity is loaded from the keyring beside the store path, so cfg must
// carry a KeyringSecret.
func GetP2PPublicKey(defraNode *node.Node, cfg *config.Config) (string, error) {
	return getP2PPublicKeyInternal(defraNode, cfg, loadIdentityFromStore)
}

func getP2PPublicKeyInternal(
	_ *node.Node, cfg *config.Config,
	loadFn func(*config.Config) (identity.FullIdentity, error),
) (string, error) {
	fullIdentity, err := loadFn(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to load identity: %w", err)
	}

	libp2pPrivKey, err := defradb.CreateLibP2PKeyFromIdentity(fullIdentity)
	if err != nil {
		return "", fmt.Errorf("failed to create LibP2P key from identity: %w", err)
	}

	libp2pPubKey := libp2pPrivKey.GetPublic()

	ed25519PubKeyBytes, err := libp2pPubKey.Raw()
	if err != nil {
		return "", fmt.Errorf("failed to get raw public key bytes: %w", err)
	}

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
	return verifyP2PSignatureInternal(publicKeyHex, message, signatureHex, func(h string) (crypto.PublicKey, error) {
		return crypto.PublicKeyFromString(crypto.KeyTypeEd25519, h)
	})
}

func verifyP2PSignatureInternal(
	publicKeyHex string, message string, signatureHex string,
	pubKeyFn func(string) (crypto.PublicKey, error),
) error {
	signatureBytes, err := hex.DecodeString(signatureHex)
	if err != nil {
		return fmt.Errorf("failed to decode signature hex: %w", err)
	}

	publicKey, err := pubKeyFn(publicKeyHex)
	if err != nil {
		return fmt.Errorf("failed to parse public key: %w", err)
	}

	ed25519PubKey, ok := publicKey.Underlying().(ed25519.PublicKey)
	if !ok {
		return fmt.Errorf("public key is not Ed25519")
	}

	err = crypto.VerifyEd25519(ed25519PubKey, []byte(message), signatureBytes)
	if err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}

	return nil
}
