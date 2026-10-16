package utils

import (
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
