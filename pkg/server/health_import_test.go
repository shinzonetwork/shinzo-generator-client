package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/snapshot"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
)

// TestSnapshotImportHandler_NoCollections checks that the import handler refuses
// before ImportKV when no chain collections are set. The node has no DB, so a
// handler that reached ImportKV would panic instead of answering.
func TestSnapshotImportHandler_NoCollections(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	filename := writeTestKVSnapshot(t, tempDir, 0, 100)

	hs := NewHealthServer(0, nil, "")
	hs.defraNode = &node.Node{}
	hs.SetSnapshotter(snapshot.New(&config.SnapshotConfig{Dir: tempDir}, nil, nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/snapshots/import?file="+filename, nil)
	hs.snapshotImportHandler(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
