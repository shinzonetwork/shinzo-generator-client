package utils

import (
	"fmt"
	"net"
)

// dialFunc abstracts net.Dial for testability.
var dialFunc = net.Dial

func GetLANIP() (string, error) {
	conn, err := dialFunc("udp", "8.8.8.8:80")
	if err != nil {
		return "", fmt.Errorf("Error retrieving ip address: %w", err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP.String(), nil
}
