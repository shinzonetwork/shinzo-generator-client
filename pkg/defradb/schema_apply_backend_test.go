package defradb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockBackend records all ApplySchema calls and delegates to fn if set,
// otherwise returns nil (success).
type mockBackend struct {
	mu    sync.Mutex
	calls []string
	fn    func(sdl string) error
}

func (m *mockBackend) ApplySchema(_ context.Context, sdl string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, sdl)
	if m.fn != nil {
		return m.fn(sdl)
	}
	return nil
}

func (m *mockBackend) getCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.calls))
	copy(cp, m.calls)
	return cp
}

func (m *mockBackend) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func TestApplyWithBackend_MonolithicSuccess(t *testing.T) {
	backend := &mockBackend{}
	ctx := context.Background()

	err := applyWithBackend(ctx, backend, "")
	require.NoError(t, err)

	assert.Equal(t, 1, backend.callCount(), "monolithic success should make exactly 1 call")
	assert.Contains(t, backend.getCalls()[0], constants.DefaultCollectionPrefix+"__Block")
}

func TestApplyWithBackend_MonolithicSuccess_CustomPrefix(t *testing.T) {
	backend := &mockBackend{}
	ctx := context.Background()

	err := applyWithBackend(ctx, backend, "Arbitrum__Mainnet")
	require.NoError(t, err)

	calls := backend.getCalls()
	require.Len(t, calls, 1)
	assert.Contains(t, calls[0], "Arbitrum__Mainnet__Block")
	assert.NotContains(t, calls[0], constants.DefaultCollectionPrefix)
}

