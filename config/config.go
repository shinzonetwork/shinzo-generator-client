package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

const CollectionName = "shinzo"

// DefraDBP2PConfig represents P2P configuration for DefraDB
type DefraDBP2PConfig struct {
	BootstrapPeers      []string `yaml:"bootstrap_peers"`
	ListenAddr          string   `yaml:"listen_addr"`
	Enabled             bool     `yaml:"enabled"`
	MaxRetries          int      `yaml:"max_retries"`
	RetryBaseDelayMs    int      `yaml:"retry_base_delay_ms"`
	ReconnectIntervalMs int      `yaml:"reconnect_interval_ms"`
	EnableAutoReconnect bool     `yaml:"enable_auto_reconnect"`
}

// DefraDBStoreConfig represents store configuration for DefraDB
type DefraDBStoreConfig struct {
	Path string `yaml:"path"`
}

// DefraDBConfig represents DefraDB configuration
type DefraDBConfig struct {
	Url           string             `yaml:"url"`
	KeyringSecret string             `yaml:"keyring_secret"`
	Embedded      bool               `yaml:"embedded"`
	P2P           DefraDBP2PConfig   `yaml:"p2p"`
	Store         DefraDBStoreConfig `yaml:"store"`
}

// Host returns the DefraDB host URL for backward compatibility
func (d *DefraDBConfig) Host() string {
	return d.Url
}

// GethConfig represents Geth node configuration
type GethConfig struct {
	NodeURL string `yaml:"node_url"`
	WsURL   string `yaml:"ws_url"`
	APIKey  string `yaml:"api_key"`
}

// IndexerConfig represents indexer configuration
type IndexerConfig struct {
	StartHeight      int `yaml:"start_height"`
	DocPushRateLimit int `yaml:"doc_push_rate_limit"`
	DocsPerTxn       int `yaml:"docs_per_txn"`        // Documents per transaction commit (default: 25)
	ConcurrentBlocks int `yaml:"concurrent_blocks"`   // Number of blocks to process concurrently (default: 1)
	PrefetchBlocks   int `yaml:"prefetch_blocks"`     // Number of blocks to prefetch (default: 2)
	ReceiptWorkers   int `yaml:"receipt_workers"`     // Concurrent receipt fetchers (default: 20)
}

// LoggerConfig represents logger configuration
type LoggerConfig struct {
	Development bool `yaml:"development"`
}

// Config represents the main configuration structure
type Config struct {
	DefraDB DefraDBConfig `yaml:"defradb"`
	Geth    GethConfig    `yaml:"geth"`
	Indexer IndexerConfig `yaml:"indexer"`
	Logger  LoggerConfig  `yaml:"logger"`
	Migration MigrationConfig `yaml:"migration"`
}

type MigrationConfig struct {
    Enabled        bool   `yaml:"enabled"`
    StartBlock     int64  `yaml:"start_block"`
    EndBlock       int64  `yaml:"end_block"`
    BatchSize      int    `yaml:"batch_size"`
    Workers        int    `yaml:"workers"`
    OutputDir      string `yaml:"output_dir"`
    Provider       string `yaml:"provider"`
    Validate       bool   `yaml:"validate"`
    ValidateSample int    `yaml:"validate_sample"`
}

