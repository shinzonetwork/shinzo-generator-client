package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initHealthServer serves the documents stored at a range of heights in tokens mode only, behind the
// schema routes' authentication. Every row's store holds a document at height 5, so a full-mode 404
// is the route being absent.
func TestInitHealthServer_DocumentsEndpoint(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)
	cases := []struct {
		name        string
		mode        string
		wantNoKey   int
		wantWithKey int
	}{
		{"full", config.ChainModeFull, http.StatusNotFound, http.StatusNotFound},
		{"unset", "", http.StatusNotFound, http.StatusNotFound},
		{"tokens", config.ChainModeTokens, http.StatusUnauthorized, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Chain: config.ChainConfig{Name: "Ethereum", Network: "Mainnet", ChainID: 1, Mode: tc.mode},
				Indexer: config.IndexerConfig{
					HealthServerPort: freeHealthPort(t),
					SchemaAuthMode:   constants.SchemaAuthModeToken,
					SchemaAPIKeys:    []string{"documents-key"},
				},
			}
			converter, err := evm.NewConverter(cfg)
			require.NoError(t, err)
			sdl, err := converter.GetSchema()
			require.NoError(t, err)
			td := testutils.SetupTestDefraDBWithSchema(t, sdl)
			blocks := converter.Collections().BlockCollection()
			res := td.Node.DB.ExecRequest(context.Background(), fmt.Sprintf(`mutation { add_%s(input: {number: 5, hash: "0x05"}) { _docID } }`, blocks))
			require.Empty(t, res.GQL.Errors)
			created := res.GQL.Data.(map[string]any)["add_"+blocks].([]map[string]any)[0]["_docID"]

			indexer := &ChainIndexer{cfg: cfg, converter: converter, defraNode: td.Node}
			require.NoError(t, indexer.initHealthServer(cfg))
			t.Cleanup(func() { _ = indexer.healthServer.Stop(context.Background()) })

			url := fmt.Sprintf("http://localhost:%d/api/v1/documents?from=5&to=5", cfg.Indexer.HealthServerPort)
			get := func(key string) (int, map[string][]string) {
				req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
				if err != nil {
					return 0, nil
				}
				if key != "" {
					req.Header.Set("Authorization", "Bearer "+key)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return 0, nil
				}
				defer func() { _ = resp.Body.Close() }()
				var ids map[string][]string
				_ = json.NewDecoder(resp.Body).Decode(&ids)
				return resp.StatusCode, ids
			}
			assert.Eventually(t, func() bool { status, _ := get(""); return status == tc.wantNoKey },
				5*time.Second, 50*time.Millisecond, "GET /api/v1/documents without credentials")
			status, ids := get("documents-key")
			assert.Equal(t, tc.wantWithKey, status)
			if tc.wantWithKey == http.StatusOK {
				assert.Equal(t, map[string][]string{blocks: {created.(string)}}, ids)
			}
		})
	}
}
