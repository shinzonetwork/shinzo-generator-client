package defrasdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-app-sdk/pkg/networking"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/keyring"
	"github.com/sourcenetwork/defradb/node"
	"github.com/sourcenetwork/immutable"
	"github.com/sourcenetwork/immutable/enumerable"
)

var DefaultConfig *config.Config = &config.Config{
	DefraDB: config.DefraDBConfig{
		Url:           "http://localhost:9181",
		KeyringSecret: os.Getenv("DEFRA_KEYRING_SECRET"),
		P2P: config.DefraDBP2PConfig{
			Enabled:             true, // P2P enabled by default
			BootstrapPeers:      requiredPeers,
			ListenAddr:          defaultListenAddress,
			MaxRetries:          5,
			RetryBaseDelayMs:    1000,  // 1 second
			ReconnectIntervalMs: 60000, // 60 seconds
			EnableAutoReconnect: true,
		},
		Store: config.DefraDBStoreConfig{
			Path: ".defra",
		},
	},
	Logger: config.LoggerConfig{
		Development: false,
	},
}

var requiredPeers []string = []string{} // Here, we can add some "big peers" to give nodes a starting place when building their peer network
const defaultListenAddress string = "/ip4/127.0.0.1/tcp/9171"
const nodeIdentityKeyName string = "node-identity-key"

// Key Management Implementation Notes:
//
// This implementation provides persistent DefraDB identity management using the keyring:
// 1. Extracting private key bytes from generated FullIdentity
// 2. Storing the raw key bytes in encrypted keyring storage (file-based keyring)
// 3. Reconstructing the same identity from stored private key bytes on subsequent runs
// 4. Ensuring the same cryptographic identity is used across application restarts
//
// Current Status: FULLY FUNCTIONAL
// - Private keys are properly extracted and stored in keyring
// - Identities are reconstructed from keyring, maintaining consistency
// - Keys are encrypted using PBES2_HS512_A256KW algorithm
// - Comprehensive error handling and logging
//
// Security Features:
// - Keys stored in encrypted keyring (default: {storePath}/keys/)
// - Encryption key derived from KeyringSecret
// - Proper error handling for corrupted or missing keys
// - Requires DEFRA_KEYRING_SECRET environment variable or config

// openKeyring opens a keyring from the config.
// Returns an error if keyring cannot be opened (KeyringSecret is required).
func openKeyring(cfg *config.Config) (keyring.Keyring, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	if cfg.DefraDB.KeyringSecret == "" {
		return nil, fmt.Errorf("KeyringSecret is required for keyring-based key management")
	}

	// Use file-based keyring (default for DefraDB)
	// Keyring path defaults to "keys" directory in store path, or "keys" in current dir
	keyringPath := filepath.Join(cfg.DefraDB.Store.Path, "keys")
	if cfg.DefraDB.Store.Path == "" {
		keyringPath = "keys"
	}

	// Ensure directory exists
	if err := os.MkdirAll(keyringPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create keyring directory: %w", err)
	}

	secret := []byte(cfg.DefraDB.KeyringSecret)
	return keyring.OpenFileKeyring(keyringPath, secret)
}

// getOrCreateNodeIdentity retrieves an existing node identity from keyring or creates a new one
func getOrCreateNodeIdentity(cfg *config.Config) (identity.Identity, error) {
	// Open keyring (required, no fallback)
	kr, err := openKeyring(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to open keyring: %w", err)
	}

	// Try to load existing identity from keyring
	identityBytes, err := kr.Get(nodeIdentityKeyName)
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
	return loadNodeIdentityFromKeyring(identityBytes)
}

// saveNodeIdentityToKeyring saves the private key bytes of a node identity to the keyring
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
	if err := kr.Set(nodeIdentityKeyName, identityBytes); err != nil {
		return fmt.Errorf("failed to save identity to keyring: %w", err)
	}

	logger.Sugar.Info("DefraDB identity private key saved to keyring")
	return nil
}

