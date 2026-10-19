package defradb

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defracontext"
	indexerErrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/keyring"
	"github.com/sourcenetwork/defradb/node"
	"github.com/sourcenetwork/immutable/enumerable"
)

// NewDefaultConfig returns the baseline application config for running DefraDB with P2P enabled,
// local store path, and defaults for bootstrap peers and retry behavior.
func NewDefaultConfig() *config.Config {
	return &config.Config{
		DefraDB: config.DefraDBConfig{
			URL:           "http://localhost:9181",
			KeyringSecret: os.Getenv("DEFRA_KEYRING_SECRET"),
			P2P: config.DefraDBP2PConfig{
				Enabled:                 true, // P2P enabled by default
				BootstrapPeers:          nil,  // Can be populated before use; see applyRequiredP2PDefaults
				ListenAddr:              defaultListenAddress,
				MaxRetries:              defaultP2PMaxRetries,
				RetryBaseDelayMs:        defaultP2PRetryBaseDelayMs,    // 1 second
				ReconnectIntervalMs:     defaultP2PReconnectIntervalMs, // 60 seconds
				EnableAutoReconnect:     true,
				ResourceMemoryMiB:       defaultP2PResourceMemoryMiB,
				ResourceFileDescriptors: defaultP2PResourceFileDescriptors,
				MaxStreamsPerPeer:       defaultP2PMaxStreamsPerPeer,
			},
			Store: config.DefraDBStoreConfig{
				Path: ".defra",
			},
		},
		Logger: config.LoggerConfig{
			Development: false,
		},
	}
}

const (
	defaultListenAddress string = "/ip4/127.0.0.1/tcp/9171"

	// NodeIdentityKeyName is the keyring entry name for the node's libp2p identity private key.
	NodeIdentityKeyName string = "node-identity-key"

	// keyringDirName is the directory holding the file keyring. It sits beside
	// the store directory so wiping or restoring the store cannot take the node
	// identity with it. The same name was the in-store location used before the
	// move, which the startup migration reads.
	keyringDirName string = "keys"

	// plaintextIdentityFileName is the legacy plaintext key file older releases
	// kept in the store path before the keyring existed.
	plaintextIdentityFileName string = "defra_identity.key"

	defaultP2PMaxRetries = 5
	// libp2p resource manager defaults. Left unset, libp2p autoscales its limits from the memory
	// the process can see, which inside a container is the host machine's rather than the cgroup
	// limit. That mismatch grants a node limits its container cannot honour and lands the per-peer
	// stream ceiling somewhere arbitrary, so peers reset incoming streams with error 4098
	// (network.StreamResourceLimitExceeded). Setting them explicitly keeps the budget predictable.
	defaultP2PResourceMemoryMiB       = 10240
	defaultP2PResourceFileDescriptors = 8192
	defaultP2PMaxStreamsPerPeer       = 4096
	defaultP2PRetryBaseDelayMs        = 1000
	defaultP2PReconnectIntervalMs     = 60000
	secp256k1PrivKeyBytes             = 32
	log2BytesPerMebibyte              = 20
	defaultBadgerValueLogFileSizeMB   = 64
	subscriptionResultChanCapacity    = 100_000
	testKeyringSecret                 = "testSecret"
)

// Key Management Implementation Notes:
//
// This implementation provides persistent DefraDB identity management using the keyring:
// 1. Extracting private key bytes from generated FullIdentity
// 2. Storing the raw key bytes in encrypted keyring storage (file-based keyring)
// 3. Reconstructing the same identity from stored private key bytes on subsequent runs
// 4. Ensuring the same cryptographic identity is used across application restarts
//
// The keyring lives in a keys directory beside the store directory, not inside
// it, so wiping or restoring the store cannot take the node identity with it.
// Older layouts are migrated on first open, before any identity is generated,
// so an upgraded node always keeps its existing identity:
//   - A keyring at {storePath}/keys, the previous in-store location, is copied
//     to the sibling keys directory.
//   - A plaintext defra_identity.key in the store path is imported into the
//     keyring.
//
// Both migrations copy and leave the source in place as an operator-visible
// backup. A new identity is generated only when neither source holds one.
//
// Security Features:
// - Keys stored in encrypted keyring (keys directory beside the store path)
// - Encryption key derived from KeyringSecret
// - Proper error handling for corrupted or missing keys
// - Requires DEFRA_KEYRING_SECRET environment variable or config

