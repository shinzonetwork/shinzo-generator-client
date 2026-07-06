package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_ValidYAML(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	configContent := `
defradb:
  url: "http://localhost:9181"
  keyring_secret: "test_secret"
  p2p:
    enabled: true
    bootstrap_peers: ["peer1", "peer2"]
    listen_addr: "/ip4/0.0.0.0/tcp/9171"
  store:
    path: "/tmp/defra"

geth:
  node_url: "http://localhost:8545"

indexer:
  start_height: 1000

logger:
  development: true
`

	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	assert.Equal(t, "http://localhost:9181", cfg.DefraDB.URL, "DefraDB.URL")
	assert.Equal(t, "test_secret", cfg.DefraDB.KeyringSecret, "DefraDB.KeyringSecret")
	assert.Len(t, cfg.DefraDB.P2P.BootstrapPeers, 2, "P2P.BootstrapPeers")
	assert.NotEmpty(t, cfg.Geth.NodeURL, "Geth.NodeURL")
	assert.Equal(t, 1000, cfg.Indexer.StartHeight, "Indexer.StartHeight")
}

func TestLoadConfig_InvalidPath(t *testing.T) {
	t.Parallel()
	_, err := LoadConfig("/nonexistent/path/config.yaml")
	require.Error(t, err)
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "invalid_config.yaml")

	require.NoError(t, os.WriteFile(configPath, []byte("defradb:\n  url: \"invalid yaml\n"), 0o600))

	_, err := LoadConfig(configPath)
	require.Error(t, err)
}

