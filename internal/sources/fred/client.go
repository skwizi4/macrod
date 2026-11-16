// Package fred is a client for the FRED API (fred.stlouisfed.org/docs/api).
package fred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources"
)

const (
	// DefaultBaseURL is the FRED API root.
	DefaultBaseURL = "https://api.stlouisfed.org/fred"
	// EnvAPIKey holds the API key.
	EnvAPIKey = "FRED_API_KEY"

	// FRED allows 120 requests per minute.
	defaultInterval = 500 * time.Millisecond
	// Maximum page size of fred/series/observations.
	maxLimit = 100000

	lastUpdatedLayout = "2006-01-02 15:04:05-07"
)

var _ sources.NumericSource = (*Client)(nil)

// Meta is the subset of fred/series metadata we keep.
type Meta struct {
	ID               string
	Title            string
	Frequency        string // lower-case frequency_short: d, w, bw, m, q, sa, a
	Units            string
	LastUpdated      time.Time
	ObservationStart time.Time
	ObservationEnd   time.Time
}

// APIError is a non-2xx response from FRED.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("fred api status %d: %s", e.Status, e.Message)
}

func (e *APIError) retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Client talks to FRED with rate limiting and retries.
type Client struct {
	baseURL    string
	apiKey     string
	http       *http.Client
	limiter    *limiter
	maxRetries int
	backoff    time.Duration
	pageLimit  int
	sleep      func(context.Context, time.Duration) error
	log        *slog.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at another server (tests).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithInterval sets the minimum spacing between requests.
func WithInterval(d time.Duration) Option { return func(c *Client) { c.limiter = newLimiter(d) } }

// WithRetries sets the retry count and the base backoff.
func WithRetries(n int, backoff time.Duration) Option {
	return func(c *Client) { c.maxRetries, c.backoff = n, backoff }
}

// WithPageLimit sets the observations page size (tests).
func WithPageLimit(n int) Option { return func(c *Client) { c.pageLimit = n } }

// WithSleep replaces the backoff sleep (tests).
func WithSleep(f func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = f }
}

