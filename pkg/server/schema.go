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

// SchemaHandler returns an http.HandlerFunc that serves the GraphQL schema SDL.
// It supports content negotiation: application/json → {"network": "...", "sdl": "..."}, text/plain → raw SDL.
func SchemaHandler(sdl string, network string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
			return
		}

		accept := r.Header.Get("Accept")
		acceptLower := strings.ToLower(accept)

		if strings.Contains(acceptLower, "application/json") && !strings.Contains(acceptLower, "text/plain") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(schemaResponse{Network: network, SDL: sdl})
		} else {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(sdl))
		}
	}
}

// SetSchemaHandler registers the authenticated schema endpoint on the mux.
func (hs *HealthServer) SetSchemaHandler(auth Authenticator, sdl string, network string, logger *slog.Logger) {
	handler := authMiddleware(auth, SchemaHandler(sdl, network), logger)
	hs.mux.HandleFunc("/api/v1/schema", handler)
}
