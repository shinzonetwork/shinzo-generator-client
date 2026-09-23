//go:build integration

// Package polygon runs the full Polygon generator pipeline — fetch (real
// fetcher against a mock RPC serving a real mainnet block), convert (Polygon
// variant selected from chain.name), store (production BlockHandler), query
// back — on an embedded DefraDB.
package polygon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// Real Polygon mainnet block 93954400 with a Bor state-sync deposit (0x7f).
const fixturePath = "../../pkg/chains/evm/testdata/polygon_block_93954400.json"

const depositTxHash = "0x8b4590d41dc86b0690a58af0285457c525d9c551625c95fee5ef2fd95e23fa9f"

// polygonRPCServer serves the fixture block for eth_getBlockByNumber and an
// empty result for eth_getBlockReceipts (receipts aren't under test here).
func polygonRPCServer(t *testing.T) *httptest.Server {
	t.Helper()
	blockResp, err := os.ReadFile(fixturePath)
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "eth_getBlockByNumber":
			var resp map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(blockResp, &resp))
			resp["id"] = req.ID
			require.NoError(t, json.NewEncoder(w).Encode(resp))
		case "eth_getBlockReceipts":
			_, err := w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":[]}`))
			require.NoError(t, err)
		default:
			_, err := w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"error":{"code":-32601,"message":"method not found"}}`))
			require.NoError(t, err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// gqlRows extracts the rows of a query result, tolerating both slice shapes
// DefraDB returns.
func gqlRows(t *testing.T, data any, collection string) []map[string]any {
	t.Helper()
	switch rows := data.(map[string]any)[collection].(type) {
	case []map[string]any:
		return rows
	case []any:
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.(map[string]any))
		}
		return out
	default:
		t.Fatalf("no rows for %s", collection)
		return nil
	}
}

func TestPolygonGeneratorIntegration(t *testing.T) {
	srv := polygonRPCServer(t)
	ctx := context.Background()

	cfg := &config.Config{
		Chain: config.ChainConfig{Name: "Polygon", Network: "Mainnet"},
	}

	// Converter + schema, selected by chain.name through the factory registry.
	converter, err := chains.NewConverter(cfg)
	require.NoError(t, err)
	sdl, err := converter.GetSchema()
	require.NoError(t, err)
	td := testutils.SetupTestDefraDBWithSchema(t, sdl)

	// Fetch the real fixture block through the production fetcher and client
	// (the 0x7f deposit tx forces the raw decode fallback).
	client, err := evm.NewEthereumClient(srv.URL, "", "", "")
	require.NoError(t, err)
	defer client.Close()
	fetcher := evm.NewFetcher(client, 4)

	raw, err := fetcher.FetchBlock(ctx, 93954400)
	require.NoError(t, err)

	result, err := converter.Convert(ctx, raw)
	require.NoError(t, err)

	handler, err := defra.NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)
	_, err = handler.Store(ctx, result)
	require.NoError(t, err)

	// Block is stored and queryable.
	blockRes := td.Node.DB.ExecRequest(ctx,
		`query { Polygon__Mainnet__Block(filter: {number: {_eq: 93954400}}) { number hash } }`)
	require.Empty(t, blockRes.GQL.Errors)
	require.Len(t, gqlRows(t, blockRes.GQL.Data, "Polygon__Mainnet__Block"), 1)

	// All 111 transactions are stored.
	txCount := td.Node.DB.ExecRequest(ctx,
		`query { Polygon__Mainnet__Transaction(filter: {blockNumber: {_eq: 93954400}}) { _docID } }`)
	require.Empty(t, txCount.GQL.Errors)
	assert.Len(t, gqlRows(t, txCount.GQL.Data, "Polygon__Mainnet__Transaction"), 111)

	// The deposit tx has from=zero address, yParity "0", type "127".
	depRes := td.Node.DB.ExecRequest(ctx,
		`query { Polygon__Mainnet__Transaction(filter: {hash: {_eq: "`+depositTxHash+`"}}) { from yParity type } }`)
	require.Empty(t, depRes.GQL.Errors)
	deps := gqlRows(t, depRes.GQL.Data, "Polygon__Mainnet__Transaction")
	require.Len(t, deps, 1)
	dep := deps[0]
	assert.Equal(t, evm.ZeroAddress, dep["from"])
	assert.Equal(t, "0", dep["yParity"])
	assert.Equal(t, "127", dep["type"])

	// Stripped fields are not in the schema — the DB itself must reject them.
	badRes := td.Node.DB.ExecRequest(ctx,
		`query { Polygon__Mainnet__Transaction { status } }`)
	assert.NotEmpty(t, badRes.GQL.Errors, "querying stripped field status must fail")
}