func TestDefraDBEmbeddedUrlMatrix(t *testing.T) {
	tests := []struct {
		name        string
		embedded    bool
		url         string
		shouldError bool
	}{
		{"embedded true with url", true, "http://localhost:9181", false},
		{"embedded false with url", false, "http://localhost:9181", false},
		{"embedded false with empty url should error", false, "", true},
		{"embedded true with empty url", true, "", false},
		{"embedded false with whitespace url should error", false, "   ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear env to avoid interference
			t.Setenv("DEFRADB_URL", "")
			t.Setenv("DEFRADB_HOST", "")
			t.Setenv("DEFRADB_PORT", "")

			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "config.yaml")
			configContent := "defradb:\n  url: \"" + tt.url + "\"\n  embedded: " + strconv.FormatBool(tt.embedded) + "\nindexer:\n  start_height: 0\n"

			require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

			_, err := LoadConfig(configPath)
			if tt.shouldError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDefraDBConfig_Host(t *testing.T) {
	t.Parallel()
	cfg := &DefraDBConfig{URL: "http://localhost:9181"}
	assert.Equal(t, "http://localhost:9181", cfg.Host())
}

func TestApplyDefaults_AllZeroValues(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	applyDefaults(cfg)

	assert.Equal(t, 8, cfg.Indexer.ConcurrentBlocks, "ConcurrentBlocks")
	assert.Equal(t, 16, cfg.Indexer.ReceiptWorkers, "ReceiptWorkers")
	assert.Equal(t, 1000, cfg.Indexer.MaxDocsPerTxn, "MaxDocsPerTxn")
	assert.Equal(t, 8080, cfg.Indexer.HealthServerPort, "HealthServerPort")
	assert.Equal(t, 100, cfg.Indexer.StartBuffer, "StartBuffer")
}

func TestApplyDefaults_PresetValuesPreserved(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.Indexer.ConcurrentBlocks = 4
	cfg.Indexer.ReceiptWorkers = 8
	cfg.Indexer.MaxDocsPerTxn = 500
	cfg.Indexer.HealthServerPort = 9090
	cfg.Indexer.StartBuffer = 50

	applyDefaults(cfg)

	assert.Equal(t, 4, cfg.Indexer.ConcurrentBlocks, "ConcurrentBlocks")
	assert.Equal(t, 8, cfg.Indexer.ReceiptWorkers, "ReceiptWorkers")
	assert.Equal(t, 500, cfg.Indexer.MaxDocsPerTxn, "MaxDocsPerTxn")
	assert.Equal(t, 9090, cfg.Indexer.HealthServerPort, "HealthServerPort")
	assert.Equal(t, 50, cfg.Indexer.StartBuffer, "StartBuffer")
}

func TestValidateConfig_NegativeStartHeight(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.DefraDB.Embedded = true
	cfg.Indexer.StartHeight = -1

	err := validateConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start_height")
}

func TestValidateConfig_ExternalEmptyUrl(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.DefraDB.Embedded = false
	cfg.DefraDB.URL = ""

	err := validateConfig(cfg)
	require.Error(t, err)
}

func TestValidateConfig_Valid(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.DefraDB.Embedded = true
	cfg.Indexer.StartHeight = 0
	cfg.Indexer.SchemaAuthMode = constants.SchemaAuthModeToken

	require.NoError(t, validateConfig(cfg))
}

func TestApplyEnvOverrides_DefraDBUrl(t *testing.T) {
	cfg := &Config{}
	t.Setenv("DEFRADB_URL", "http://custom:9181")
	applyEnvOverrides(cfg)
	assert.Equal(t, "http://custom:9181", cfg.DefraDB.URL)
}

func TestApplyEnvOverrides_DefraDBHost(t *testing.T) {
	tests := []struct {
		name    string
		port    string
		wantURL string
	}{
		{"with port", "1234", "http://myhost:1234"},
		{"without port defaults to 9181", "", "http://myhost:9181"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			// DEFRADB_URL must be unset for HOST to take effect
			t.Setenv("DEFRADB_URL", "")
			t.Setenv("DEFRADB_HOST", "myhost")
			t.Setenv("DEFRADB_PORT", tt.port)
			applyEnvOverrides(cfg)
			assert.Equal(t, tt.wantURL, cfg.DefraDB.URL)
		})
	}
}

func TestApplyEnvOverrides_DefraDBKeyringSecret(t *testing.T) {
	cfg := &Config{}
	t.Setenv("DEFRADB_KEYRING_SECRET", "mysecret")
	applyEnvOverrides(cfg)
	assert.Equal(t, "mysecret", cfg.DefraDB.KeyringSecret)
}

func TestApplyEnvOverrides_P2PConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("DEFRADB_P2P_ENABLED", "true")
	t.Setenv("DEFRADB_P2P_LISTEN_ADDR", "/ip4/0.0.0.0/tcp/9999")
	t.Setenv("DEFRADB_P2P_ACCEPT_INCOMING", "true")
	applyEnvOverrides(cfg)

	assert.True(t, cfg.DefraDB.P2P.Enabled, "P2P.Enabled")
	assert.Equal(t, "/ip4/0.0.0.0/tcp/9999", cfg.DefraDB.P2P.ListenAddr, "P2P.ListenAddr")
	assert.True(t, cfg.DefraDB.P2P.AcceptIncoming, "P2P.AcceptIncoming")
}

func TestApplyEnvOverrides_P2P_InvalidBool(t *testing.T) {
	cfg := &Config{}
	t.Setenv("DEFRADB_P2P_ENABLED", "not_a_bool")
	t.Setenv("DEFRADB_P2P_ACCEPT_INCOMING", "invalid")
	applyEnvOverrides(cfg)

	// Should be silently ignored
	assert.False(t, cfg.DefraDB.P2P.Enabled, "P2P.Enabled should remain false for invalid bool")
	assert.False(t, cfg.DefraDB.P2P.AcceptIncoming, "P2P.AcceptIncoming should remain false for invalid bool")
}

