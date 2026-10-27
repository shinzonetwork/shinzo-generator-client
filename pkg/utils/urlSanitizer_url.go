package utils

import (
	"errors"
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

// RedactHTTPError rewrites the *url.Error that net/http's client attaches to
// every failed request and that url.Parse returns for malformed input. Both
// echo the full URL through Error(), so an RPC endpoint with credentials
// embedded in its path or query leaks them through error logs, e.g.
// Post "https://host/v2/KEY": dial tcp: connection refused. The rebuilt
// error keeps the operation, the redacted endpoint, and the wrapped cause,
// so errors.Is/As and timeout classification keep working. Errors without a
// *url.Error in their chain are returned unchanged; it is meant to be fed
// the raw transport error, before any additional wrapping.
func RedactHTTPError(err error) error {
	if uErr, ok := errors.AsType[*url.Error](err); ok {
		return &url.Error{Op: uErr.Op, URL: RedactEndpoint(uErr.URL), Err: uErr.Err}
	}
	return err
}
