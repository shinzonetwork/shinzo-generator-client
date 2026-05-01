package testutils

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	acpIdentity "github.com/sourcenetwork/defradb/acp/identity"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/node"
)

// TestDefraDB holds a running embedded DefraDB node for testing.
type TestDefraDB struct {
	Node     *node.Node
	Dir      string
	Port     int
	Identity acpIdentity.FullIdentity // was acpIdentity.Identity
}

// SetupTestDefraDB creates and starts an in-memory DefraDB node with schema applied.
// It uses a temporary directory and a random free port to avoid conflicts.
// Call the returned cleanup function (or use t.Cleanup) when done.
func SetupTestDefraDB(t *testing.T) *TestDefraDB {
	t.Helper()
	logger.InitConsoleOnly(true)
	ctx := context.Background()
	tmpDir := t.TempDir()
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// Generate a signing identity for the test node
	nodeIdentity, err := identity.Generate(crypto.KeyTypeSecp256k1)
	if err != nil {
		t.Fatalf("Failed to generate node identity: %v", err)
	}

	opts := options.Node().
		SetDisableAPI(false).
		SetDisableP2P(true)
	opts.Store().SetPath(tmpDir)
	opts.HTTP().SetAddress(addr)
	opts.DB().SetNodeIdentity(nodeIdentity) // ← set identity at node level

	defraNode, err := node.New(ctx, opts)
	if err != nil {
		t.Fatalf("Failed to create DefraDB node: %v", err)
	}

	if err := defraNode.Start(ctx); err != nil {
		t.Fatalf("Failed to start DefraDB node: %v", err)
	}

	// Apply schema
	_, err = defraNode.DB.AddCollection(ctx, schema.GetSchema())
	if err != nil && !strings.Contains(err.Error(), "collection already exists") {
		defraNode.Close(ctx)
		t.Fatalf("Failed to apply schema: %v", err)
	}

	td := &TestDefraDB{
		Node:     defraNode,
		Dir:      tmpDir,
		Port:     port,
		Identity: nodeIdentity, // added
	}

	t.Cleanup(func() {
		defraNode.Close(context.Background())
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
	listener.Close()
	return port
}
