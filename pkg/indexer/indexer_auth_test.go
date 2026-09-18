package indexer

// indexer_auth_test.go covers the schema-authentication wiring of
// ChainIndexer: newAuthenticator mode selection and the initServices
// mTLS error path.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/server"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// newAuthenticator tests
// ---------------------------------------------------------------------------

func TestNewAuthenticator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mode     string
		keys     []string
		wantErr  error
		wantType any
	}{
		{"none", constants.SchemaAuthModeNone, nil, nil, server.NoOpAuthenticator{}},
		{"empty", "", nil, nil, &server.BearerAuthenticator{}},
		{"token", constants.SchemaAuthModeToken, []string{"key1", "key2"}, nil, &server.BearerAuthenticator{}},
		{"mtls_not_implemented", constants.SchemaAuthModeMTLS, nil, ErrMTLSNotImplemented, nil},
		{"unknown", "invalid", nil, ErrUnknownAuthMode, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			auth, err := newAuthenticator(tt.mode, tt.keys)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, auth)
			} else {
				require.NoError(t, err)
				assert.IsType(t, tt.wantType, auth)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// initServices mTLS error propagation
// ---------------------------------------------------------------------------

func TestInitServices_MTLSMode_ReturnsError(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			URL: fmt.Sprintf("http://localhost:%d", td.Port),
		},
		Indexer: config.IndexerConfig{
			SchemaAuthMode:   constants.SchemaAuthModeMTLS,
			HealthServerPort: 1,
		},
	}

	indexer := &ChainIndexer{
		defraNode: td.Node,
		cfg:       cfg,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := indexer.initServices(ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema auth configuration")
	assert.ErrorIs(t, err, ErrMTLSNotImplemented)
}
