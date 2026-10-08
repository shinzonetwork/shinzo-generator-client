package testutils

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/node"
)

// TestDefraDB holds a running embedded DefraDB node for testing.
type TestDefraDB struct {
	Node *node.Node
	Dir  string
	Port int
}

// SetupTestDefraDB creates and starts an in-memory DefraDB node with schema applied.
// It uses a temporary directory and a random free port to avoid conflicts.
// Call the returned cleanup function (or use t.Cleanup) when done.
func SetupTestDefraDB(t *testing.T) *TestDefraDB {
	collections, err := chains.NewCollections(nil)
	if err != nil {
		t.Fatalf("failed to create collections: %v", err)
	}
	sdl, err := schema.LoadSchemaSDL(collections)
	if err != nil {
		t.Fatalf("GetSchema: %v", err)
	}
	return SetupTestDefraDBWithSchema(t, sdl)
}

// SetupTestDefraDBWithSchema creates and starts an in-memory DefraDB node with a provided schema.
// It uses a temporary directory and a random free port to avoid conflicts.
// Call the returned cleanup function (or use t.Cleanup) when done.
func SetupTestDefraDBWithSchema(t *testing.T, schemaSDL string) *TestDefraDB {
	t.Helper()
	return SetupTestDefraDBWithSchemaOpts(t, schemaSDL, t.TempDir())
}

// NodeOptionFn mutates the node option builder after the shared test defaults
// (API on, P2P off, store path, HTTP address) are applied, so callers can add
// production node options without losing them.
type NodeOptionFn func(nb *options.NodeOptionsBuilder)

// SetupTestDefraDBWithSchemaOpts starts an embedded DefraDB node like
// SetupTestDefraDBWithSchema, but with a caller-provided store directory and
// extra node options. A caller-owned storePath lets tests place a file keyring
// next to the store the way the production bootstrap does, and the extra node
// options let tests mirror production settings such as the node identity or
// badger value-log file size.
func SetupTestDefraDBWithSchemaOpts(t *testing.T, schemaSDL, storePath string, extra ...NodeOptionFn) *TestDefraDB {
	t.Helper()

	// Initialize logger if not already done
	logger.InitConsoleOnly(true)

	ctx := context.Background()

	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	opts := options.Node().
		SetDisableAPI(false).
		SetDisableP2P(true)
	opts.Store().SetPath(storePath)
	opts.HTTP().SetAddress(addr)
	// Extras run after the base settings so they can override them.
	for _, apply := range extra {
		apply(opts)
	}

	defraNode, err := node.New(ctx, opts)
	if err != nil {
		t.Fatalf("Failed to create DefraDB node: %v", err)
	}

	err = defraNode.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start DefraDB node: %v", err)
	}

	// Apply schema
	_, err = defraNode.DB.AddCollection(ctx, schemaSDL)
	if err != nil && !strings.Contains(err.Error(), errors.ErrStrCollectionAlreadyExists) {
		_ = defraNode.Close(ctx)
		t.Fatalf("Failed to apply schema: %v", err)
	}

	td := &TestDefraDB{
		Node: defraNode,
		Dir:  storePath,
		Port: port,
	}

	t.Cleanup(func() {
		_ = defraNode.Close(context.Background())
	})

	return td
}

// getFreePort returns a free TCP port on localhost.
func getFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to get free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}