// loadNodeIdentityFromKeyring loads a node identity from keyring bytes
func loadNodeIdentityFromKeyring(identityBytes []byte) (identity.Identity, error) {
	// Parse the format: "keyType:rawKeyBytes"
	sepPos := bytes.Index(identityBytes, []byte(":"))
	if sepPos == -1 {
		// Old format without key type prefix, assume secp256k1
		identityBytes = append([]byte(crypto.KeyTypeSecp256k1+":"), identityBytes...)
		sepPos = len(crypto.KeyTypeSecp256k1)
	}

	keyType := string(identityBytes[:sepPos])
	keyBytes := identityBytes[sepPos+1:]

	// Reconstruct private key from bytes
	privateKey, err := crypto.PrivateKeyFromBytes(crypto.KeyType(keyType), keyBytes)
	if err != nil {
		var emptyIdentity identity.Identity
		return emptyIdentity, fmt.Errorf("failed to reconstruct private key: %w", err)
	}

	// Reconstruct identity from private key
	fullIdentity, err := identity.FromPrivateKey(privateKey)
	if err != nil {
		var emptyIdentity identity.Identity
		return emptyIdentity, fmt.Errorf("failed to reconstruct identity from private key: %w", err)
	}

	logger.Sugar.Info("DefraDB identity successfully loaded from keyring")
	return fullIdentity, nil
}

// GetOrCreateNodeIdentity retrieves an existing node identity from keyring or creates a new one.
// This is an exported version of getOrCreateNodeIdentity for use by external packages.
func GetOrCreateNodeIdentity(cfg *config.Config) (identity.Identity, error) {
	return getOrCreateNodeIdentity(cfg)
}

// GetIdentityContext returns a context with the node identity attached.
func GetIdentityContext(ctx context.Context, cfg *config.Config) (context.Context, error) {
	nodeIdentity, err := getOrCreateNodeIdentity(cfg)
	if err != nil {
		return ctx, fmt.Errorf("failed to get node identity: %w", err)
	}
	return identity.WithContext(ctx, immutable.Some[identity.Identity](nodeIdentity)), nil
}

// createLibP2PKeyFromIdentity creates a LibP2P private key from a DefraDB identity
// This ensures the LibP2P peer ID is deterministically derived from the same identity
func createLibP2PKeyFromIdentity(nodeIdentity identity.Identity) (libp2pcrypto.PrivKey, error) {
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
	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("expected 32-byte secp256k1 key, got %d bytes", len(keyBytes))
	}

	// Generate Ed25519 key from secp256k1 seed
	libp2pPrivKey, _, err := libp2pcrypto.GenerateEd25519Key(strings.NewReader(string(keyBytes)))
	if err != nil {
		return nil, fmt.Errorf("failed to generate Ed25519 key from identity seed: %w", err)
	}

	return libp2pPrivKey, nil
}