// LoadConfig loads configuration from a YAML file and environment variables
func LoadConfig(path string) (*Config, error) {
	// Load .env file if it exists
	_ = godotenv.Load()

	// Load YAML config
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Apply environment variable overrides
	applyEnvOverrides(&cfg)

	// Apply default values
	applyDefaults(&cfg)

	// Validate configuration
	if err := validateConfig(&cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// applyDefaults sets default values for optional configuration
func applyDefaults(cfg *Config) {
	if cfg.Indexer.DocsPerTxn <= 0 {
		cfg.Indexer.DocsPerTxn = 25 // Default batch size
	}
	if cfg.Indexer.ConcurrentBlocks <= 0 {
		cfg.Indexer.ConcurrentBlocks = 1 // Default to sequential
	}
	if cfg.Indexer.PrefetchBlocks <= 0 {
		cfg.Indexer.PrefetchBlocks = 2 // Prefetch 2 blocks ahead
	}
	if cfg.Indexer.ReceiptWorkers <= 0 {
		cfg.Indexer.ReceiptWorkers = 20 // Default receipt workers
	}
}

// validateConfig validates the configuration
func validateConfig(cfg *Config) error {
	if cfg.Indexer.StartHeight < 0 {
		return fmt.Errorf("start_height must be >= 0")
	}

	// When using an external DefraDB instance (embedded=false), a URL is required.
	// Embedded DefraDB can run on a random port when Url is empty.
	if !cfg.DefraDB.Embedded && strings.TrimSpace(cfg.DefraDB.Url) == "" {
		return fmt.Errorf("external DefraDB requires a non-empty url")
	}
	return nil
}

// applyEnvOverrides applies environment variable overrides to configuration
func applyEnvOverrides(cfg *Config) {
	// DefraDB configuration
	if defraUrl := os.Getenv("DEFRADB_URL"); defraUrl != "" {
		cfg.DefraDB.Url = defraUrl
	} else if host := os.Getenv("DEFRADB_HOST"); host != "" {
		if port := os.Getenv("DEFRADB_PORT"); port != "" {
			cfg.DefraDB.Url = fmt.Sprintf("http://%s:%s", host, port)
		} else {
			cfg.DefraDB.Url = fmt.Sprintf("http://%s:9181", host)
		}
	}

	if keyringSecret := os.Getenv("DEFRADB_KEYRING_SECRET"); keyringSecret != "" {
		cfg.DefraDB.KeyringSecret = keyringSecret
	}

	if p2pEnabled := os.Getenv("DEFRADB_P2P_ENABLED"); p2pEnabled != "" {
		if parsed, err := strconv.ParseBool(p2pEnabled); err == nil {
			cfg.DefraDB.P2P.Enabled = parsed
		}
	}

	if listenAddr := os.Getenv("DEFRADB_P2P_LISTEN_ADDR"); listenAddr != "" {
		cfg.DefraDB.P2P.ListenAddr = listenAddr
	}

	if storePath := os.Getenv("DEFRADB_STORE_PATH"); storePath != "" {
		cfg.DefraDB.Store.Path = storePath
	}

	// Geth configuration
	if gethRpcUrl := os.Getenv("GETH_RPC_URL"); gethRpcUrl != "" {
		cfg.Geth.NodeURL = gethRpcUrl
	}

	if gethWsUrl := os.Getenv("GETH_WS_URL"); gethWsUrl != "" {
		cfg.Geth.WsURL = gethWsUrl
	}

	if gethApiKey := os.Getenv("GETH_API_KEY"); gethApiKey != "" {
		cfg.Geth.APIKey = gethApiKey
	}

	// Indexer configuration
	if startHeight := os.Getenv("INDEXER_START_HEIGHT"); startHeight != "" {
		if h, err := strconv.Atoi(startHeight); err == nil {
			cfg.Indexer.StartHeight = h
		}
	}
	if docsPerTxn := os.Getenv("INDEXER_DOCS_PER_TXN"); docsPerTxn != "" {
		if n, err := strconv.Atoi(docsPerTxn); err == nil {
			cfg.Indexer.DocsPerTxn = n
		}
	}
	if concurrentBlocks := os.Getenv("INDEXER_CONCURRENT_BLOCKS"); concurrentBlocks != "" {
		if n, err := strconv.Atoi(concurrentBlocks); err == nil {
			cfg.Indexer.ConcurrentBlocks = n
		}
	}
	if prefetchBlocks := os.Getenv("INDEXER_PREFETCH_BLOCKS"); prefetchBlocks != "" {
		if n, err := strconv.Atoi(prefetchBlocks); err == nil {
			cfg.Indexer.PrefetchBlocks = n
		}
	}
	if receiptWorkers := os.Getenv("INDEXER_RECEIPT_WORKERS"); receiptWorkers != "" {
		if n, err := strconv.Atoi(receiptWorkers); err == nil {
			cfg.Indexer.ReceiptWorkers = n
		}
	}

	// Logger configuration
	if loggerDebug := os.Getenv("LOGGER_DEBUG"); loggerDebug != "" {
		if debug, err := strconv.ParseBool(loggerDebug); err == nil {
			cfg.Logger.Development = debug
		}
	}
}
