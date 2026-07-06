package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
)

type schemaResponse struct {
	Network string `json:"network"`
	Schema  string `json:"schema"`
}

type collectionsResponse struct {
	Network     string                   `json:"network"`
	Collections []schema.CollectionEntry `json:"collections"`
}

// EnableSchemaEndpoint registers the authenticated schema endpoint on the mux.
// The SDL and network values are captured by the handler closure at registration time,
// making them immutable for the lifetime of the server.
//
// It returns an error if the per-collection SDL cache cannot be precomputed, so
// startup fails fast instead of serving a degraded set of collections.
func (hs *HealthServer) EnableSchemaEndpoint(sdl string, network string, auth Authenticator) error {
	collectionH, err := collectionHandler(network)
	if err != nil {
		return fmt.Errorf("precompute collection SDLs for network %s: %w", network, err)
	}
	handler := newSchemaHandler(sdl, network)
	hs.mux.HandleFunc("GET /api/v1/schema", authMiddleware(auth, handler, slog.Default()))
	hs.mux.HandleFunc("GET /api/v1/schema/{collection}", authMiddleware(auth, collectionH, slog.Default()))
	hs.mux.HandleFunc("GET /api/v1/schema/collections", authMiddleware(auth, collectionsListHandler(network), slog.Default()))
	return nil
}

// newSchemaHandler returns an http.HandlerFunc that serves the GraphQL schema as JSON.
// HEAD requests receive headers (Content-Type, Cache-Control) but no body.
func newSchemaHandler(sdl string, network string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", constants.ContentTypeJSON)
		w.Header().Set("Cache-Control", constants.CacheControlSchema)
		if r.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(w).Encode(schemaResponse{Network: network, Schema: sdl})
	}
}

// collectionsListHandler returns an http.HandlerFunc that serves the list of collections as JSON.
// HEAD requests receive headers but no body.
func collectionsListHandler(prefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", constants.ContentTypeJSON)
		w.Header().Set("Cache-Control", constants.CacheControlSchema)
		if r.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(w).Encode(collectionsResponse{
			Network:     prefix,
			Collections: schema.ListCollections(prefix),
		})
	}
}

// collectionHandler returns an http.HandlerFunc that serves a single collection's schema as JSON.
// Returns 404 if the collection name does not match a known collection.
// Collection SDLs are precomputed at registration time — zero per-request allocation.
// HEAD requests receive headers but no body.
//
// It returns an error if the precomputation of collection SDLs fails, so the
// caller can fail fast at registration rather than serving a degraded cache.
func collectionHandler(prefix string) (http.HandlerFunc, error) {
	sdlCache, err := schema.PrecomputeCollectionSDLs(prefix)
	if err != nil {
		return nil, fmt.Errorf("precompute collection SDLs: %w", err)
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("collection")
		sdl, ok := sdlCache[name]
		if !ok {
			writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("collection '%s' not found", name))
			return
		}

		w.Header().Set("Content-Type", constants.ContentTypeJSON)
		w.Header().Set("Cache-Control", constants.CacheControlSchema)
		if r.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(w).Encode(schemaResponse{Network: prefix, Schema: sdl})
	}
	return handler, nil
}
