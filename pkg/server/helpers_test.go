package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	authErrors "github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
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
	var resp authErrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "unauthorized", resp.Code)
	assert.Equal(t, "missing or empty credentials", resp.Message)
}