// Package ecbtexts reads ecb.europa.eu: monetary policy decisions, the
// monetary policy statement of the press conference and its Q&A, the
// accounts of the meetings, speeches and interviews of Executive Board
// members (ТЗ §6.1), and the Governing Council meeting calendar (§8.1).
package ecbtexts

import (
	"net/http"
	"time"

	"macro-collector/internal/sources/httpx"
)

const (
	userAgent = "macro-collector/1.0 (+personal research; no redistribution)"
	// Be polite to ecb.europa.eu; the decision-day mode polls every 20 s.
	defaultInterval = time.Second
)

// Client fetches ecb.europa.eu pages: the shared HTTP client with the
// settings of this site.
type Client = httpx.Client

// ErrNotFound is returned for HTTP 404.
var ErrNotFound = httpx.ErrNotFound

// New returns a client for ecb.europa.eu; opts override the site defaults
// (1 request per second, 3 retries from 2 s, 30 s timeout).
func New(opts ...httpx.Option) *Client {
	defaults := []httpx.Option{
		httpx.WithHTTPClient(&http.Client{Timeout: 30 * time.Second}),
		httpx.WithUserAgent(userAgent),
		httpx.WithInterval(defaultInterval),
		httpx.WithRetries(3, 2*time.Second),
	}
	return httpx.New(append(defaults, opts...)...)
}