// OpenKeyring opens the file-based keyring in the keys directory beside the
// store path using KeyringSecret as the encryption secret. Returns an error if
// cfg is nil or KeyringSecret is empty; callers that want a "no keyring → fall
// back" flow should detect that error at their own call site rather than
// relying on a silent nil return.
//
// Opening the keyring runs the one-time migrations first (in-store keyring
// copy, plaintext identity import), so every consumer sees the migrated state
// and the load-or-generate decision never runs while an old identity exists.
func OpenKeyring(cfg *config.Config) (keyring.Keyring, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	if cfg.DefraDB.KeyringSecret == "" {
		return nil, fmt.Errorf("KeyringSecret is required for keyring-based key management")
	}

	if logger.Sugar == nil {
		logger.Init(cfg.Logger.Development)
	}

	keyringPath := keyringPathFor(cfg.DefraDB.Store.Path)

	// Ensure directory exists
	if err := os.MkdirAll(keyringPath, 0o750); err != nil { //nolint:mnd
		return nil, fmt.Errorf("failed to create keyring directory: %w", err)
	}

	if err := migrateKeyringDir(cfg.DefraDB.Store.Path, keyringPath); err != nil {
		return nil, fmt.Errorf("failed to migrate keyring: %w", err)
	}

	kr, err := keyring.OpenFileKeyring(keyringPath, []byte(cfg.DefraDB.KeyringSecret))
	if err != nil {
		return nil, fmt.Errorf("failed to open keyring: %w", err)
	}

	if err := importPlaintextIdentity(kr, cfg.DefraDB.Store.Path); err != nil {
		return nil, fmt.Errorf("failed to import legacy plaintext identity: %w", err)
	}

	return kr, nil
}

// keyringPathFor resolves the keyring to a keys directory beside the store
// directory so restoring or wiping the store cannot take the node identity
// with it. An empty store path keeps the current-directory "keys" fallback.
func keyringPathFor(storePath string) string {
	if storePath == "" {
		return keyringDirName
	}
	return filepath.Join(filepath.Dir(storePath), keyringDirName)
}

// migrateKeyringDir copies a keyring found at {storePath}/keys, the location
// used before the keyring moved beside the store, into the sibling keyring
// directory. It only copies when the destination has no node-identity-key, so
// a key already at the new location is never overwritten and existing
// destination entries are never clobbered. The legacy directory is left
// untouched as an operator-visible backup.
func migrateKeyringDir(storePath, keyringPath string) error {
	if storePath == "" {
		return nil
	}
	legacyPath := filepath.Join(storePath, keyringDirName)
	if filepath.Clean(legacyPath) == filepath.Clean(keyringPath) {
		return nil
	}

	legacyKeyFile := filepath.Join(legacyPath, NodeIdentityKeyName)
	if _, err := os.Stat(legacyKeyFile); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to inspect legacy keyring: %w", err)
		}
		return nil
	}
	newKeyFile := filepath.Join(keyringPath, NodeIdentityKeyName)
	if _, err := os.Stat(newKeyFile); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect keyring: %w", err)
	}

	entries, err := os.ReadDir(legacyPath)
	if err != nil {
		return fmt.Errorf("failed to read legacy keyring directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		dst := filepath.Join(keyringPath, entry.Name())
		if _, err := os.Stat(dst); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("failed to inspect keyring entry: %w", err)
		}
		if err := copyKeyringFile(filepath.Join(legacyPath, entry.Name()), dst); err != nil {
			return fmt.Errorf("failed to copy keyring entry %s: %w", entry.Name(), err)
		}
	}

	logger.Sugar.Infof("Migrated node identity keyring from %s to %s", legacyPath, keyringPath)
	return nil
}

// copyKeyringFile copies a single keyring file, preserving its permissions.
func copyKeyringFile(src, dst string) error {
	data, err := os.ReadFile(src) //nolint:gosec // src comes from os.ReadDir on the local legacy keyring directory, never from user input
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode().Perm()) //nolint:gosec // dst is the sibling keyring directory opened for the same migration purpose
}

