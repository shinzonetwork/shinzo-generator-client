package utils

import (
	"fmt"
	"net"
)

// GetLANIP is a helper function that gets a local address network IP.
func GetLANIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", fmt.Errorf("error retrieving ip address: %w", err)
	}
	defer func() { _ = conn.Close() }()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP.String(), nil
}
