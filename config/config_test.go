package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLoadConfig_ValidYAML(t *testing.T) {
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

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config file: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.DefraDB.Url != "http://localhost:9181" {
		t.Errorf("Expected url 'http://localhost:9181', got '%s'", cfg.DefraDB.Url)
	}
	if cfg.DefraDB.KeyringSecret != "test_secret" {
		t.Errorf("Expected keyring_secret 'test_secret', got '%s'", cfg.DefraDB.KeyringSecret)
	}
	if len(cfg.DefraDB.P2P.BootstrapPeers) != 2 {
		t.Errorf("Expected 2 bootstrap peers, got %d", len(cfg.DefraDB.P2P.BootstrapPeers))
	}
	if cfg.Geth.NodeURL == "" {
		t.Error("Expected non-empty node_url")
	}
	if cfg.Indexer.StartHeight != 1000 {
		t.Errorf("Expected start_height 1000, got %d", cfg.Indexer.StartHeight)
	}
}

func TestLoadConfig_InvalidPath(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("Expected error for nonexistent config file")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "invalid_config.yaml")

	if err := os.WriteFile(configPath, []byte("defradb:\n  url: \"invalid yaml\n"), 0644); err != nil {
		t.Fatalf("Failed to write test config file: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Error("Expected error for invalid YAML")
	}
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

			if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
				t.Fatalf("Failed to write test config file: %v", err)
			}

			_, err := LoadConfig(configPath)
			if tt.shouldError && err == nil {
				t.Fatalf("expected error for embedded=%v url='%s'", tt.embedded, tt.url)
			}
			if !tt.shouldError && err != nil {
				t.Fatalf("unexpected error for embedded=%v url='%s': %v", tt.embedded, tt.url, err)
			}
		})
	}
}

func TestDefraDBConfig_Host(t *testing.T) {
	cfg := &DefraDBConfig{Url: "http://localhost:9181"}
	if cfg.Host() != "http://localhost:9181" {
		t.Errorf("Host() = %q, want %q", cfg.Host(), "http://localhost:9181")
	}
}

func TestApplyDefaults_AllZeroValues(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)

	if cfg.Indexer.ConcurrentBlocks != 8 {
		t.Errorf("ConcurrentBlocks = %d, want 8", cfg.Indexer.ConcurrentBlocks)
	}
	if cfg.Indexer.ReceiptWorkers != 16 {
		t.Errorf("ReceiptWorkers = %d, want 16", cfg.Indexer.ReceiptWorkers)
	}
	if cfg.Indexer.MaxDocsPerTxn != 1000 {
		t.Errorf("MaxDocsPerTxn = %d, want 1000", cfg.Indexer.MaxDocsPerTxn)
	}
	if cfg.Indexer.HealthServerPort != 8080 {
		t.Errorf("HealthServerPort = %d, want 8080", cfg.Indexer.HealthServerPort)
	}
	if cfg.Indexer.StartBuffer != 100 {
		t.Errorf("StartBuffer = %d, want 100", cfg.Indexer.StartBuffer)
	}
}

func TestApplyDefaults_PresetValuesPreserved(t *testing.T) {
	cfg := &Config{}
	cfg.Indexer.ConcurrentBlocks = 4
	cfg.Indexer.ReceiptWorkers = 8
	cfg.Indexer.MaxDocsPerTxn = 500
	cfg.Indexer.HealthServerPort = 9090
	cfg.Indexer.StartBuffer = 50

	applyDefaults(cfg)

	if cfg.Indexer.ConcurrentBlocks != 4 {
		t.Errorf("ConcurrentBlocks should be preserved as 4, got %d", cfg.Indexer.ConcurrentBlocks)
	}
	if cfg.Indexer.ReceiptWorkers != 8 {
		t.Errorf("ReceiptWorkers should be preserved as 8, got %d", cfg.Indexer.ReceiptWorkers)
	}
	if cfg.Indexer.MaxDocsPerTxn != 500 {
		t.Errorf("MaxDocsPerTxn should be preserved as 500, got %d", cfg.Indexer.MaxDocsPerTxn)
	}
	if cfg.Indexer.HealthServerPort != 9090 {
		t.Errorf("HealthServerPort should be preserved as 9090, got %d", cfg.Indexer.HealthServerPort)
	}
	if cfg.Indexer.StartBuffer != 50 {
		t.Errorf("StartBuffer should be preserved as 50, got %d", cfg.Indexer.StartBuffer)
	}
}

