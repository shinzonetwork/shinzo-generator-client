package utils

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetLANIP(t *testing.T) {
	ip, err := GetLANIP()
	require.NoError(t, err)
	assert.NotEmpty(t, ip)

	parsed := net.ParseIP(ip)
	assert.NotNil(t, parsed, "should return a valid IP address, got: %s", ip)
}

func TestGetLANIP_DialError(t *testing.T) {
	ip, err := getLANIPWithDialer(func(_, _ string) (net.Conn, error) {
		return nil, errors.New("no network")
	})
	assert.Error(t, err)
	assert.Empty(t, ip)
	assert.Contains(t, err.Error(), "error retrieving ip address")
}