// importPlaintextIdentity imports the legacy plaintext defra_identity.key from
// the store path into the keyring when the keyring holds no node-identity-key.
// Releases before the keyring stored the identity as a hex file in the store
// path; importing it here keeps those nodes on their existing identity instead
// of generating a new one. The file stays in place as an operator-visible
// backup. An unparsable file is logged and skipped rather than failing startup:
// a key that cannot be decoded holds no recoverable identity, and blocking
// startup over it would leave the node down while the keyring can still hold a
// valid identity from another source.
func importPlaintextIdentity(kr keyring.Keyring, storePath string) error {
	if storePath == "" {
		return nil
	}
	keyPath := filepath.Join(storePath, plaintextIdentityFileName)
	if _, err := os.Stat(keyPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to inspect legacy key file: %w", err)
		}
		return nil
	}

	if _, err := kr.Get(NodeIdentityKeyName); err == nil {
		return nil
	} else if !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("failed to inspect keyring: %w", err)
	}

	nodeIdentity, err := fullIdentityFromHexFile(keyPath)
	if err != nil {
		logger.Sugar.Warnf("Ignoring unreadable legacy identity file %s: %v", keyPath, err)
		return nil
	}

	if err := saveNodeIdentityToKeyring(kr, nodeIdentity); err != nil {
		return err
	}

	logger.Sugar.Infof("Imported legacy plaintext identity from %s into the keyring", keyPath)
	return nil
}

// fullIdentityFromHexFile reads a hex-encoded secp256k1 private key file and
// rebuilds the FullIdentity, matching the legacy defra_identity.key format.
func fullIdentityFromHexFile(keyPath string) (identity.FullIdentity, error) {
	keyHex, err := os.ReadFile(filepath.Clean(keyPath))
	if err != nil {
		return nil, fmt.Errorf("failed to read key file at %s: %w", keyPath, err)
	}

	keyBytes, err := hex.DecodeString(strings.TrimSpace(string(keyHex)))
	if err != nil {
		return nil, fmt.Errorf("failed to decode key hex: %w", err)
	}

	privateKey, err := crypto.PrivateKeyFromBytes(crypto.KeyTypeSecp256k1, keyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to reconstruct private key: %w", err)
	}

	fullIdentity, err := identity.FromPrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to reconstruct identity from private key: %w", err)
	}

	return fullIdentity, nil
}

// getOrCreateNodeIdentity retrieves an existing node identity from keyring or creates a new one.
func getOrCreateNodeIdentity(cfg *config.Config) (identity.Identity, error) {
	// Open keyring (required, no fallback)
	kr, err := OpenKeyring(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to open keyring: %w", err)
	}

	// Try to load existing identity from keyring
	identityBytes, err := kr.Get(NodeIdentityKeyName)
	if err != nil {
		if !errors.Is(err, keyring.ErrNotFound) {
			return nil, fmt.Errorf("failed to get identity from keyring: %w", err)
		}

		// Key not found, create new identity
		logger.Sugar.Info("Generating new DefraDB identity")
		nodeIdentity, err := identity.Generate(crypto.KeyTypeSecp256k1)
		if err != nil {
			return nil, fmt.Errorf("failed to generate new identity: %w", err)
		}

		// Save the new identity to keyring
		if err := saveNodeIdentityToKeyring(kr, nodeIdentity); err != nil {
			return nil, fmt.Errorf("failed to save identity to keyring: %w", err)
		}

		return nodeIdentity, nil
	}

	// Load existing identity from keyring
	logger.Sugar.Info("Loading existing DefraDB identity from keyring")
	return LoadIdentityFromBytes(identityBytes)
}

