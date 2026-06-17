package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

type schemaResponse struct {
	Network string `json:"network"`
	Schema  string `json:"schema"`
}

// EnableSchemaEndpoint stores the schema configuration and registers the authenticated schema endpoint on the mux.
func (hs *HealthServer) EnableSchemaEndpoint(sdl string, network string, auth Authenticator) {
	hs.schemaSDL = sdl
	hs.schemaNetwork = network
	hs.schemaAuth = auth
	hs.mux.HandleFunc("/api/v1/schema", authMiddleware(auth, requireGetMethod(hs.schemaHandler), slog.Default()))
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

// requireGetMethod wraps an http.HandlerFunc and rejects any request that is not GET.
// On rejection it returns 405 with a JSON error body and sets the Allow header per RFC 7231 §6.5.5.
func requireGetMethod(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
			return
		}
		next(w, r)
	}
}

// schemaHandler serves the GraphQL schema.
// It supports content negotiation: application/json → {"network": "...", "schema": "..."}, text/plain → raw SDL.
// Any other Accept header value (including omitting it or using */*) results in 406 Not Acceptable.
func (hs *HealthServer) schemaHandler(w http.ResponseWriter, r *http.Request) {
	contentType, ok := negotiateContentType(r.Header.Get("Accept"))
	if !ok {
		writeJSONError(w, http.StatusNotAcceptable, "not_acceptable", "supported content types: application/json, text/plain")
		return
	}

	switch contentType {
	case constants.ContentTypeJSON:
		w.Header().Set("Content-Type", constants.ContentTypeJSONCharset)
		_ = json.NewEncoder(w).Encode(schemaResponse{Network: hs.schemaNetwork, Schema: hs.schemaSDL})
	case constants.ContentTypePlain:
		w.Header().Set("Content-Type", constants.ContentTypePlainCharset)
		_, _ = w.Write([]byte(hs.schemaSDL))
	}
}
