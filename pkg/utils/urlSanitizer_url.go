package utils

import (
	"net/url"
)

// RedactEndpoint reduces an endpoint URL to scheme://host so API keys that
// providers embed in the path (.../v2/<key>) or the query (?key=<key>) never
// reach terminal or CI logs. Everything after the host (userinfo, path, query)
// is dropped, which is where key material lives for providers like drpc,
// Infura, and Alchemy. url.Parse rejects control characters, so a CR/LF-forged
// value falls into the unparseable branch rather than being echoed, and any
// other unparseable input returns a fixed sentinel instead of the raw string.
func RedactEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "(endpoint hidden)"
	}
	return u.Scheme + "://" + u.Host
}
