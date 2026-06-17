package server

import (
	"encoding/json"
	"net/http"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	authErrors "github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
)

// writeJSONError writes a structured JSON error response for host-client-facing endpoints.
// It sets the Content-Type to application/json and encodes an ErrorResponse with the given
// code, message, and HTTP status. This is the canonical error format for the /api/v1/*
// endpoints consumed programmatically by host clients — health.go endpoints use plain text
// errors instead because they serve browser/operational dashboards.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", constants.ContentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(authErrors.ErrorResponse{Code: code, Message: message})
}