func TestValidateConfig_NegativeStartHeight(t *testing.T) {
	cfg := &Config{}
	cfg.DefraDB.Embedded = true
	cfg.Indexer.StartHeight = -1

	err := validateConfig(cfg)
	if err == nil {
		t.Error("expected error for negative start_height")
	}
	if !strings.Contains(err.Error(), "start_height") {
		t.Errorf("error should mention start_height, got: %v", err)
	}
}

func TestValidateConfig_ExternalEmptyUrl(t *testing.T) {
	cfg := &Config{}
	cfg.DefraDB.Embedded = false
	cfg.DefraDB.Url = ""

	err := validateConfig(cfg)
	if err == nil {
		t.Error("expected error for external DefraDB with empty url")
	}
}

func TestValidateConfig_Valid(t *testing.T) {
	cfg := &Config{}
	cfg.DefraDB.Embedded = true
	cfg.Indexer.StartHeight = 0

	if err := validateConfig(cfg); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestApplyEnvOverrides_DefraDBUrl(t *testing.T) {
	cfg := &Config{}
	t.Setenv("DEFRADB_URL", "http://custom:9181")
	applyEnvOverrides(cfg)
	if cfg.DefraDB.Url != "http://custom:9181" {
		t.Errorf("DEFRADB_URL override failed: got %q", cfg.DefraDB.Url)
	}
}

func TestApplyEnvOverrides_DefraDBHost_WithPort(t *testing.T) {
	cfg := &Config{}
	// DEFRADB_URL must be unset for HOST to take effect
	t.Setenv("DEFRADB_URL", "")
	t.Setenv("DEFRADB_HOST", "myhost")
	t.Setenv("DEFRADB_PORT", "1234")
	applyEnvOverrides(cfg)
	if cfg.DefraDB.Url != "http://myhost:1234" {
		t.Errorf("DEFRADB_HOST+PORT override failed: got %q", cfg.DefraDB.Url)
	}
}

func TestApplyEnvOverrides_DefraDBHost_WithoutPort(t *testing.T) {
	cfg := &Config{}
	t.Setenv("DEFRADB_URL", "")
	t.Setenv("DEFRADB_HOST", "myhost")
	t.Setenv("DEFRADB_PORT", "")
	applyEnvOverrides(cfg)
	if cfg.DefraDB.Url != "http://myhost:9181" {
		t.Errorf("DEFRADB_HOST without PORT should default to 9181, got %q", cfg.DefraDB.Url)
	}
}

func TestApplyEnvOverrides_DefraDBKeyringSecret(t *testing.T) {
	cfg := &Config{}
	t.Setenv("DEFRADB_KEYRING_SECRET", "mysecret")
	applyEnvOverrides(cfg)
	if cfg.DefraDB.KeyringSecret != "mysecret" {
		t.Errorf("DEFRADB_KEYRING_SECRET override failed: got %q", cfg.DefraDB.KeyringSecret)
	}
}

func TestApplyEnvOverrides_P2PConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("DEFRADB_P2P_ENABLED", "true")
	t.Setenv("DEFRADB_P2P_LISTEN_ADDR", "/ip4/0.0.0.0/tcp/9999")
	t.Setenv("DEFRADB_P2P_ACCEPT_INCOMING", "true")
	applyEnvOverrides(cfg)

	if !cfg.DefraDB.P2P.Enabled {
		t.Error("P2P.Enabled should be true")
	}
	if cfg.DefraDB.P2P.ListenAddr != "/ip4/0.0.0.0/tcp/9999" {
		t.Errorf("P2P.ListenAddr = %q", cfg.DefraDB.P2P.ListenAddr)
	}
	if !cfg.DefraDB.P2P.AcceptIncoming {
		t.Error("P2P.AcceptIncoming should be true")
	}
}