// saveNodeIdentityToKeyring saves the private key bytes of a node identity to the keyring.
func saveNodeIdentityToKeyring(kr keyring.Keyring, nodeIdentity identity.Identity) error {
	// Cast to FullIdentity to access private key
	fullIdentity, ok := nodeIdentity.(identity.FullIdentity)
	if !ok {
		return fmt.Errorf("identity is not a FullIdentity, cannot extract private key")
	}

	// Get the private key from the identity
	privateKey := fullIdentity.PrivateKey()
	if privateKey == nil {
		return fmt.Errorf("failed to get private key from identity")
	}

	// Get raw key bytes
	keyBytes := privateKey.Raw()
	if len(keyBytes) == 0 {
		return fmt.Errorf("private key has no raw bytes")
	}

	// Format: "keyType:rawKeyBytes" (same format as DefraDB CLI)
	keyType := string(privateKey.Type())
	identityBytes := append([]byte(keyType+":"), keyBytes...)

	// Save to keyring
	if err := kr.Set(NodeIdentityKeyName, identityBytes); err != nil {
		return fmt.Errorf("failed to save identity to keyring: %w", err)
	}

	logger.Sugar.Info("DefraDB identity private key saved to keyring")
	return nil
}

// LoadIdentityFromBytes parses identity bytes in the keyring's "keyType:rawKeyBytes"
// format (the same format DefraDB CLI writes) and rebuilds the FullIdentity.
// Pre-prefix bytes are assumed to be secp256k1 for backward compatibility.
func LoadIdentityFromBytes(identityBytes []byte) (identity.Identity, error) {
	// Parse the format: "keyType:rawKeyBytes"
	sepPos := bytes.Index(identityBytes, []byte(":"))
	if sepPos == -1 {
		// Old format without key type prefix, assume secp256k1
		identityBytes = append([]byte(crypto.KeyTypeSecp256k1+":"), identityBytes...)
		sepPos = len(crypto.KeyTypeSecp256k1)
	}

	keyType := string(identityBytes[:sepPos])
	keyBytes := identityBytes[sepPos+1:]

	privateKey, err := crypto.PrivateKeyFromBytes(crypto.KeyType(keyType), keyBytes)
	if err != nil {
		var emptyIdentity identity.Identity
		return emptyIdentity, fmt.Errorf("failed to reconstruct private key: %w", err)
	}

	fullIdentity, err := identity.FromPrivateKey(privateKey)
	if err != nil {
		var emptyIdentity identity.Identity
		return emptyIdentity, fmt.Errorf("failed to reconstruct identity from private key: %w", err)
	}

	logger.Sugar.Info("DefraDB identity successfully loaded from keyring")
	return fullIdentity, nil
}

// LoadIdentityFromKeyring fetches the node-identity entry from kr and parses it
// into an Identity. Convenience wrapper for callers that already hold an open
// keyring and don't need the create-if-missing branch.
func LoadIdentityFromKeyring(kr keyring.Keyring) (identity.Identity, error) {
	identityBytes, err := kr.Get(NodeIdentityKeyName)
	if err != nil {
		return nil, fmt.Errorf("failed to get identity from keyring: %w", err)
	}
	return LoadIdentityFromBytes(identityBytes)
}

// GetOrCreateNodeIdentity retrieves an existing node identity from keyring or creates a new one.
// This is an exported version of getOrCreateNodeIdentity for use by external packages.
func GetOrCreateNodeIdentity(cfg *config.Config) (identity.Identity, error) {
	return getOrCreateNodeIdentity(cfg)
}

// WithIdentityContext returns a new context with the identity value set.
// Thin wrapper over defracontext.WithIdentity for callers that already import this package.
func WithIdentityContext(ctx context.Context, id identity.Identity) context.Context {
	return defracontext.WithIdentity(ctx, id)
}

// IdentityFromContext returns the identity stored in the context, if any.
// Thin wrapper over defracontext.IdentityFrom for callers that already import this package.
func IdentityFromContext(ctx context.Context) (identity.Identity, bool) {
	return defracontext.IdentityFrom(ctx)
}

// GetIdentityContext returns a context with the node identity attached.
func GetIdentityContext(ctx context.Context, cfg *config.Config) (context.Context, error) {
	nodeIdentity, err := getOrCreateNodeIdentity(cfg)
	if err != nil {
		return ctx, fmt.Errorf("failed to get node identity: %w", err)
	}
	return defracontext.WithIdentity(ctx, nodeIdentity), nil
}