func TestApplyEnvOverrides_StoreConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("DEFRADB_STORE_PATH", "/custom/path")
	t.Setenv("DEFRADB_BLOCK_CACHE_MB", "256")
	t.Setenv("DEFRADB_MEMTABLE_MB", "128")
	t.Setenv("DEFRADB_INDEX_CACHE_MB", "64")
	t.Setenv("DEFRADB_NUM_COMPACTORS", "4")
	t.Setenv("DEFRADB_NUM_LEVEL_ZERO_TABLES", "10")
	t.Setenv("DEFRADB_NUM_LEVEL_ZERO_TABLES_STALL", "20")
	applyEnvOverrides(cfg)

	assert.Equal(t, "/custom/path", cfg.DefraDB.Store.Path, "Store.Path")
	assert.Equal(t, int64(256), cfg.DefraDB.Store.BlockCacheMB, "Store.BlockCacheMB")
	assert.Equal(t, int64(128), cfg.DefraDB.Store.MemTableMB, "Store.MemTableMB")
	assert.Equal(t, int64(64), cfg.DefraDB.Store.IndexCacheMB, "Store.IndexCacheMB")
	assert.Equal(t, 4, cfg.DefraDB.Store.NumCompactors, "Store.NumCompactors")
	assert.Equal(t, 10, cfg.DefraDB.Store.NumLevelZeroTables, "Store.NumLevelZeroTables")
	assert.Equal(t, 20, cfg.DefraDB.Store.NumLevelZeroTablesStall, "Store.NumLevelZeroTablesStall")
}

func TestApplyEnvOverrides_StoreConfig_InvalidValues(t *testing.T) {
	cfg := &Config{}
	t.Setenv("DEFRADB_BLOCK_CACHE_MB", "not_a_number")
	t.Setenv("DEFRADB_MEMTABLE_MB", "invalid")
	t.Setenv("DEFRADB_INDEX_CACHE_MB", "bad")
	t.Setenv("DEFRADB_NUM_COMPACTORS", "abc")
	t.Setenv("DEFRADB_NUM_LEVEL_ZERO_TABLES", "xyz")
	t.Setenv("DEFRADB_NUM_LEVEL_ZERO_TABLES_STALL", "zzz")
	applyEnvOverrides(cfg)

	// All should remain at zero values
	assert.Zero(t, cfg.DefraDB.Store.BlockCacheMB, "BlockCacheMB should remain 0 for invalid value")
	assert.Zero(t, cfg.DefraDB.Store.MemTableMB, "MemTableMB should remain 0 for invalid value")
	assert.Zero(t, cfg.DefraDB.Store.IndexCacheMB, "IndexCacheMB should remain 0 for invalid value")
}

func TestApplyEnvOverrides_GethConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("GETH_RPC_URL", "http://geth:8545")
	t.Setenv("GETH_WS_URL", "ws://geth:8546")
	t.Setenv("GETH_API_KEY", "myapikey")
	t.Setenv("GETH_API_KEY_TYPE", "X-Api-Key")
	applyEnvOverrides(cfg)

	assert.Equal(t, "http://geth:8545", cfg.Geth.NodeURL, "Geth.NodeURL")
	assert.Equal(t, "ws://geth:8546", cfg.Geth.WsURL, "Geth.WsURL")
	assert.Equal(t, "myapikey", cfg.Geth.APIKey, "Geth.APIKey")
}

func TestApplyEnvOverrides_IndexerConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("INDEXER_START_HEIGHT", "5000")
	t.Setenv("INDEXER_CONCURRENT_BLOCKS", "16")
	t.Setenv("INDEXER_RECEIPT_WORKERS", "32")
	t.Setenv("INDEXER_MAX_DOCS_PER_TXN", "2000")
	t.Setenv("INDEXER_BLOCKS_PER_MINUTE", "60")
	t.Setenv("INDEXER_HEALTH_SERVER_PORT", "9090")
	t.Setenv("INDEXER_START_BUFFER", "200")
	applyEnvOverrides(cfg)

	assert.Equal(t, 5000, cfg.Indexer.StartHeight, "Indexer.StartHeight")
	assert.Equal(t, 16, cfg.Indexer.ConcurrentBlocks, "Indexer.ConcurrentBlocks")
	assert.Equal(t, 32, cfg.Indexer.ReceiptWorkers, "Indexer.ReceiptWorkers")
	assert.Equal(t, 2000, cfg.Indexer.MaxDocsPerTxn, "Indexer.MaxDocsPerTxn")
	assert.Equal(t, 60, cfg.Indexer.BlocksPerMinute, "Indexer.BlocksPerMinute")
	assert.Equal(t, 9090, cfg.Indexer.HealthServerPort, "Indexer.HealthServerPort")
	assert.Equal(t, 200, cfg.Indexer.StartBuffer, "Indexer.StartBuffer")
}