func TestApplyEnvOverrides_P2P_InvalidBool(t *testing.T) {
	cfg := &Config{}
	t.Setenv("DEFRADB_P2P_ENABLED", "not_a_bool")
	t.Setenv("DEFRADB_P2P_ACCEPT_INCOMING", "invalid")
	applyEnvOverrides(cfg)

	// Should be silently ignored
	if cfg.DefraDB.P2P.Enabled {
		t.Error("P2P.Enabled should remain false for invalid bool")
	}
	if cfg.DefraDB.P2P.AcceptIncoming {
		t.Error("P2P.AcceptIncoming should remain false for invalid bool")
	}
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

	if cfg.DefraDB.Store.Path != "/custom/path" {
		t.Errorf("Store.Path = %q", cfg.DefraDB.Store.Path)
	}
	if cfg.DefraDB.Store.BlockCacheMB != 256 {
		t.Errorf("Store.BlockCacheMB = %d", cfg.DefraDB.Store.BlockCacheMB)
	}
	if cfg.DefraDB.Store.MemTableMB != 128 {
		t.Errorf("Store.MemTableMB = %d", cfg.DefraDB.Store.MemTableMB)
	}
	if cfg.DefraDB.Store.IndexCacheMB != 64 {
		t.Errorf("Store.IndexCacheMB = %d", cfg.DefraDB.Store.IndexCacheMB)
	}
	if cfg.DefraDB.Store.NumCompactors != 4 {
		t.Errorf("Store.NumCompactors = %d", cfg.DefraDB.Store.NumCompactors)
	}
	if cfg.DefraDB.Store.NumLevelZeroTables != 10 {
		t.Errorf("Store.NumLevelZeroTables = %d", cfg.DefraDB.Store.NumLevelZeroTables)
	}
	if cfg.DefraDB.Store.NumLevelZeroTablesStall != 20 {
		t.Errorf("Store.NumLevelZeroTablesStall = %d", cfg.DefraDB.Store.NumLevelZeroTablesStall)
	}
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
	if cfg.DefraDB.Store.BlockCacheMB != 0 {
		t.Error("BlockCacheMB should remain 0 for invalid value")
	}
	if cfg.DefraDB.Store.MemTableMB != 0 {
		t.Error("MemTableMB should remain 0 for invalid value")
	}
	if cfg.DefraDB.Store.IndexCacheMB != 0 {
		t.Error("IndexCacheMB should remain 0 for invalid value")
	}
}

func TestApplyEnvOverrides_GethConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("GETH_RPC_URL", "http://geth:8545")
	t.Setenv("GETH_WS_URL", "ws://geth:8546")
	t.Setenv("GETH_API_KEY", "myapikey")
	applyEnvOverrides(cfg)

	if cfg.Geth.NodeURL != "http://geth:8545" {
		t.Errorf("Geth.NodeURL = %q", cfg.Geth.NodeURL)
	}
	if cfg.Geth.WsURL != "ws://geth:8546" {
		t.Errorf("Geth.WsURL = %q", cfg.Geth.WsURL)
	}
	if cfg.Geth.APIKey != "myapikey" {
		t.Errorf("Geth.APIKey = %q", cfg.Geth.APIKey)
	}
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

	if cfg.Indexer.StartHeight != 5000 {
		t.Errorf("Indexer.StartHeight = %d", cfg.Indexer.StartHeight)
	}
	if cfg.Indexer.ConcurrentBlocks != 16 {
		t.Errorf("Indexer.ConcurrentBlocks = %d", cfg.Indexer.ConcurrentBlocks)
	}
	if cfg.Indexer.ReceiptWorkers != 32 {
		t.Errorf("Indexer.ReceiptWorkers = %d", cfg.Indexer.ReceiptWorkers)
	}
	if cfg.Indexer.MaxDocsPerTxn != 2000 {
		t.Errorf("Indexer.MaxDocsPerTxn = %d", cfg.Indexer.MaxDocsPerTxn)
	}
	if cfg.Indexer.BlocksPerMinute != 60 {
		t.Errorf("Indexer.BlocksPerMinute = %d", cfg.Indexer.BlocksPerMinute)
	}
	if cfg.Indexer.HealthServerPort != 9090 {
		t.Errorf("Indexer.HealthServerPort = %d", cfg.Indexer.HealthServerPort)
	}
	if cfg.Indexer.StartBuffer != 200 {
		t.Errorf("Indexer.StartBuffer = %d", cfg.Indexer.StartBuffer)
	}
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

	if cfg.Indexer.StartHeight != 1000 {
		t.Errorf("StartHeight should be preserved as 1000, got %d", cfg.Indexer.StartHeight)
	}
}

