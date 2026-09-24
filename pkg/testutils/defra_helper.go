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

// SetupTestDefraDB creates and starts a disk-backed DefraDB node with schema applied.
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

// SetupTestDefraDBWithSchema creates and starts a disk-backed DefraDB node
// with a provided schema. It uses a temporary directory and a random free
// port to avoid conflicts.
// Call the returned cleanup function (or use t.Cleanup) when done.
func SetupTestDefraDBWithSchema(tb testing.TB, schemaSDL string) *TestDefraDB {
	tb.Helper()
	return setupDefraDB(tb, schemaSDL, false)
}

// SetupTestDefraDBWithSchemaInMemory creates and starts an in-memory DefraDB
// node with a provided schema. Data lives purely in memory, so startup is
// free of disk I/O — useful for isolating pipeline cost from storage cost,
// at the price of a slower store path for large writes.
// Call the returned cleanup function (or use t.Cleanup) when done.
func SetupTestDefraDBWithSchemaInMemory(tb testing.TB, schemaSDL string) *TestDefraDB {
	tb.Helper()
	return setupDefraDB(tb, schemaSDL, true)
}

// setupDefraDB is the shared node builder behind the exported helpers; the
// only difference between the backends is how the store is rooted.
func setupDefraDB(tb testing.TB, schemaSDL string, inMemory bool) *TestDefraDB {
	tb.Helper()

	// Initialize logger if not already done
	logger.InitConsoleOnly(true)

	ctx := context.Background()

	port := getFreePort(tb)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	opts := options.Node().
		SetDisableAPI(false).
		SetDisableP2P(true)
	dir := ""
	if inMemory {
		opts.Store().SetType(options.NodeMemoryStore)
	} else {
		dir = tb.TempDir()
		opts.Store().SetPath(dir)
	}
	opts.HTTP().SetAddress(addr)

	defraNode, err := node.New(ctx, opts)
	if err != nil {
		tb.Fatalf("Failed to create DefraDB node: %v", err)
	}

	err = defraNode.Start(ctx)
	if err != nil {
		tb.Fatalf("Failed to start DefraDB node: %v", err)
	}

	// Apply schema
	_, err = defraNode.DB.AddCollection(ctx, schemaSDL)
	if err != nil && !strings.Contains(err.Error(), errors.ErrStrCollectionAlreadyExists) {
		_ = defraNode.Close(ctx)
		tb.Fatalf("Failed to apply schema: %v", err)
	}

	td := &TestDefraDB{
		Node: defraNode,
		Dir:  dir,
		Port: port,
	}

	tb.Cleanup(func() {
		_ = defraNode.Close(context.Background())
	})

	return td
}

// getFreePort returns a free TCP port on localhost.
func getFreePort(tb testing.TB) int {
	tb.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("Failed to get free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}
