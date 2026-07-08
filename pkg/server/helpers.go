package server

import (
	"encoding/json"
	"net/http"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
)

// errorResponse is the JSON envelope for API error responses on /api/v1/* endpoints.
// It is consumed programmatically by host clients. health.go endpoints use plain text
// errors instead because they serve browser/operational dashboards.
type errorResponse struct {
	Code    string `json:"error"`
	Message string `json:"message"`
}

// writeJSONError writes a structured JSON error response for host-client-facing endpoints.
// It sets the Content-Type to application/json and encodes an errorResponse with the given
// code, message, and HTTP status.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", constants.ContentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Code: code, Message: message})
}