func StartDefraInstance(cfg *config.Config, schemaApplier SchemaApplier, nodeOpts []options.Enumerable[options.NodeOptions], replicationFilter client.ReplicationFilter, collectionsOfInterest ...string) (*node.Node, *NetworkHandler, error) {
	ctx := context.Background()

	if cfg == nil {
		return nil, nil, fmt.Errorf("config cannot be nil")
	}
	cfg.DefraDB.P2P.BootstrapPeers = append(cfg.DefraDB.P2P.BootstrapPeers, requiredPeers...)
	if len(cfg.DefraDB.P2P.ListenAddr) == 0 {
		cfg.DefraDB.P2P.ListenAddr = defaultListenAddress
	}

	logger.Init(cfg.Logger.Development)

	// Use persistent identity from keyring (required, no fallback)
	nodeIdentity, err := getOrCreateNodeIdentity(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting or creating identity: %w", err)
	}

	// Create LibP2P private key from the same identity to ensure consistent peer ID
	libp2pPrivKey, err := createLibP2PKeyFromIdentity(nodeIdentity)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating LibP2P private key from identity: %v", err)
	}

	// Get raw bytes for P2P private key configuration (DefraDB 0.20 API TBD)
	libp2pKeyBytes, err := libp2pPrivKey.Raw()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting LibP2P private key bytes: %v", err)
	}

	// Get real IP address to replace loopback addresses
	ipAddress, err := networking.GetLANIP()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get LAN IP address: %v", err)
	}

	// Replace loopback addresses in URL with real IP
	defraUrl := cfg.DefraDB.Url
	defraUrl = strings.Replace(defraUrl, "http://localhost", ipAddress, 1)
	defraUrl = strings.Replace(defraUrl, "http://127.0.0.1", ipAddress, 1)
	defraUrl = strings.Replace(defraUrl, "localhost", ipAddress, 1)
	defraUrl = strings.Replace(defraUrl, "127.0.0.1", ipAddress, 1)

	// Replace loopback addresses in listen address with real IP
	listenAddress := cfg.DefraDB.P2P.ListenAddr
	if len(listenAddress) > 0 {
		listenAddress = strings.Replace(listenAddress, "127.0.0.1", ipAddress, 1)
		listenAddress = strings.Replace(listenAddress, "localhost", ipAddress, 1)
	}

	// Create defra node options using builder pattern
	nb := options.Node().
		SetDisableAPI(false).
		SetDisableP2P(false) // Enable P2P networking
	nb.P2P().SetEnablePubSub(true)
	nb.Store().SetPath(cfg.DefraDB.Store.Path)
	nb.HTTP().SetAddress(defraUrl)
	nb.DB().SetNodeIdentity(nodeIdentity)

	// Apply badger memory configuration if specified
	var storeOpts []func(*options.NodeOptions)
	if cfg.DefraDB.Store.BlockCacheMB > 0 {
		size := cfg.DefraDB.Store.BlockCacheMB << 20
		storeOpts = append(storeOpts, func(o *options.NodeOptions) { o.Store.BadgerBlockCacheSize = size })
		logger.Sugar.Infof("Badger block cache size: %dMB", cfg.DefraDB.Store.BlockCacheMB)
	}
	if cfg.DefraDB.Store.MemTableMB > 0 {
		size := cfg.DefraDB.Store.MemTableMB << 20
		storeOpts = append(storeOpts, func(o *options.NodeOptions) { o.Store.BadgerMemTableSize = size })
		logger.Sugar.Infof("Badger memtable size: %dMB", cfg.DefraDB.Store.MemTableMB)
	}
	if cfg.DefraDB.Store.IndexCacheMB > 0 {
		size := cfg.DefraDB.Store.IndexCacheMB << 20
		storeOpts = append(storeOpts, func(o *options.NodeOptions) { o.Store.BadgerIndexCacheSize = size })
		logger.Sugar.Infof("Badger index cache size: %dMB", cfg.DefraDB.Store.IndexCacheMB)
	}
	if cfg.DefraDB.Store.NumCompactors > 0 {
		n := cfg.DefraDB.Store.NumCompactors
		storeOpts = append(storeOpts, func(o *options.NodeOptions) { o.Store.BadgerNumCompactors = n })
		logger.Sugar.Infof("Badger num compactors: %d", cfg.DefraDB.Store.NumCompactors)
	}
	if cfg.DefraDB.Store.NumLevelZeroTables > 0 {
		n := cfg.DefraDB.Store.NumLevelZeroTables
		storeOpts = append(storeOpts, func(o *options.NodeOptions) { o.Store.BadgerNumLevelZeroTables = n })
		logger.Sugar.Infof("Badger L0 tables before compaction: %d", cfg.DefraDB.Store.NumLevelZeroTables)
	}
	if cfg.DefraDB.Store.NumLevelZeroTablesStall > 0 {
		n := cfg.DefraDB.Store.NumLevelZeroTablesStall
		storeOpts = append(storeOpts, func(o *options.NodeOptions) { o.Store.BadgerNumLevelZeroTablesStall = n })
		logger.Sugar.Infof("Badger L0 tables stall threshold: %d", cfg.DefraDB.Store.NumLevelZeroTablesStall)
	}
	vlogSizeMB := cfg.DefraDB.Store.ValueLogFileSizeMB
	if vlogSizeMB <= 0 {
		vlogSizeMB = 64
	}
	nb.Store().SetBadgerFileSize(vlogSizeMB << 20)
	logger.Sugar.Infof("Badger value log file size: %dMB", vlogSizeMB)

	// Add P2P configuration options
	if len(listenAddress) > 0 {
		nb.P2P().SetListenAddresses(listenAddress)
		logger.Sugar.Infof("P2P Listen Address configured: %s", listenAddress)
	}

	if len(libp2pKeyBytes) > 0 {
		nb.P2P().SetPrivateKey(libp2pKeyBytes)
		logger.Sugar.Info("P2P Private Key configured for consistent peer ID")
	}

	// Collect all options: builder + badger extras + user-provided options
	allOpts := []options.Enumerable[options.NodeOptions]{nb}
	if len(storeOpts) > 0 {
		allOpts = append(allOpts, enumerable.New(storeOpts))
	}
	allOpts = append(allOpts, nodeOpts...)

	defraNode, err := node.New(ctx, allOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create defra node: %v ", err)
	}

	if replicationFilter != nil {
		defraNode.ReplicationFilter = replicationFilter
	}

	err = defraNode.Start(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start defra node: %v ", err)
	}

	err = schemaApplier.ApplySchema(ctx, defraNode)
	if err != nil {
		if strings.Contains(err.Error(), "collection already exists") {
			logger.Sugar.Warnf("Failed to apply schema: %v\nProceeding...", err)
		} else {
			defer defraNode.Close(ctx)
			return nil, nil, fmt.Errorf("failed to apply schema: %v", err)
		}
	}

	err = defraNode.DB.CreateP2PCollections(ctx, collectionsOfInterest)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to add collections of interest %v: %w", collectionsOfInterest, err)
	}

	// Create network handler
	networkHandler := NewNetworkHandler(defraNode, cfg)

	// Conditionally start P2P networking based on config
	if cfg.DefraDB.P2P.Enabled {
		err = networkHandler.StartNetwork()
		if err != nil {
			logger.Sugar.Warnf("Failed to start P2P network: %v", err)
			// Don't fail completely, just log the warning
		}
	} else {
		logger.Sugar.Info("🔇 P2P networking disabled by configuration")
	}

	return defraNode, networkHandler, nil
}