// CreateLibP2PKeyFromIdentity derives a deterministic libp2p Ed25519 private
// key from a DefraDB secp256k1 identity. The 32-byte secp256k1 key bytes are
// used as the seed, so the libp2p peer ID stays stable across restarts and
// matches across every component that calls this with the same identity.
func CreateLibP2PKeyFromIdentity(nodeIdentity identity.Identity) (libp2pcrypto.PrivKey, error) {
	// Cast to FullIdentity to access private key
	fullIdentity, ok := nodeIdentity.(identity.FullIdentity)
	if !ok {
		return nil, fmt.Errorf("identity is not a FullIdentity, cannot extract private key")
	}

	// Get the private key from the identity
	privateKey := fullIdentity.PrivateKey()
	if privateKey == nil {
		return nil, fmt.Errorf("failed to get private key from identity")
	}

	// Get raw key bytes
	keyBytes := privateKey.Raw()
	if len(keyBytes) == 0 {
		return nil, fmt.Errorf("private key has no raw bytes")
	}

	// DefraDB expects Ed25519 keys, but DefraDB identities use secp256k1
	// We need to derive an Ed25519 key deterministically from the secp256k1 key
	// Use the secp256k1 key bytes as seed for Ed25519 key generation
	if len(keyBytes) != secp256k1PrivKeyBytes {
		return nil, fmt.Errorf("expected 32-byte secp256k1 key, got %d bytes", len(keyBytes))
	}

	// Generate Ed25519 key from secp256k1 seed
	libp2pPrivKey, _, err := libp2pcrypto.GenerateEd25519Key(strings.NewReader(string(keyBytes)))
	if err != nil {
		return nil, fmt.Errorf("failed to generate Ed25519 key from identity seed: %w", err)
	}

	return libp2pPrivKey, nil
}

func applyRequiredP2PDefaults(cfg *config.Config) {
	// No required bootstrap peers currently; extend here when well-known peers are added.
	if len(cfg.DefraDB.P2P.ListenAddr) == 0 {
		cfg.DefraDB.P2P.ListenAddr = defaultListenAddress
	}
}

func getNodeIdentityAndP2PKeyBytes(cfg *config.Config) (identity.Identity, []byte, error) {
	nodeIdentity, err := getOrCreateNodeIdentity(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting or creating identity: %w", err)
	}

	libp2pPrivKey, err := CreateLibP2PKeyFromIdentity(nodeIdentity)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating LibP2P private key from identity: %w", err)
	}

	libp2pKeyBytes, err := libp2pPrivKey.Raw()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting LibP2P private key bytes: %w", err)
	}

	return nodeIdentity, libp2pKeyBytes, nil
}

func resolveDefraAddresses(cfg *config.Config) (string, string, error) {
	ipAddress, err := utils.GetLANIP()
	if err != nil {
		return "", "", fmt.Errorf("failed to get LAN IP address: %w", err)
	}

	defraURL := replaceLoopbackAddress(cfg.DefraDB.URL, ipAddress)
	listenAddress := cfg.DefraDB.P2P.ListenAddr
	if len(listenAddress) > 0 {
		listenAddress = replaceLoopbackAddress(listenAddress, ipAddress)
	}

	return defraURL, listenAddress, nil
}

func replaceLoopbackAddress(value, ipAddress string) string {
	value = strings.Replace(value, "http://localhost", ipAddress, 1)
	value = strings.Replace(value, "http://127.0.0.1", ipAddress, 1)
	value = strings.Replace(value, "localhost", ipAddress, 1)
	value = strings.Replace(value, "127.0.0.1", ipAddress, 1)
	return value
}

func buildStoreOptions(cfg *config.Config) []func(*options.NodeOptions) { //nolint:revive // TODO! Update or remove function if it is confirmed that it is outdated
	// Badger tuning knobs (BlockCacheMB, MemTableMB, IndexCacheMB, NumCompactors,
	// NumLevelZeroTables, NumLevelZeroTablesStall) were removed from NodeStoreOptions
	// in defradb v1.0.0-rc1. Any corresponding config fields are intentionally ignored.
	return nil
}

