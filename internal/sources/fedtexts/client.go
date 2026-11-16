// Package fedtexts reads federalreserve.gov: the FOMC meeting calendar, the
// Beige Book schedule and (later) statements, minutes, speeches via RSS.
package fedtexts

import (
	"net/http"
	"time"

	"macro-collector/internal/sources/httpx"
)

const (
	userAgent = "macro-collector/1.0 (+personal research; no redistribution)"
	// Be polite to federalreserve.gov; FOMC mode polls every 20 s anyway.
	defaultInterval = time.Second
)

// Client fetches federalreserve.gov pages: the shared HTTP client with the
// settings of this site.
type Client = httpx.Client

// ErrNotFound is returned for HTTP 404: a page that is not published yet
// (the FOMC statement before 14:00 ET).
var ErrNotFound = httpx.ErrNotFound

// HTTPError is a non-2xx, non-404 response.
type HTTPError = httpx.HTTPError

// New returns a client for federalreserve.gov; opts override the site
// defaults (1 request per second, 3 retries from 2 s, 30 s timeout).
func New(opts ...httpx.Option) *Client {
	defaults := []httpx.Option{
		httpx.WithHTTPClient(&http.Client{Timeout: 30 * time.Second}),
		httpx.WithUserAgent(userAgent),
		httpx.WithInterval(defaultInterval),
		httpx.WithRetries(3, 2*time.Second),
	}
	return httpx.New(append(defaults, opts...)...)
}