// A simple wrapper on StartDefraInstance that changes the configured defra store path to a temp directory for the test
func StartDefraInstanceWithTestConfig(t *testing.T, cfg *config.Config, schemaApplier SchemaApplier, collectionsOfInterest ...string) (*node.Node, error) {
	ipAddress, err := networking.GetLANIP()
	if err != nil {
		return nil, err
	}
	listenAddress := fmt.Sprintf("/ip4/%s/tcp/0", ipAddress)
	defraUrl := fmt.Sprintf("%s:0", ipAddress)
	if cfg == nil {
		cfg = DefaultConfig
	}
	cfg.DefraDB.Store.Path = t.TempDir()
	cfg.DefraDB.Url = defraUrl
	cfg.DefraDB.P2P.ListenAddr = listenAddress
	cfg.DefraDB.KeyringSecret = "testSecret"
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

	resultChan := make(chan T, 100000)

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
func marshalUnmarshal(data interface{}, target interface{}) error {
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}
	return json.Unmarshal(dataBytes, target)
}

// ====================================================================
// NEW CLIENT API - Clean alternative to StartDefraInstance
// ====================================================================

// Client provides a clean interface for DefraDB operations
type Client struct {
	node    *node.Node
	network *NetworkHandler
	config  *config.Config
}

// NewClient creates a new client instance (doesn't start anything)
func NewClient(cfg *config.Config) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	return &Client{
		config: cfg,
	}, nil
}

