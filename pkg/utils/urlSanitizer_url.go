package utils

import (
	"net/url"
)

// RedactEndpoint reduces an endpoint URL to scheme://host[:port] so secrets
// embedded in the userinfo, path, or query never reach terminal or CI logs.
// u.Host carries the port when present, so custom ports stay visible for
// debugging. url.Parse rejects control characters, so a CR/LF-forged value
// falls into the unparseable branch, and any other unparseable input returns
// a fixed sentinel instead of the raw string.
func RedactEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "(endpoint hidden)"
	}
	return u.Scheme + "://" + u.Host
}