// applyP2PResourceLimits gives libp2p an explicit resource-manager budget instead of
// letting it autoscale. Autoscaling reads the memory the process can see, which inside
// a container is the host machine's, not the cgroup's, so a node can be granted limits
// it cannot honour and ends up resetting streams with StreamResourceLimitExceeded.
//
// Each limit is resolved here rather than on the config, so any caller that builds node
// options gets explicit limits instead of silently falling back to the autoscaling this
// exists to replace.
func applyP2PResourceLimits(nb *options.NodeOptionsBuilder, p2pCfg config.DefraDBP2PConfig) {
	memoryMiB := p2pCfg.ResourceMemoryMiB
	if memoryMiB <= 0 {
		memoryMiB = defaultP2PResourceMemoryMiB
	}

	fileDescriptors := p2pCfg.ResourceFileDescriptors
	if fileDescriptors <= 0 {
		fileDescriptors = defaultP2PResourceFileDescriptors
	}

	maxStreamsPerPeer := p2pCfg.MaxStreamsPerPeer
	if maxStreamsPerPeer <= 0 {
		maxStreamsPerPeer = defaultP2PMaxStreamsPerPeer
	}

	nb.P2P().
		SetResourceMemoryMiB(memoryMiB).
		SetResourceFileDescriptors(fileDescriptors).
		SetMaxStreamsPerPeer(maxStreamsPerPeer)

	logger.Sugar.Infof(
		"P2P resource limits configured: %dMiB memory, %d file descriptors, %d streams per peer",
		memoryMiB, fileDescriptors, maxStreamsPerPeer,
	)
}

func buildNodeOptions(
	cfg *config.Config,
	nodeIdentity identity.Identity,
	defraURL, listenAddress string,
	libp2pKeyBytes []byte,
	nodeOpts []options.Enumerable[options.NodeOptions],
) []options.Enumerable[options.NodeOptions] {
	nb := options.Node().
		SetDisableAPI(false).
		SetDisableP2P(false)
	nb.P2P().SetEnablePubSub(true)
	applyP2PResourceLimits(nb, cfg.DefraDB.P2P)
	nb.Store().SetPath(cfg.DefraDB.Store.Path)
	nb.HTTP().SetAddress(defraURL)
	nb.DB().SetNodeIdentity(nodeIdentity)

	vlogSizeMB := cfg.DefraDB.Store.ValueLogFileSizeMB
	if vlogSizeMB <= 0 {
		vlogSizeMB = defaultBadgerValueLogFileSizeMB
	}
	nb.Store().SetBadgerFileSize(vlogSizeMB << log2BytesPerMebibyte)
	logger.Sugar.Infof("Badger value log file size: %dMB", vlogSizeMB)

	if len(listenAddress) > 0 {
		nb.P2P().SetListenAddresses(listenAddress)
		logger.Sugar.Infof("P2P Listen Address configured: %s", listenAddress)
	}
	if len(libp2pKeyBytes) > 0 {
		nb.P2P().SetPrivateKey(libp2pKeyBytes)
		logger.Sugar.Info("P2P Private Key configured for consistent peer ID")
	}

	allOpts := []options.Enumerable[options.NodeOptions]{nb}
	storeOpts := buildStoreOptions(cfg)
	if len(storeOpts) > 0 {
		allOpts = append(allOpts, enumerable.New(storeOpts))
	}

	return append(allOpts, nodeOpts...)
}

func createAndStartNode(
	ctx context.Context,
	allOpts []options.Enumerable[options.NodeOptions],
	replicationFilter client.ReplicationFilter,
) (*node.Node, error) {
	defraNode, err := node.New(ctx, allOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create defra node: %w", err)
	}

	if replicationFilter != nil {
		defraNode.ReplicationFilter = replicationFilter
	}

	if err := defraNode.Start(ctx); err != nil {
		_ = defraNode.Close(ctx)
		return nil, fmt.Errorf("failed to start defra node: %w", err)
	}

	return defraNode, nil
}

func initializeNetworkHandler(ctx *context.Context, defraNode *node.Node, cfg *config.Config) *NetworkHandler {
	networkHandler := NewNetworkHandler(ctx, defraNode, cfg)
	if cfg.DefraDB.P2P.Enabled {
		if err := networkHandler.StartNetwork(ctx); err != nil {
			logger.Sugar.Warnf("Failed to start P2P network: %v", err)
		}
	} else {
		logger.Sugar.Info("🔇 P2P networking disabled by configuration")
	}

	return networkHandler
}

