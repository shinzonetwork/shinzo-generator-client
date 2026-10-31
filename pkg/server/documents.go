package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// maxDocumentHeights is the most block heights one documents request may cover, so a single request
// cannot make the generator read its whole store.
const maxDocumentHeights = 1000

// DocIDsByHeight returns the IDs of the documents stored at block heights from through to, by
// collection name.
type DocIDsByHeight func(ctx context.Context, from, to int64) (map[string][]string, error)

// EnableDocumentsEndpoint registers the authenticated endpoint that lists the documents this
// generator stored at a range of block heights, so a host can fetch the ones it is missing.
func (hs *HealthServer) EnableDocumentsEndpoint(docIDs DocIDsByHeight, auth Authenticator) {
	hs.mux.HandleFunc("GET /api/v1/documents", authMiddleware(auth, documentsHandler(docIDs), slog.Default()))
}

// documentsHandler serves the IDs of the documents stored at the heights from through to, given as
// query parameters, as a JSON object of collection name to IDs.
func documentsHandler(docIDs DocIDsByHeight) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		from, fromErr := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
		to, toErr := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
		if fromErr != nil || toErr != nil || from < 0 || to < from || to-from >= maxDocumentHeights {
			writeJSONError(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("from and to must be heights with from <= to, covering at most %d heights", maxDocumentHeights))
			return
		}
		ids, err := docIDs(r.Context(), from, to)
		if err != nil {
			logger.Sugar.Errorf("Cannot list the documents at heights %d-%d: %v", from, to, err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "cannot list the documents")
			return
		}
		w.Header().Set("Content-Type", constants.ContentTypeJSON)
		_ = json.NewEncoder(w).Encode(ids)
	}
}
