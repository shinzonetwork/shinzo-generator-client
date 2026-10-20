package errors

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"
)

func TestIsTransientRPCError(t *testing.T) {
	t.Parallel()

	dialRefused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"connection refused (wrapped)", fmt.Errorf("failed to get block 5: %w", syscall.ECONNREFUSED), true},
		{"connection reset", syscall.ECONNRESET, true},
		{"dial error", dialRefused, true},
		{"url error around dial", &url.Error{Op: "Post", URL: "http://localhost:8545", Err: dialRefused}, true},
		{"dns failure", &net.DNSError{Err: "no such host", Name: "shinzohub-val-000-0"}, true},
		{"unexpected eof", fmt.Errorf("read body: %w", io.ErrUnexpectedEOF), true},
		{"typed rpc connection failure", NewRPCConnectionFailed("rpc", "Dial", "", errors.New("x")), true},
		{"geth http 503", errors.New("503 Service Unavailable: upstream down"), true},
		{"cosmos client 502", errors.New("cosmos block 7: GET /block_results: status 502: bad gateway"), true},
		{"rate limited", errors.New("429 Too Many Requests: slow down"), true},
		{"plain string mentioning refused", errors.New("connection refused"), false},
		{"not found", errors.New("block not found"), false},
		{"data error", NewInvalidBlockFormat("evm", "Convert", "", nil), false},
		{"http 400", errors.New("GET /x: status 400: bad request"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsTransientRPCError(tt.err); got != tt.want {
				t.Errorf("IsTransientRPCError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
