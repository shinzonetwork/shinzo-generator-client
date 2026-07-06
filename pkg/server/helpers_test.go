package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteJSONError_SetsContentType(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeJSONError(rec, http.StatusUnauthorized, "unauthorized", "test")
	assert.Equal(t, constants.ContentTypeJSON, rec.Header().Get("Content-Type"))
}

func TestWriteJSONError_SetsStatusCode(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeJSONError(rec, http.StatusForbidden, "forbidden", "test")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestWriteJSONError_ResponseBody(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeJSONError(rec, http.StatusUnauthorized, "unauthorized", "missing or empty credentials")
	var resp errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "unauthorized", resp.Code)
	assert.Equal(t, "missing or empty credentials", resp.Message)
}

func TestErrorResponse_JSONSerialization(t *testing.T) {
	t.Parallel()
	resp := errorResponse{Code: "unauthorized", Message: "missing or empty credentials"}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}

	var decoded map[string]string
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}

	if decoded["error"] != "unauthorized" {
		t.Errorf("json[error] = %q, want %q", decoded["error"], "unauthorized")
	}
	if decoded["message"] != "missing or empty credentials" {
		t.Errorf("json[message] = %q, want %q", decoded["message"], "missing or empty credentials")
	}
}
