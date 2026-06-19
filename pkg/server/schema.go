package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
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
func (hs *HealthServer) EnableSchemaEndpoint(sdl string, network string, auth Authenticator) {
	handler := newSchemaHandler(sdl, network)
	hs.mux.HandleFunc("/api/v1/schema", authMiddleware(auth, requireReadMethod(handler), slog.Default()))
	hs.mux.HandleFunc("/api/v1/schema/{collection}", authMiddleware(auth, requireReadMethod(collectionHandler(network)), slog.Default()))
	hs.mux.HandleFunc("/api/v1/schema/collections", authMiddleware(auth, requireReadMethod(collectionsListHandler(network)), slog.Default()))
}

// negotiateContentType parses the Accept header and returns the matching content type.
// It strips media-type parameters (e.g. "; charset=utf-8") before matching so that
// well-formed clients sending "application/json; charset=utf-8" are accepted.
// Multi-value Accept headers are rejected (ambiguous) as are */* and empty values.
func negotiateContentType(accept string) (contentType string, ok bool) {
	accept = strings.TrimSpace(accept)
	if accept == "" {
		return "", false
	}

	// Reject any header containing multiple values — forcing the client to be explicit.
	if strings.Contains(accept, ",") {
		return "", false
	}

	// Strip media-type parameters (e.g. "; charset=utf-8") to get the bare media range.
	mediaRange := strings.ToLower(strings.SplitN(accept, ";", constants.AcceptHeaderMaxParts)[0])
	mediaRange = strings.TrimSpace(mediaRange)

	switch mediaRange {
	case constants.ContentTypeJSON:
		return constants.ContentTypeJSON, true
	case constants.ContentTypePlain:
		return constants.ContentTypePlain, true
	default:
		return "", false
	}
}

// requireReadMethod wraps an http.HandlerFunc and rejects any request that is not GET or HEAD.
// On rejection it returns 405 with a JSON error body and sets the Allow header per RFC 7231 §6.5.5.
func requireReadMethod(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET and HEAD are supported")
			return
		}
		next(w, r)
	}
}

// newSchemaHandler returns an http.HandlerFunc that serves the GraphQL schema.
// It supports content negotiation: application/json → {"network": "...", "schema": "..."}, text/plain → raw SDL.
// Any other Accept header value (including omitting it or using */*) results in 406 Not Acceptable.
// HEAD requests receive headers (Content-Type, Cache-Control) but no body.
func newSchemaHandler(sdl string, network string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contentType, ok := negotiateContentType(r.Header.Get("Accept"))
		if !ok {
			writeJSONError(w, http.StatusNotAcceptable, "not_acceptable", "supported content types: application/json, text/plain")
			return
		}

		w.Header().Set("Cache-Control", constants.CacheControlSchema)

		switch contentType {
		case constants.ContentTypeJSON:
			w.Header().Set("Content-Type", constants.ContentTypeJSON)
			if r.Method == http.MethodHead {
				return
			}
			_ = json.NewEncoder(w).Encode(schemaResponse{Network: network, Schema: sdl})
		case constants.ContentTypePlain:
			w.Header().Set("Content-Type", constants.ContentTypePlain)
			if r.Method == http.MethodHead {
				return
			}
			_, _ = w.Write([]byte(sdl))
		}
	}
}

// collectionsListHandler returns an http.HandlerFunc that serves the list of collections
// as JSON. Only application/json is accepted; any other Accept header results in 406.
func collectionsListHandler(prefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contentType, ok := negotiateContentType(r.Header.Get("Accept"))
		if !ok || contentType != constants.ContentTypeJSON {
			writeJSONError(w, http.StatusNotAcceptable, "not_acceptable", "supported content types: application/json")
			return
		}

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

// collectionHandler returns an http.HandlerFunc that serves a single collection's SDL.
// It supports content negotiation: application/json → {network, schema}, text/plain → raw SDL.
// Returns 404 if the collection name does not match a known collection.
// Collection SDLs are precomputed at registration time — zero per-request allocation.
func collectionHandler(prefix string) http.HandlerFunc {
	sdlCache := schema.PrecomputeCollectionSDLs(prefix)
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("collection")
		sdl, ok := sdlCache[name]
		if !ok {
			writeJSONError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("collection '%s' not found", name))
			return
		}

		contentType, ok := negotiateContentType(r.Header.Get("Accept"))
		if !ok {
			writeJSONError(w, http.StatusNotAcceptable, "not_acceptable",
				"supported content types: application/json, text/plain")
			return
		}

		w.Header().Set("Cache-Control", constants.CacheControlSchema)

		switch contentType {
		case constants.ContentTypeJSON:
			w.Header().Set("Content-Type", constants.ContentTypeJSON)
			if r.Method == http.MethodHead {
				return
			}
			_ = json.NewEncoder(w).Encode(schemaResponse{Network: prefix, Schema: sdl})
		case constants.ContentTypePlain:
			w.Header().Set("Content-Type", constants.ContentTypePlain)
			if r.Method == http.MethodHead {
				return
			}
			_, _ = w.Write([]byte(sdl))
		}
	}
}