// StartDefraInstance configures and starts a DefraDB node from cfg (identity,
// store, HTTP URL, P2P), merges nodeOpts, assigns replicationFilter when
// non-nil, applies schema via schemaApplier, registers collectionsOfInterest
// for P2P, and returns the running node and its NetworkHandler.
func StartDefraInstance(cfg *config.Config, schemaApplier SchemaApplier, nodeOpts []options.Enumerable[options.NodeOptions], replicationFilter client.ReplicationFilter, collectionsOfInterest ...string) (*node.Node, *NetworkHandler, error) {
	ctx := context.Background()

	if cfg == nil {
		return nil, nil, fmt.Errorf("config cannot be nil")
	}
	applyRequiredP2PDefaults(cfg)

	if logger.Sugar == nil {
		logger.Init(cfg.Logger.Development)
	}

	nodeIdentity, libp2pKeyBytes, err := getNodeIdentityAndP2PKeyBytes(cfg)
	if err != nil {
		return nil, nil, err
	}

	defraURL, listenAddress, err := resolveDefraAddresses(cfg)
	if err != nil {
		return nil, nil, err
	}

	allOpts := buildNodeOptions(cfg, nodeIdentity, defraURL, listenAddress, libp2pKeyBytes, nodeOpts)
	defraNode, err := createAndStartNode(ctx, allOpts, replicationFilter)
	if err != nil {
		return nil, nil, err
	}

	if err := schemaApplier.ApplySchema(ctx, defraNode); err != nil {
		_ = defraNode.Close(ctx)
		return nil, nil, fmt.Errorf("failed to apply schema: %w", err)
	}

	err = defraNode.DB.AddP2PCollections(ctx, collectionsOfInterest)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to add collections of interest %v: %w", collectionsOfInterest, err)
	}

	networkHandler := initializeNetworkHandler(&ctx, defraNode, cfg)

	return defraNode, networkHandler, nil
}

// StartDefraInstanceWithTestConfig is a simple wrapper on StartDefraInstance that changes the configured defra store path to a temp directory for the test. The store is nested inside the temp directory so the keyring sits beside the store, not inside it, and separate nodes started from the same test never share a keyring.
func StartDefraInstanceWithTestConfig(t *testing.T, cfg *config.Config, schemaApplier SchemaApplier, collectionsOfInterest ...string) (*node.Node, error) {
	ipAddress, err := utils.GetLANIP()
	if err != nil {
		return nil, err
	}
	listenAddress := fmt.Sprintf("/ip4/%s/tcp/0", ipAddress)
	defraURL := fmt.Sprintf("%s:0", ipAddress)
	if cfg == nil {
		cfg = NewDefaultConfig()
	}
	cfg.DefraDB.Store.Path = filepath.Join(t.TempDir(), "store")
	cfg.DefraDB.URL = defraURL
	cfg.DefraDB.P2P.ListenAddr = listenAddress
	cfg.DefraDB.KeyringSecret = testKeyringSecret
	node, _, err := StartDefraInstance(cfg, schemaApplier, nil, nil, collectionsOfInterest...)
	return node, err
}

