package defra

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/sourcenetwork/defradb/node"
)

// GetPort returns the port number from a DefraDB node's API URL.
func GetPort(targetNode *node.Node) int {
	url := targetNode.APIURL
	return GetPortFromURL(url)
}

// GetPortFromURL extracts the port number from a URL string. The parse must
// accept any host form: the embedded node's API address is resolved through
// the deployment's rewrite of the configured URL (a loopback address becomes
// the machine's LAN IP so remote peers can reach it), so the API URL for a
// real running node is rarely loopback-shaped and a loopback-only parse
// reports no port for every such node.
func GetPortFromURL(rawURL string) int {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return -1
	}
	// A bare "host:port" value parses as scheme:host without a "//" prefix,
	// so it is normalized into a parseable authority first.
	if !strings.Contains(rawURL, "://") {
		rawURL = "//" + rawURL
	}
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil {
		return -1
	}
	portStr := parsed.Port()
	if portStr == "" {
		return -1
	}
	portInt, convErr := strconv.Atoi(portStr)
	if convErr != nil {
		return -1
	}
	return portInt
}