func TestApplyEnvOverrides_IndexerConfig_InvalidValues(t *testing.T) {
	cfg := &Config{}
	cfg.Indexer.StartHeight = 1000
	t.Setenv("INDEXER_START_HEIGHT", "not_a_number")
	t.Setenv("INDEXER_CONCURRENT_BLOCKS", "invalid")
	t.Setenv("INDEXER_RECEIPT_WORKERS", "bad")
	t.Setenv("INDEXER_MAX_DOCS_PER_TXN", "wrong")
	t.Setenv("INDEXER_BLOCKS_PER_MINUTE", "nope")
	t.Setenv("INDEXER_HEALTH_SERVER_PORT", "xxx")
	t.Setenv("INDEXER_START_BUFFER", "yyy")
	applyEnvOverrides(cfg)

	assert.Equal(t, 1000, cfg.Indexer.StartHeight, "StartHeight should be preserved as 1000")
}

func TestApplyEnvOverrides_LoggerConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("LOGGER_DEBUG", "true")
	applyEnvOverrides(cfg)

	assert.True(t, cfg.Logger.Development, "Logger.Development should be true")
}

func TestApplyEnvOverrides_LoggerConfig_Invalid(t *testing.T) {
	cfg := &Config{}
	t.Setenv("LOGGER_DEBUG", "not_a_bool")
	applyEnvOverrides(cfg)

	assert.False(t, cfg.Logger.Development, "Logger.Development should remain false for invalid bool")
}

func TestApplyEnvOverrides_PrunerConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("PRUNER_ENABLED", "true")
	t.Setenv("PRUNER_MAX_BLOCKS", "1000")
	t.Setenv("PRUNER_PRUNE_THRESHOLD", "100")
	t.Setenv("PRUNER_INTERVAL_SECONDS", "30")
	applyEnvOverrides(cfg)

	assert.True(t, cfg.Pruner.Enabled, "Pruner.Enabled")
	assert.Equal(t, int64(1000), cfg.Pruner.MaxBlocks, "Pruner.MaxBlocks")
	assert.Equal(t, int64(100), cfg.Pruner.PruneThreshold, "Pruner.PruneThreshold")
	assert.Equal(t, 30, cfg.Pruner.IntervalSeconds, "Pruner.IntervalSeconds")
}

func TestApplyEnvOverrides_PrunerConfig_Invalid(t *testing.T) {
	cfg := &Config{}
	t.Setenv("PRUNER_ENABLED", "invalid")
	t.Setenv("PRUNER_MAX_BLOCKS", "not_num")
	t.Setenv("PRUNER_PRUNE_THRESHOLD", "bad")
	t.Setenv("PRUNER_INTERVAL_SECONDS", "nope")
	applyEnvOverrides(cfg)

	assert.False(t, cfg.Pruner.Enabled, "Pruner.Enabled should remain false")
}