// WithLogger sets the logger; it should already carry source=fred.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// New returns a client. apiKey must be set.
func New(apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("fred: %s is empty", EnvAPIKey)
	}
	c := &Client{
		baseURL:    DefaultBaseURL,
		apiKey:     apiKey,
		http:       &http.Client{Timeout: 30 * time.Second},
		limiter:    newLimiter(defaultInterval),
		maxRetries: 4,
		backoff:    time.Second,
		pageLimit:  maxLimit,
		sleep:      sleepCtx,
		log:        slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// Name implements sources.NumericSource.
func (c *Client) Name() string { return "fred" }

type seriesResponse struct {
	Seriess []struct {
		ID               string `json:"id"`
		Title            string `json:"title"`
		FrequencyShort   string `json:"frequency_short"`
		Units            string `json:"units"`
		LastUpdated      string `json:"last_updated"`
		ObservationStart string `json:"observation_start"`
		ObservationEnd   string `json:"observation_end"`
	} `json:"seriess"`
}

// FetchMeta returns fred/series metadata for id.
func (c *Client) FetchMeta(ctx context.Context, id string) (Meta, error) {
	var resp seriesResponse
	if err := c.get(ctx, "series", url.Values{"series_id": {id}}, &resp); err != nil {
		return Meta{}, fmt.Errorf("fred meta %s: %w", id, err)
	}
	if len(resp.Seriess) != 1 {
		return Meta{}, fmt.Errorf("fred meta %s: got %d series", id, len(resp.Seriess))
	}
	s := resp.Seriess[0]
	m := Meta{ID: s.ID, Title: s.Title, Frequency: strings.ToLower(s.FrequencyShort), Units: s.Units}
	var err error
	if m.LastUpdated, err = time.Parse(lastUpdatedLayout, s.LastUpdated); err != nil {
		return Meta{}, fmt.Errorf("fred meta %s last_updated: %w", id, err)
	}
	if m.ObservationStart, err = time.Parse(time.DateOnly, s.ObservationStart); err != nil {
		return Meta{}, fmt.Errorf("fred meta %s observation_start: %w", id, err)
	}
	if m.ObservationEnd, err = time.Parse(time.DateOnly, s.ObservationEnd); err != nil {
		return Meta{}, fmt.Errorf("fred meta %s observation_end: %w", id, err)
	}
	return m, nil
}

type observationsResponse struct {
	Count        int `json:"count"`
	Offset       int `json:"offset"`
	Observations []struct {
		Date  string `json:"date"`
		Value string `json:"value"`
	} `json:"observations"`
}

// FetchSeries returns level observations of id from since on, in date order.
// Missing values (".") are skipped.
func (c *Client) FetchSeries(ctx context.Context, id string, since time.Time) ([]model.Observation, error) {
	var out []model.Observation
	for offset := 0; ; {
		params := url.Values{
			"series_id":         {id},
			"observation_start": {since.Format(time.DateOnly)},
			"sort_order":        {"asc"},
			"limit":             {strconv.Itoa(c.pageLimit)},
			"offset":            {strconv.Itoa(offset)},
		}
		var resp observationsResponse
		if err := c.get(ctx, "series/observations", params, &resp); err != nil {
			return nil, fmt.Errorf("fred fetch %s: %w", id, err)
		}
		for _, o := range resp.Observations {
			if o.Value == "." {
				continue
			}
			date, err := time.Parse(time.DateOnly, o.Date)
			if err != nil {
				return nil, fmt.Errorf("fred fetch %s date %q: %w", id, o.Date, err)
			}
			v, err := strconv.ParseFloat(o.Value, 64)
			if err != nil {
				return nil, fmt.Errorf("fred fetch %s value %q on %s: %w", id, o.Value, o.Date, err)
			}
			out = append(out, model.Observation{Date: date, Value: v})
		}
		offset += len(resp.Observations)
		if len(resp.Observations) == 0 || offset >= resp.Count {
			return out, nil
		}
	}
}

// get calls endpoint with retries and decodes the JSON body into dst.
func (c *Client) get(ctx context.Context, endpoint string, params url.Values, dst any) error {
	var lastErr error
	for attempt := 0; ; attempt++ {
		body, err := c.do(ctx, endpoint, params)
		if err == nil {
			if err := json.Unmarshal(body, dst); err != nil {
				return fmt.Errorf("decode %s: %w", endpoint, err)
			}
			return nil
		}
		lastErr = err
		var apiErr *APIError
		if ctx.Err() != nil || (errors.As(err, &apiErr) && !apiErr.retryable()) || attempt >= c.maxRetries {
			return lastErr
		}
		wait := c.backoffFor(attempt)
		c.log.Warn("request failed, retrying", "endpoint", endpoint, "series", params.Get("series_id"),
			"attempt", attempt+1, "wait", wait, "err", err)
		if err := c.sleep(ctx, wait); err != nil {
			return lastErr
		}
	}
}

// do performs one request and returns the body of a 2xx response.
func (c *Client) do(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	status, body, err := c.raw(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, apiError(status, body)
	}
	return body, nil
}

// raw performs one rate-limited request without retries. Transport errors
// never include the request URL, which carries the API key.
func (c *Client) raw(ctx context.Context, endpoint string, params url.Values) (int, []byte, error) {
	if err := c.limiter.wait(ctx); err != nil {
		return 0, nil, err
	}
	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	q.Set("api_key", c.apiKey)
	q.Set("file_type", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/"+endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return 0, nil, fmt.Errorf("build request %s: %w", endpoint, stripURL(err))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("request %s: %w", endpoint, stripURL(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read %s: %w", endpoint, stripURL(err))
	}
	return resp.StatusCode, body, nil
}

func apiError(status int, body []byte) *APIError {
	var e struct {
		ErrorMessage string `json:"error_message"`
	}
	msg := http.StatusText(status)
	if json.Unmarshal(body, &e) == nil && e.ErrorMessage != "" {
		msg = e.ErrorMessage
	}
	return &APIError{Status: status, Message: msg}
}

// stripURL drops the URL (and so the API key) from *url.Error.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// backoffFor returns base·2^attempt plus up to base of jitter, capped at 1 min.
func (c *Client) backoffFor(attempt int) time.Duration {
	d := c.backoff << attempt
	if c.backoff > 0 {
		d += rand.N(c.backoff)
	}
	return min(d, time.Minute)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
