// Package httpx is the HTTP client shared by the phase 3 sources (Treasury,
// Yahoo, CFTC): rate limiting, retries with backoff and jitter on network
// errors, 429 and 5xx, no retries on other 4xx (ТЗ §9).
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// DefaultUserAgent identifies the collector; some servers (TreasuryDirect,
// Yahoo) reject requests without a User-Agent.
const DefaultUserAgent = "Mozilla/5.0 (compatible; macro-collector/1.0; personal research)"

const maxBody = 64 << 20

// ErrNotFound is returned for HTTP 404.
var ErrNotFound = errors.New("not found")

// HTTPError is a non-2xx, non-404 response.
type HTTPError struct {
	Status int
	URL    string
	Body   string // first bytes of the body, for diagnostics
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("GET %s: status %d", e.URL, e.Status)
	}
	return fmt.Sprintf("GET %s: status %d: %s", e.URL, e.Status, e.Body)
}

func (e *HTTPError) retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Client fetches URLs with rate limiting and retries.
type Client struct {
	http       *http.Client
	userAgent  string
	interval   time.Duration
	maxRetries int
	backoff    time.Duration
	sleep      func(context.Context, time.Duration) error
	log        *slog.Logger

	mu   sync.Mutex
	next time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// WithInterval sets the minimum spacing between requests.
func WithInterval(d time.Duration) Option { return func(c *Client) { c.interval = d } }

// WithRetries sets the retry count and the base backoff.
func WithRetries(n int, backoff time.Duration) Option {
	return func(c *Client) { c.maxRetries, c.backoff = n, backoff }
}

// WithSleep replaces the backoff sleep (tests).
func WithSleep(f func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = f }
}

// WithLogger sets the logger; it should already carry the source field.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// New returns a client.
func New(opts ...Option) *Client {
	c := &Client{
		http:       &http.Client{Timeout: 60 * time.Second},
		userAgent:  DefaultUserAgent,
		interval:   500 * time.Millisecond,
		maxRetries: 4,
		backoff:    time.Second,
		sleep:      SleepCtx,
		log:        slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Get returns the body of url. Network errors, 429 and 5xx are retried with
// backoff; 404 returns ErrNotFound without retries.
func (c *Client) Get(ctx context.Context, url string) ([]byte, error) {
	body, _, err := c.GetModified(ctx, url)
	return body, err
}

// GetModified is Get with the Last-Modified time of the response (zero if
// the server does not send it).
func (c *Client) GetModified(ctx context.Context, url string) ([]byte, time.Time, error) {
	for attempt := 0; ; attempt++ {
		body, mod, err := c.get(ctx, url)
		if err == nil || errors.Is(err, ErrNotFound) {
			return body, mod, err
		}
		var he *HTTPError
		if ctx.Err() != nil || (errors.As(err, &he) && !he.retryable()) || attempt >= c.maxRetries {
			return nil, time.Time{}, err
		}
		wait := c.backoffFor(attempt)
		c.log.Warn("request failed, retrying", "url", url, "attempt", attempt+1, "wait", wait, "err", err)
		if serr := c.sleep(ctx, wait); serr != nil {
			return nil, time.Time{}, err
		}
	}
}

func (c *Client) get(ctx context.Context, url string) ([]byte, time.Time, error) {
	if err := c.wait(ctx); err != nil {
		return nil, time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("GET %s: %w", url, err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json, text/html;q=0.9, */*;q=0.8")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, time.Time{}, fmt.Errorf("GET %s: %w", url, ErrNotFound)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, time.Time{}, &HTTPError{Status: resp.StatusCode, URL: url, Body: snippet}
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("GET %s read: %w", url, err)
	}
	mod, _ := http.ParseTime(resp.Header.Get("Last-Modified"))
	return body, mod, nil
}

// wait spaces requests at least interval apart.
func (c *Client) wait(ctx context.Context) error {
	if c.interval <= 0 {
		return ctx.Err()
	}
	c.mu.Lock()
	now := time.Now()
	slot := c.next
	if now.After(slot) {
		slot = now
	}
	c.next = slot.Add(c.interval)
	c.mu.Unlock()
	if d := slot.Sub(now); d > 0 {
		return SleepCtx(ctx, d)
	}
	return ctx.Err()
}

// backoffFor returns base·2^attempt plus up to base of jitter, capped at 1 min.
func (c *Client) backoffFor(attempt int) time.Duration {
	d := c.backoff << attempt
	if c.backoff > 0 {
		d += rand.N(c.backoff)
	}
	return min(d, time.Minute)
}

// SleepCtx sleeps for d or until ctx is done.
func SleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