func TestApplyEnvOverrides_LoggerConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("LOGGER_DEBUG", "true")
	applyEnvOverrides(cfg)

	if !cfg.Logger.Development {
		t.Error("Logger.Development should be true")
	}
}

func TestApplyEnvOverrides_LoggerConfig_Invalid(t *testing.T) {
	cfg := &Config{}
	t.Setenv("LOGGER_DEBUG", "not_a_bool")
	applyEnvOverrides(cfg)

	if cfg.Logger.Development {
		t.Error("Logger.Development should remain false for invalid bool")
	}
}

func TestApplyEnvOverrides_PrunerConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("PRUNER_ENABLED", "true")
	t.Setenv("PRUNER_MAX_BLOCKS", "1000")
	t.Setenv("PRUNER_PRUNE_THRESHOLD", "100")
	t.Setenv("PRUNER_INTERVAL_SECONDS", "30")
	applyEnvOverrides(cfg)

	if !cfg.Pruner.Enabled {
		t.Error("Pruner.Enabled should be true")
	}
	if cfg.Pruner.MaxBlocks != 1000 {
		t.Errorf("Pruner.MaxBlocks = %d", cfg.Pruner.MaxBlocks)
	}
	if cfg.Pruner.PruneThreshold != 100 {
		t.Errorf("Pruner.PruneThreshold = %d", cfg.Pruner.PruneThreshold)
	}
	if cfg.Pruner.IntervalSeconds != 30 {
		t.Errorf("Pruner.IntervalSeconds = %d", cfg.Pruner.IntervalSeconds)
	}
}

func TestApplyEnvOverrides_PrunerConfig_Invalid(t *testing.T) {
	cfg := &Config{}
	t.Setenv("PRUNER_ENABLED", "invalid")
	t.Setenv("PRUNER_MAX_BLOCKS", "not_num")
	t.Setenv("PRUNER_PRUNE_THRESHOLD", "bad")
	t.Setenv("PRUNER_INTERVAL_SECONDS", "nope")
	applyEnvOverrides(cfg)

	if cfg.Pruner.Enabled {
		t.Error("Pruner.Enabled should remain false")
	}
}

func TestApplyEnvOverrides_SnapshotConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("SNAPSHOT_ENABLED", "true")
	t.Setenv("SNAPSHOT_DIR", "/custom/snapshots")
	t.Setenv("SNAPSHOT_BLOCKS_PER_FILE", "5000")
	t.Setenv("SNAPSHOT_INTERVAL_SECONDS", "120")
	applyEnvOverrides(cfg)

	if !cfg.Snapshot.Enabled {
		t.Error("Snapshot.Enabled should be true")
	}
	if cfg.Snapshot.Dir != "/custom/snapshots" {
		t.Errorf("Snapshot.Dir = %q", cfg.Snapshot.Dir)
	}
	if cfg.Snapshot.BlocksPerFile != 5000 {
		t.Errorf("Snapshot.BlocksPerFile = %d", cfg.Snapshot.BlocksPerFile)
	}
	if cfg.Snapshot.IntervalSeconds != 120 {
		t.Errorf("Snapshot.IntervalSeconds = %d", cfg.Snapshot.IntervalSeconds)
	}
}

func TestApplyEnvOverrides_SnapshotConfig_Invalid(t *testing.T) {
	cfg := &Config{}
	t.Setenv("SNAPSHOT_ENABLED", "notbool")
	t.Setenv("SNAPSHOT_BLOCKS_PER_FILE", "invalid")
	t.Setenv("SNAPSHOT_INTERVAL_SECONDS", "bad")
	applyEnvOverrides(cfg)

	if cfg.Snapshot.Enabled {
		t.Error("Snapshot.Enabled should remain false")
	}
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

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config file: %v", err)
	}

	t.Setenv("INDEXER_START_HEIGHT", "2000")
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Indexer.StartHeight != 2000 {
		t.Errorf("Expected start_height 2000, got %d", cfg.Indexer.StartHeight)
	}
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

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config file: %v", err)
	}

	t.Setenv("INDEXER_START_HEIGHT", "not_a_number")
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Indexer.StartHeight != 1000 {
		t.Errorf("Expected start_height 1000 (original), got %d", cfg.Indexer.StartHeight)
	}
}
