package utils

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		endpoint string
		expected string
	}{
		{
			name:     "key embedded in path",
			endpoint: "https://example.com/ethereum/s3cr3t-key",
			expected: "https://example.com",
		},
		{
			name:     "key embedded in nested path",
			endpoint: "https://example.org/v3/s3cr3t",
			expected: "https://example.org",
		},
		{
			name:     "key embedded in path on subdomain",
			endpoint: "https://node.example.net/v2/KEY",
			expected: "https://node.example.net",
		},
		{
			name:     "userinfo credentials dropped",
			endpoint: "https://user:pass@example.org/v3/s3cr3t",
			expected: "https://example.org",
		},
		{
			name:     "key in query dropped",
			endpoint: "wss://example.com/ws?key=abc123",
			expected: "wss://example.com",
		},
		{
			name:     "host with port preserved",
			endpoint: "http://127.0.0.1:8545",
			expected: "http://127.0.0.1:8545",
		},
		{
			name:     "ws scheme with port preserved",
			endpoint: "ws://geth:8546",
			expected: "ws://geth:8546",
		},
		{
			name:     "custom port preserved alongside path key",
			endpoint: "https://example.com:8545/v3/s3cr3t",
			expected: "https://example.com:8545",
		},
		{
			name:     "empty endpoint hidden",
			endpoint: "",
			expected: "(endpoint hidden)",
		},
		{
			name:     "unparseable endpoint hidden",
			endpoint: "not a url",
			expected: "(endpoint hidden)",
		},
		{
			name:     "scheme-less endpoint hidden",
			endpoint: "example.com/ethereum/s3cr3t-key",
			expected: "(endpoint hidden)",
		},
		{
			name:     "control character injection hidden",
			endpoint: "http://host\n.evil",
			expected: "(endpoint hidden)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.expected, RedactEndpoint(tc.endpoint))
		})
	}
}

func TestRedactHTTPError(t *testing.T) {
	t.Parallel()

	t.Run("request error URL redacted with port kept", func(t *testing.T) {
		t.Parallel()

		err := &url.Error{
			Op:  "Post",
			URL: "https://example.com:8545/v2/s3cr3t-key",
			Err: errors.New("dial tcp 10.0.0.1:8545: connect: connection refused"),
		}

		redacted := RedactHTTPError(err)

		assert.Equal(t,
			`Post "https://example.com:8545": dial tcp 10.0.0.1:8545: connect: connection refused`,
			redacted.Error())
		assert.NotContains(t, redacted.Error(), "s3cr3t-key")
	})

	t.Run("parse error hides raw endpoint", func(t *testing.T) {
		t.Parallel()

		invalidEndpoint := fmt.Sprintf("http://host\n.evil/v2/%s", "s3cr3t-key")
		_, err := url.Parse(invalidEndpoint)
		if !assert.Error(t, err) {
			return
		}

		redacted := RedactHTTPError(err)

		assert.Contains(t, redacted.Error(), "(endpoint hidden)")
		assert.NotContains(t, redacted.Error(), "s3cr3t-key")
		assert.NotContains(t, redacted.Error(), "evil")
	})

	t.Run("wrapped error is found and redacted", func(t *testing.T) {
		t.Parallel()

		err := fmt.Errorf("outer: %w", &url.Error{
			Op:  "Post",
			URL: "https://example.com/v3/s3cr3t",
			Err: errors.New("boom"),
		})

		redacted := RedactHTTPError(err)

		assert.NotContains(t, redacted.Error(), "s3cr3t")
	})

	t.Run("cause stays reachable for errors.Is and timeout checks", func(t *testing.T) {
		t.Parallel()

		redacted := RedactHTTPError(&url.Error{
			Op:  "Post",
			URL: "https://example.com/v2/s3cr3t",
			Err: context.DeadlineExceeded,
		})

		assert.ErrorIs(t, redacted, context.DeadlineExceeded)
		assert.True(t, func() *url.Error {
			target := &url.Error{}
			_ = errors.As(redacted, &target)
			return target
		}().Timeout())
	})

	t.Run("non-URL errors pass through unchanged", func(t *testing.T) {
		t.Parallel()

		err := errors.New("boom")

		assert.Same(t, err, RedactHTTPError(err))
	})
}
