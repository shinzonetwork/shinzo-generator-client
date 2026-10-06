package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// Tests in this file use t.Setenv, so they cannot run in parallel.

func TestSetupOTel(t *testing.T) {
	tests := []struct {
		name     string
		exporter string
		attrs    string
		wantErr  bool
		errIs    error
	}{
		{name: "disabled ignores malformed attributes", exporter: "", attrs: "service.instance.id"},
		{name: "valid config", exporter: "none", attrs: "service.instance.id=node-1"},
		{name: "malformed attributes", exporter: "none", attrs: "service.instance.id", wantErr: true, errIs: resource.ErrPartialResource},
		{name: "unknown exporter", exporter: "bogus", wantErr: true},
	}

	origLogger := logger.Sugar
	t.Cleanup(func() { logger.Sugar = origLogger })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OTEL_METRICS_EXPORTER", tt.exporter)
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", tt.attrs)
			core, logs := observer.New(zap.ErrorLevel)
			logger.Sugar = zap.New(core).Sugar()

			shutdown, err := setupOTel(t.Context(), config.ChainConfig{Name: "ethereum", Network: "mainnet"})
			if !tt.wantErr {
				require.NoError(t, err)
				otel.Handle(errors.New("export failed")) //nolint:err113
				assert.Equal(t, 1, logs.FilterMessage("OpenTelemetry: export failed").Len(), "OTel errors must go to the logger")
				require.NoError(t, shutdown(t.Context()))
				return
			}
			require.Error(t, err)
			if tt.errIs != nil {
				assert.ErrorIs(t, err, tt.errIs)
			}
		})
	}
}

func TestRun_TelemetryMisconfiguredStopsBeforeIndexer(t *testing.T) {
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.instance.id")

	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "defra")
	configPath := filepath.Join(tmpDir, "config.yaml")
	configContent := `
chain:
  name: "Ethereum"
  network: "Mainnet"
defradb:
  embedded: true
  store:
    path: "` + storePath + `"
geth:
  node_url: "http://127.0.0.1:1"
`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	err := run([]string{"-config", configPath})
	require.ErrorIs(t, err, resource.ErrPartialResource)
	assert.Contains(t, err.Error(), "telemetry setup")
	assert.NoDirExists(t, storePath, "the indexer must not be created")
}