func TestApplyEnvOverrides_SnapshotConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("SNAPSHOT_ENABLED", "true")
	t.Setenv("SNAPSHOT_DIR", "/custom/snapshots")
	t.Setenv("SNAPSHOT_BLOCKS_PER_FILE", "5000")
	t.Setenv("SNAPSHOT_INTERVAL_SECONDS", "120")
	applyEnvOverrides(cfg)

	assert.True(t, cfg.Snapshot.Enabled, "Snapshot.Enabled")
	assert.Equal(t, "/custom/snapshots", cfg.Snapshot.Dir, "Snapshot.Dir")
	assert.Equal(t, int64(5000), cfg.Snapshot.BlocksPerFile, "Snapshot.BlocksPerFile")
	assert.Equal(t, 120, cfg.Snapshot.IntervalSeconds, "Snapshot.IntervalSeconds")
}

func TestApplyEnvOverrides_SnapshotConfig_Invalid(t *testing.T) {
	cfg := &Config{}
	t.Setenv("SNAPSHOT_ENABLED", "notbool")
	t.Setenv("SNAPSHOT_BLOCKS_PER_FILE", "invalid")
	t.Setenv("SNAPSHOT_INTERVAL_SECONDS", "bad")
	applyEnvOverrides(cfg)

	assert.False(t, cfg.Snapshot.Enabled, "Snapshot.Enabled should remain false")
}

func TestLoadConfig_EnvironmentOverrides_Integration(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	configContent := `
defradb:
  url: "http://localhost:9181"
  keyring_secret: "pingpong"
indexer:
  start_height: 1000
`

	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	t.Setenv("INDEXER_START_HEIGHT", "2000")
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	assert.Equal(t, 2000, cfg.Indexer.StartHeight, "Indexer.StartHeight")
}

func TestLoadConfig_InvalidEnvironmentValues(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	configContent := `
defradb:
  embedded: true
indexer:
  start_height: 1000
`

	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	t.Setenv("INDEXER_START_HEIGHT", "not_a_number")
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	assert.Equal(t, 1000, cfg.Indexer.StartHeight, "StartHeight should be preserved as 1000")
}

func TestSchemaAuthModeDefault(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	applyDefaults(cfg)
	assert.Equal(t, constants.SchemaAuthModeToken, cfg.Indexer.SchemaAuthMode, "SchemaAuthMode default")
}

func TestSchemaAuthModeEnvOverride(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     string
	}{
		{"none", "none", constants.SchemaAuthModeNone},
		{"token", "token", constants.SchemaAuthModeToken},
		{"mtls", "mtls", constants.SchemaAuthModeMTLS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			t.Setenv("SCHEMA_AUTH_MODE", tt.envValue)
			applySchemaEnvOverrides(cfg)
			assert.Equal(t, tt.want, cfg.Indexer.SchemaAuthMode, "SchemaAuthMode")
		})
	}
}

func TestSchemaAPIKeysEnvOverride(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		want    []string
		wantNil bool
	}{
		{"multiple", "key1, key2 ,key3", []string{"key1", "key2", "key3"}, false},
		{"trims and drops empty", "a, b ,, c", []string{"a", "b", "c"}, false},
		{"single key", "onlykey", []string{"onlykey"}, false},
		{"empty string", "", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			t.Setenv("SCHEMA_API_KEYS", tt.envVal)
			applySchemaEnvOverrides(cfg)
			if tt.wantNil {
				assert.Nil(t, cfg.Indexer.SchemaAPIKeys, "SchemaAPIKeys should be nil")
			} else {
				assert.Equal(t, tt.want, cfg.Indexer.SchemaAPIKeys, "SchemaAPIKeys")
			}
		})
	}
}

func TestValidateConfig_AuthModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		mode        string
		shouldError bool
		errContains string
	}{
		{"none", constants.SchemaAuthModeNone, false, ""},
		{"token", constants.SchemaAuthModeToken, false, ""},
		{"mtls", constants.SchemaAuthModeMTLS, false, ""},
		{"invalid", "invalid", true, "invalid SCHEMA_AUTH_MODE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			cfg.DefraDB.Embedded = true
			cfg.Indexer.SchemaAuthMode = tt.mode
			err := validateConfig(cfg)
			if tt.shouldError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
