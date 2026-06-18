package testutils

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
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
	sdl, err := schema.GetSchema()
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

	// Initialize logger if not already done
	logger.InitConsoleOnly(true)

	ctx := context.Background()
	tmpDir := t.TempDir()

	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	opts := options.Node().
		SetDisableAPI(false).
		SetDisableP2P(true)
	opts.Store().SetPath(tmpDir)
	opts.HTTP().SetAddress(addr)

	defraNode, err := node.New(ctx, opts)
	if err != nil {
		t.Fatalf("Failed to create DefraDB node: %v", err)
	}

	err = defraNode.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start DefraDB node: %v", err)
	}

	// Apply schema
	_, err = defraNode.DB.AddSchema(ctx, schemaSDL)
	if err != nil && !strings.Contains(err.Error(), errors.ErrStrCollectionAlreadyExists) {
		_ = defraNode.Close(ctx)
		t.Fatalf("Failed to apply schema: %v", err)
	}

	td := &TestDefraDB{
		Node: defraNode,
		Dir:  tmpDir,
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
