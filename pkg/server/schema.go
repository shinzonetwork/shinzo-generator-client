package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

type schemaResponse struct {
	Network string `json:"network"`
	SDL     string `json:"sdl"`
}

// EnableSchemaEndpoint stores the schema configuration and registers the authenticated schema endpoint on the mux.
func (hs *HealthServer) EnableSchemaEndpoint(sdl string, network string, auth Authenticator) {
	hs.schemaSDL = sdl
	hs.schemaNetwork = network
	hs.schemaAuth = auth
	hs.mux.HandleFunc("/api/v1/schema", authMiddleware(auth, hs.schemaHandler, slog.Default()))
}

// schemaHandler serves the GraphQL schema SDL.
// It supports content negotiation: application/json → {"network": "...", "sdl": "..."}, text/plain → raw SDL.
// Any other Accept header value (including omitting it or using */*) results in 406 Not Acceptable.
func (hs *HealthServer) schemaHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}

	accept := strings.ToLower(strings.TrimSpace(r.Header.Get("Accept")))

	switch accept {
	case "application/json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(schemaResponse{Network: hs.schemaNetwork, SDL: hs.schemaSDL})
	case "text/plain":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(hs.schemaSDL))
	default:
		writeJSONError(w, http.StatusNotAcceptable, "not_acceptable", "supported content types: application/json, text/plain")
	}
}
