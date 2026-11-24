package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	shinzoerrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// maxDocumentHeights is the most block heights one documents request lists, so a single request
// cannot make the generator read its whole store. A wider request is answered for its first
// maxDocumentHeights heights.
const maxDocumentHeights = 1000

// DocIDsByHeight returns the IDs of the documents stored at block heights from through to, by
// collection name.
type DocIDsByHeight func(ctx context.Context, from, to int64) (map[string][]string, error)

// LowestHeight returns the lowest block height in the store. With none stored it returns an error
// whose pkg/errors code is CodeDocumentNotFound.
type LowestHeight func(ctx context.Context) (int64, error)

// goneResponse answers a documents request that starts below the lowest height the generator
// stores: those heights were pruned or never indexed, so no request can list them.
type goneResponse struct {
	errorResponse

	LowestHeight int64 `json:"lowestHeight"`
}

// EnableDocumentsEndpoint registers the authenticated endpoint that lists the documents this
// generator stored at a range of block heights, so a host can fetch the ones it is missing.
func (hs *HealthServer) EnableDocumentsEndpoint(docIDs DocIDsByHeight, lowest LowestHeight, auth Authenticator) {
	hs.mux.HandleFunc("GET /api/v1/documents", authMiddleware(auth, documentsHandler(docIDs, lowest), slog.Default()))
}

// documentsHandler serves the IDs of the documents stored at the heights from through to, given as
// query parameters, as a JSON object of collection name to IDs. A request starting below the
// lowest stored height is answered 410 Gone with that height.
func documentsHandler(docIDs DocIDsByHeight, lowest LowestHeight) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		from, fromErr := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
		to, toErr := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
		if fromErr != nil || toErr != nil || from < 0 || to < from {
			writeJSONError(w, http.StatusBadRequest, "bad_request", "from and to must be heights with from <= to")
			return
		}
		if to-from >= maxDocumentHeights {
			to = from + maxDocumentHeights - 1
		}

		low, err := lowest(r.Context())
		if err != nil && shinzoerrors.GetErrorCode(err) != shinzoerrors.CodeDocumentNotFound {
			logger.Sugar.Errorf("Cannot read the lowest stored height: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "cannot list the documents")
			return
		}
		if err == nil && from < low {
			w.Header().Set("Content-Type", constants.ContentTypeJSON)
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(goneResponse{
				Code:         "gone",
				Message:      fmt.Sprintf("heights below %d are not stored", low),
				LowestHeight: low,
			})
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
