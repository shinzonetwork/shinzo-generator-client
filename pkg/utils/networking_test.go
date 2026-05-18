package utils

import (
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