func TestApplyWithBackend_MonolithicOtherError(t *testing.T) {
	backend := &mockBackend{
		fn: func(_ string) error {
			return fmt.Errorf("connection refused")
		},
	}
	ctx := context.Background()

	err := applyWithBackend(ctx, backend, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
	assert.Contains(t, err.Error(), "failed to apply schema")
}

func TestApplyWithBackend_FallbackToPerFile(t *testing.T) {
	isMonolithic := true
	backend := &mockBackend{
		fn: func(_ string) error {
			if isMonolithic {
				isMonolithic = false
				return fmt.Errorf("collection already exists in database")
			}
			return nil
		},
	}
	ctx := context.Background()

	err := applyWithBackend(ctx, backend, "")
	require.NoError(t, err)

	files, err := schema.ListCollectionFiles()
	require.NoError(t, err)

	callCount := backend.callCount()
	assert.Equal(t, 1+len(files), callCount,
		"should make 1 monolithic call then %d per-file calls", len(files))
}

func TestApplyWithBackend_FallbackPerFileSkipAlreadyExists(t *testing.T) {
	callIdx := 0
	backend := &mockBackend{
		fn: func(_ string) error {
			callIdx++
			// Monolithic call: already exists
			if callIdx == 1 {
				return fmt.Errorf("collection already exists in database")
			}
			// Per-file call #3 (accessListEntry): also already exists
			if callIdx == 3 {
				return fmt.Errorf("collection already exists")
			}
			return nil
		},
	}
	ctx := context.Background()

	err := applyWithBackend(ctx, backend, "")
	require.NoError(t, err, "should tolerate already-existing collections")
}

func TestApplyWithBackend_FallbackPerFileHardError(t *testing.T) {
	callIdx := 0
	backend := &mockBackend{
		fn: func(_ string) error {
			callIdx++
			if callIdx == 1 {
				return fmt.Errorf("collection already exists in database")
			}
			if callIdx == 3 {
				return fmt.Errorf("network error: connection reset")
			}
			return nil
		},
	}
	ctx := context.Background()

	err := applyWithBackend(ctx, backend, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network error")
	assert.Contains(t, err.Error(), "failed to apply collection schema")
}

func TestApplyPerFileWithBackend_AllSucceed(t *testing.T) {
	backend := &mockBackend{}
	ctx := context.Background()

	err := applyPerFileWithBackend(ctx, backend, constants.DefaultCollectionPrefix)
	require.NoError(t, err)

	files, err := schema.ListCollectionFiles()
	require.NoError(t, err)

	calls := backend.getCalls()
	assert.Equal(t, len(files), len(calls), "should make one call per collection file")
}

func TestApplyPerFileWithBackend_SkipAlreadyExists(t *testing.T) {
	callIdx := 0
	backend := &mockBackend{
		fn: func(_ string) error {
			callIdx++
			if callIdx == 3 {
				return fmt.Errorf("collection already exists in database")
			}
			return nil
		},
	}
	ctx := context.Background()

	err := applyPerFileWithBackend(ctx, backend, constants.DefaultCollectionPrefix)
	require.NoError(t, err, "should skip already-existing collections and succeed")

	files, _ := schema.ListCollectionFiles()
	assert.Equal(t, len(files), backend.callCount(),
		"should attempt all files even when some already exist")
}

func TestApplyPerFileWithBackend_HardErrorAborts(t *testing.T) {
	callIdx := 0
	backend := &mockBackend{
		fn: func(_ string) error {
			callIdx++
			if callIdx == 3 {
				return fmt.Errorf("network error: connection reset")
			}
			return nil
		},
	}
	ctx := context.Background()

	err := applyPerFileWithBackend(ctx, backend, constants.DefaultCollectionPrefix)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network error")
	assert.Contains(t, err.Error(), "failed to apply collection schema")
}

func TestApplyPerFileWithBackend_CustomPrefix(t *testing.T) {
	backend := &mockBackend{}
	ctx := context.Background()

	err := applyPerFileWithBackend(ctx, backend, "Arbitrum__Mainnet")
	require.NoError(t, err)

	calls := backend.getCalls()
	for _, call := range calls {
		assert.Contains(t, call, "Arbitrum__Mainnet__",
			"custom prefix should appear in SDL")
		assert.NotContains(t, call, constants.DefaultCollectionPrefix,
			"default prefix should not appear with custom prefix")
	}
}

func TestHTTPBackend_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"data": "{}"}`)
	}))
	defer server.Close()

	backend := &HTTPBackend{URL: server.URL}
	ctx := context.Background()

	err := backend.ApplySchema(ctx, "type Test { name: String }")
	assert.NoError(t, err)
}

func TestHTTPBackend_AlreadyExistsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "collection already exists")
	}))
	defer server.Close()

	backend := &HTTPBackend{URL: server.URL}
	ctx := context.Background()

	err := backend.ApplySchema(ctx, "type Test { name: String }")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collection already exists")

	var httpErr *httpError
	assert.ErrorAs(t, err, &httpErr, "error should be *httpError")
	assert.Equal(t, 500, httpErr.StatusCode)
}

func TestHTTPBackend_OtherErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, "service unavailable")
	}))
	defer server.Close()

	backend := &HTTPBackend{URL: server.URL}
	ctx := context.Background()

	err := backend.ApplySchema(ctx, "type Test { name: String }")
	require.Error(t, err)

	var httpErr *httpError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, 503, httpErr.StatusCode)
	assert.Contains(t, httpErr.Body, "service unavailable")
}

func TestHTTPBackend_ConnectionRefused(t *testing.T) {
	backend := &HTTPBackend{URL: "http://127.0.0.1:1"}
	ctx := context.Background()

	err := backend.ApplySchema(ctx, "type Test { name: String }")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to send schema")
}

func TestHTTPBackend_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	backend := &HTTPBackend{URL: "http://127.0.0.1:1"}
	err := backend.ApplySchema(ctx, "type Test { name: String }")
	require.Error(t, err)
}

func TestApplyWithBackend_EmptyPrefixResolvesToDefault(t *testing.T) {
	backend := &mockBackend{}
	ctx := context.Background()

	err := applyWithBackend(ctx, backend, "")
	require.NoError(t, err)

	calls := backend.getCalls()
	require.NotEmpty(t, calls)
	assert.Contains(t, calls[0], constants.DefaultCollectionPrefix+"__Block",
		"empty prefix should resolve to default prefix in monolithic SDL")
}

func TestHTTPBackend_RequestBodyContainsSDL(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
	}))
	defer server.Close()

	backend := &HTTPBackend{URL: server.URL}
	ctx := context.Background()

	testSDL := "type Test { name: String }"
	err := backend.ApplySchema(ctx, testSDL)
	require.NoError(t, err)
	assert.Contains(t, receivedBody, "type Test")
}

func TestHTTPBackend_SetsContentType(t *testing.T) {
	var contentType string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
	}))
	defer server.Close()

	backend := &HTTPBackend{URL: server.URL}
	ctx := context.Background()

	err := backend.ApplySchema(ctx, "type Test { name: String }")
	require.NoError(t, err)
	assert.Equal(t, "application/schema", contentType)
}

func TestApplyWithBackend_PreservesNonAlreadyExistsError(t *testing.T) {
	originalErr := fmt.Errorf("some database schema error: invalid type")
	backend := &mockBackend{
		fn: func(_ string) error {
			return originalErr
		},
	}
	ctx := context.Background()

	err := applyWithBackend(ctx, backend, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, originalErr, "original error should be wrapped")
	assert.Contains(t, err.Error(), "failed to apply schema")
}