// Start initializes key generation, node startup, and network handler
func (c *Client) Start(ctx context.Context) error {
	if c.node != nil {
		return fmt.Errorf("client already started")
	}

	// Use the same core logic as StartDefraInstance but without schema
	c.config.DefraDB.P2P.BootstrapPeers = append(c.config.DefraDB.P2P.BootstrapPeers, requiredPeers...)
	if len(c.config.DefraDB.P2P.ListenAddr) == 0 {
		c.config.DefraDB.P2P.ListenAddr = defaultListenAddress
	}

	logger.Init(c.config.Logger.Development)

	// Use persistent identity from keyring (required, no fallback)
	nodeIdentity, err := getOrCreateNodeIdentity(c.config)
	if err != nil {
		return fmt.Errorf("error getting or creating identity: %w", err)
	}

	// Create LibP2P private key from the same identity to ensure consistent peer ID
	libp2pPrivKey, err := createLibP2PKeyFromIdentity(nodeIdentity)
	if err != nil {
		return fmt.Errorf("error creating LibP2P private key from identity: %v", err)
	}

	// Get raw bytes for P2P private key configuration
	libp2pKeyBytes, err := libp2pPrivKey.Raw()
	if err != nil {
		return fmt.Errorf("error getting LibP2P private key bytes: %v", err)
	}

	// Get real IP address to replace loopback addresses
	ipAddress, err := networking.GetLANIP()
	if err != nil {
		return fmt.Errorf("failed to get LAN IP address: %v", err)
	}

	// Replace loopback addresses in URL with real IP
	defraUrl := c.config.DefraDB.Url
	defraUrl = strings.Replace(defraUrl, "http://localhost", ipAddress, 1)
	defraUrl = strings.Replace(defraUrl, "http://127.0.0.1", ipAddress, 1)
	defraUrl = strings.Replace(defraUrl, "localhost", ipAddress, 1)
	defraUrl = strings.Replace(defraUrl, "127.0.0.1", ipAddress, 1)

	// Replace loopback addresses in listen address with real IP
	listenAddress := c.config.DefraDB.P2P.ListenAddr
	if len(listenAddress) > 0 {
		listenAddress = strings.Replace(listenAddress, "127.0.0.1", ipAddress, 1)
		listenAddress = strings.Replace(listenAddress, "localhost", ipAddress, 1)
	}

	// Create defra node options using builder pattern
	nb := options.Node().
		SetDisableAPI(false).
		SetDisableP2P(false)
	nb.P2P().SetEnablePubSub(true)
	nb.Store().SetPath(c.config.DefraDB.Store.Path)
	nb.HTTP().SetAddress(defraUrl)
	nb.DB().SetNodeIdentity(nodeIdentity)

	// Apply badger memory configuration if specified
	var storeOpts []func(*options.NodeOptions)
	if c.config.DefraDB.Store.BlockCacheMB > 0 {
		size := c.config.DefraDB.Store.BlockCacheMB << 20
		storeOpts = append(storeOpts, func(o *options.NodeOptions) { o.Store.BadgerBlockCacheSize = size })
	}
	if c.config.DefraDB.Store.MemTableMB > 0 {
		size := c.config.DefraDB.Store.MemTableMB << 20
		storeOpts = append(storeOpts, func(o *options.NodeOptions) { o.Store.BadgerMemTableSize = size })
	}
	if c.config.DefraDB.Store.IndexCacheMB > 0 {
		size := c.config.DefraDB.Store.IndexCacheMB << 20
		storeOpts = append(storeOpts, func(o *options.NodeOptions) { o.Store.BadgerIndexCacheSize = size })
	}
	if c.config.DefraDB.Store.NumCompactors > 0 {
		n := c.config.DefraDB.Store.NumCompactors
		storeOpts = append(storeOpts, func(o *options.NodeOptions) { o.Store.BadgerNumCompactors = n })
	}
	if c.config.DefraDB.Store.NumLevelZeroTables > 0 {
		n := c.config.DefraDB.Store.NumLevelZeroTables
		storeOpts = append(storeOpts, func(o *options.NodeOptions) { o.Store.BadgerNumLevelZeroTables = n })
	}
	if c.config.DefraDB.Store.NumLevelZeroTablesStall > 0 {
		n := c.config.DefraDB.Store.NumLevelZeroTablesStall
		storeOpts = append(storeOpts, func(o *options.NodeOptions) { o.Store.BadgerNumLevelZeroTablesStall = n })
	}
	{
		vlogSizeMB := c.config.DefraDB.Store.ValueLogFileSizeMB
		if vlogSizeMB <= 0 {
			vlogSizeMB = 64
		}
		nb.Store().SetBadgerFileSize(vlogSizeMB << 20)
	}

	if len(listenAddress) > 0 {
		nb.P2P().SetListenAddresses(listenAddress)
		logger.Sugar.Infof("P2P Listen Address configured: %s", listenAddress)
	}

	if len(libp2pKeyBytes) > 0 {
		nb.P2P().SetPrivateKey(libp2pKeyBytes)
		logger.Sugar.Info("P2P Private Key configured for consistent peer ID")
	}

	// Collect all options
	allOpts := []options.Enumerable[options.NodeOptions]{nb}
	if len(storeOpts) > 0 {
		allOpts = append(allOpts, enumerable.New(storeOpts))
	}

	c.node, err = node.New(ctx, allOpts...)
	if err != nil {
		return fmt.Errorf("failed to create defra node: %v", err)
	}

	err = c.node.Start(ctx)
	if err != nil {
		return fmt.Errorf("failed to start defra node: %v", err)
	}

	// Create network handler
	c.network = NewNetworkHandler(c.node, c.config)

	if c.config.DefraDB.P2P.Enabled {
		err = c.network.StartNetwork()
		if err != nil {
			logger.Sugar.Warnf("Failed to start P2P network: %v", err)
		}
	} else {
		logger.Sugar.Info("🔇 P2P networking disabled by configuration")
	}

	return nil
}

// Stop cleanly shuts down the client
func (c *Client) Stop(ctx context.Context) error {
	if c.node == nil {
		return nil
	}

	err := c.node.Close(ctx)
	c.node = nil
	c.network = nil
	return err
}

// ApplySchema applies a GraphQL schema string to the started node
func (c *Client) ApplySchema(ctx context.Context, schema string) error {
	if c.node == nil {
		return fmt.Errorf("client must be started before applying schema")
	}

	if len(schema) == 0 {
		return fmt.Errorf("schema cannot be empty")
	}

	_, err := c.node.DB.AddSchema(ctx, schema)
	if err != nil {
		if strings.Contains(err.Error(), "collection already exists") {
			logger.Sugar.Warnf("Failed to apply schema: %v\nProceeding...", err)
			return nil
		}
		return fmt.Errorf("failed to apply schema: %v", err)
	}

	return nil
}

// GetNode returns the underlying DefraDB node
func (c *Client) GetNode() *node.Node {
	return c.node
}

// GetNetworkHandler returns the network handler
func (c *Client) GetNetworkHandler() *NetworkHandler {
	return c.network
}