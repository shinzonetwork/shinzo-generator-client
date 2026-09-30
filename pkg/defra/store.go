package defra

import (
	"context"
	"fmt"
	"strings"

	"github.com/sourcenetwork/defradb/client"
	defrahttp "github.com/sourcenetwork/defradb/http"
)

// Store is the seam through which the generator talks to DefraDB. Both the
// embedded node (node.Node.DB) and defradb's official HTTP client implement
// client.TxnStore, so a single interface covers both modes: embedded and
// external (a standalone `defradb start` process reachable over HTTP).
type Store = client.TxnStore

// NewHTTPStore connects to an external DefraDB node over its HTTP API using
// defradb's official HTTP client. The returned store implements the same
// client.TxnStore surface as the embedded node's DB, so the rest of the
// generator is backend-agnostic. ctx is reserved for future identity/timeout
// wiring; requests carry their own contexts at call time.
func NewHTTPStore(_ context.Context, url string) (Store, error) {
	trimmed := strings.TrimSpace(url)
	if trimmed == "" {
		return nil, fmt.Errorf("external DefraDB url is empty")
	}
	httpClient, err := defrahttp.NewClient(trimmed)
	if err != nil {
		return nil, fmt.Errorf("failed to create DefraDB HTTP client for %s: %w", sanitizedURL(trimmed), err)
	}
	return httpClient, nil
}

// sanitizedURL strips any credentials from a URL before logging.
func sanitizedURL(raw string) string {
	if i := strings.Index(raw, "@"); i >= 0 {
		if j := strings.Index(raw, "://"); j >= 0 && j+3 <= i {
			return raw[:j+3] + "***" + raw[i:]
		}
	}
	return raw
}
