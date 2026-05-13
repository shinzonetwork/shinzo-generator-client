package utils

import (
	"fmt"
	"net"
)

// GetLANIP is a helper function that gets a local address network IP.
func GetLANIP() (string, error) {
	return getLANIPWithDialer(net.Dial)
}

// getLANIPWithDialer is the testable implementation of GetLANIP; it accepts an
// explicit dial function so unit tests can inject a mock without a global var.
func getLANIPWithDialer(dial func(network, address string) (net.Conn, error)) (string, error) {
	conn, err := dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", fmt.Errorf("error retrieving ip address: %w", err)
	}
	defer func() { _ = conn.Close() }()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP.String(), nil
}
