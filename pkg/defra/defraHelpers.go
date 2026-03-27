package defra

import (
	"strconv"
	"strings"

	"github.com/sourcenetwork/defradb/node"
)

func GetPort(targetNode *node.Node) int {
	url := targetNode.APIURL
	return GetPortFromUrl(url)
}

func GetPortFromUrl(url string) int {
	// Check if it's localhost format (http://127.0.0.1:port, http://localhost:port, or http://[::]:port)
	if !strings.Contains(url, "127.0.0.1:") && !strings.Contains(url, "localhost:") && !strings.Contains(url, "[::]:") {
		return -1
	}

	// Extract port by splitting on colon and taking the last part
	parts := strings.Split(url, ":")

	// Get the last part (port) and remove any trailing path
	portStr := parts[len(parts)-1]
	if strings.Contains(portStr, "/") {
		portStr = strings.Split(portStr, "/")[0]
	}

	// Convert port string to int
	portInt, err := strconv.Atoi(portStr)
	if err != nil {
		return -1
	}

	return portInt
}