// Subscribe creates a GraphQL subscription for real-time updates.
//
// This function uses non-blocking sends to prevent slow consumers from blocking subscription processing.
func Subscribe[T any](ctx context.Context, defraNode *node.Node, subscription string) (<-chan T, error) {
	result := defraNode.DB.ExecRequest(ctx, subscription)

	if result.Subscription == nil {
		// Check if there are GraphQL errors that explain why subscription is nil
		if result.GQL.Errors != nil {
			return nil, fmt.Errorf("subscription failed with GraphQL errors: %v", result.GQL.Errors)
		}
		return nil, fmt.Errorf("subscription channel is nil - DefraDB may not support subscriptions for this query: %s", subscription)
	}

	resultChan := make(chan T, subscriptionResultChanCapacity)

	go func() {
		defer close(resultChan)

		for {
			select {
			case <-ctx.Done():
				return
			case gqlResult, ok := <-result.Subscription:
				if !ok {
					return
				}

				if gqlResult.Errors != nil {
					// log errors but continue
					logger.Sugar.Errorf("failed to subscribe: %s , errors: %v", subscription, gqlResult.Errors)
					continue
				}
				// Parse and send typed result
				var typedResult T
				if err := marshalUnmarshal(gqlResult.Data, &typedResult); err == nil {
					// Non-blocking send to prevent slow consumers from blocking subscription processing
					select {
					case resultChan <- typedResult:
					case <-ctx.Done():
						return
					default:
						logger.Sugar.Warnf("subscription buffer full, dropping event for query: %s", subscription)
					}
				} else {
					logger.Sugar.Errorf("failed to parse subscription data: %v, raw data: %+v", err, gqlResult.Data)
				}
			}
		}
	}()

	return resultChan, nil
}

// marshalUnmarshal converts a generic interface{} to a specific typed struct
// using JSON marshal/unmarshal. This is the same pattern used throughout the query client.
func marshalUnmarshal(data any, target any) error {
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}
	return json.Unmarshal(dataBytes, target)
}

// ====================================================================
// NEW CLIENT API - Clean alternative to StartDefraInstance
// ====================================================================

// Client provides a clean interface for DefraDB operations.
type Client struct {
	node    *node.Node
	network *NetworkHandler
	config  *config.Config
}

// NewClient creates a new client instance (doesn't start anything).
func NewClient(cfg *config.Config) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	return &Client{
		config: cfg,
	}, nil
}

// Start initializes key generation, node startup, and network handler.
func (c *Client) Start(ctx context.Context) error {
	if c.node != nil {
		return fmt.Errorf("client already started")
	}

	applyRequiredP2PDefaults(c.config)

	if logger.Sugar == nil {
		logger.Init(c.config.Logger.Development)
	}

	nodeIdentity, libp2pKeyBytes, err := getNodeIdentityAndP2PKeyBytes(c.config)
	if err != nil {
		return err
	}

	defraURL, listenAddress, err := resolveDefraAddresses(c.config)
	if err != nil {
		return err
	}

	allOpts := buildNodeOptions(c.config, nodeIdentity, defraURL, listenAddress, libp2pKeyBytes, nil)
	c.node, err = createAndStartNode(ctx, allOpts, nil)
	if err != nil {
		return err
	}

	c.network = initializeNetworkHandler(&ctx, c.node, c.config)

	return nil
}

// Stop cleanly shuts down the client.
func (c *Client) Stop(ctx context.Context) error {
	if c.node == nil {
		return nil
	}

	err := c.node.Close(ctx)
	c.node = nil
	c.network = nil
	return err
}

// ApplySchema applies a GraphQL schema string to the started node.
func (c *Client) ApplySchema(ctx context.Context, schema string) error {
	if c.node == nil {
		return fmt.Errorf("client must be started before applying schema")
	}

	if len(schema) == 0 {
		return fmt.Errorf("schema cannot be empty")
	}

	_, err := c.node.DB.AddCollection(ctx, schema)
	if err != nil {
		if strings.Contains(err.Error(), indexerErrors.ErrStrCollectionAlreadyExists) {
			logger.Sugar.Warnf("Failed to apply schema: %v\nProceeding...", err)
			return nil
		}
		return fmt.Errorf("failed to apply schema: %w", err)
	}

	return nil
}

// ApplyCollectionSchemas applies the embedded collection schemas to the
// started node using the given chain's collection names.
// See ApplyCollectionSchemas for details on the monolithic-first strategy
// and additive-only guarantee.
func (c *Client) ApplyCollectionSchemas(ctx context.Context, collections chains.Collections) error {
	if c.node == nil {
		return fmt.Errorf("client must be started before applying schema")
	}

	return ApplyCollectionSchemas(ctx, c.node, collections)
}

// GetNode returns the underlying DefraDB node.
func (c *Client) GetNode() *node.Node {
	return c.node
}

// GetNetworkHandler returns the network handler.
func (c *Client) GetNetworkHandler() *NetworkHandler {
	return c.network
}
