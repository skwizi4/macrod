package fred

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "abcdefghijklmnopqrstuvwxyz012345"

var fixtureDir = filepath.Join("..", "..", "..", "testdata", "fred")

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixtureServer serves recorded responses: series/<id> → series_<id>.json,
// series/observations → observations_<id>.json, series/release →
// series_release_<id>.json.
func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("api_key") != testKey || q.Get("file_type") != "json" {
			t.Errorf("request %s: missing api_key or file_type=json", r.URL.Path)
		}
		var name string
		switch r.URL.Path {
		case "/fred/series":
			name = "series_" + q.Get("series_id")
			if q.Get("series_id") == "NOT_A_SERIES_ID" {
				w.WriteHeader(http.StatusBadRequest)
				name = "error_bad_series"
			}
		case "/fred/series/observations":
			name = "observations_" + q.Get("series_id")
		case "/fred/series/release":
			name = "series_release_" + q.Get("series_id")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(readFixture(t, name))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestClient(t *testing.T, url string, opts ...Option) *Client {
	t.Helper()
	base := []Option{
		WithBaseURL(url + "/fred"),
		WithInterval(0),
		WithRetries(3, time.Millisecond),
		WithSleep(func(context.Context, time.Duration) error { return nil }),
	}
	c, err := New(testKey, append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNewRequiresKey(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Error("New with empty key succeeded")
	}
}

func TestFetchMeta(t *testing.T) {
	c := newTestClient(t, fixtureServer(t).URL)
	m, err := c.FetchMeta(context.Background(), "PAYEMS")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "PAYEMS" || m.Frequency != "m" || m.Units != "Thousands of Persons" {
		t.Errorf("meta = %+v", m)
	}
	if !m.ObservationStart.Equal(day("1939-01-01")) || m.LastUpdated.IsZero() {
		t.Errorf("dates = %v, %v", m.ObservationStart, m.LastUpdated)
	}
	// "-05" offset must be kept.
	if _, off := m.LastUpdated.Zone(); off != -5*3600 {
		t.Errorf("last_updated offset = %d", off)
	}

	m, err = c.FetchMeta(context.Background(), "DGS10")
	if err != nil {
		t.Fatal(err)
	}
	if m.Frequency != "d" || m.Units != "Percent" {
		t.Errorf("DGS10 meta = %+v", m)
	}
}

func TestFetchSeriesSkipsMissing(t *testing.T) {
	c := newTestClient(t, fixtureServer(t).URL)
	obs, err := c.FetchSeries(context.Background(), "DGS10", day("2025-12-15"))
	if err != nil {
		t.Fatal(err)
	}
	// The fixture has 20 rows, two of them "." (Christmas, New Year).
	if len(obs) != 18 {
		t.Fatalf("got %d observations, want 18", len(obs))
	}
	for _, o := range obs {
		if o.Date.Equal(day("2025-12-25")) || o.Date.Equal(day("2026-01-01")) {
			t.Errorf("holiday %s not skipped", o.Date.Format(time.DateOnly))
		}
	}
	if !obs[0].Date.Equal(day("2025-12-15")) || obs[0].Value != 4.18 {
		t.Errorf("first = %+v", obs[0])
	}
	for i := 1; i < len(obs); i++ {
		if !obs[i].Date.After(obs[i-1].Date) {
			t.Fatalf("not ascending at %d", i)
		}
	}
}

// pagingServer serves the PAYEMS fixture honouring offset and limit.
func pagingServer(t *testing.T, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	var full observationsResponse
	if err := json.Unmarshal(readFixture(t, "observations_PAYEMS"), &full); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page := full
		page.Offset = offset
		page.Observations = full.Observations[offset:min(offset+limit, len(full.Observations))]
		json.NewEncoder(w).Encode(page)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchSeriesPaginates(t *testing.T) {
	var requests atomic.Int32
	c := newTestClient(t, pagingServer(t, &requests).URL, WithPageLimit(7))
	obs, err := c.FetchSeries(context.Background(), "PAYEMS", day("2024-01-01"))
	if err != nil {
		t.Fatal(err)
	}
	// 2024-01 … 2026-09 = 33 months → 5 pages of 7.
	if len(obs) != 33 || requests.Load() != 5 {
		t.Errorf("got %d observations in %d requests, want 33 in 5", len(obs), requests.Load())
	}
	if !obs[32].Date.Equal(day("2026-09-01")) {
		t.Errorf("last = %v", obs[32].Date)
	}
}

func TestRetriesTransientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Write(readFixture(t, "series_DGS10"))
		}
	}))
	defer srv.Close()

	var waits []time.Duration
	c := newTestClient(t, srv.URL, WithRetries(3, 100*time.Millisecond),
		WithSleep(func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }))
	if _, err := c.FetchMeta(context.Background(), "DGS10"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || len(waits) != 2 {
		t.Fatalf("calls = %d, waits = %v", calls.Load(), waits)
	}
	// Exponential with jitter below base: [100ms, 200ms) then [200ms, 300ms).
	if waits[0] < 100*time.Millisecond || waits[0] >= 200*time.Millisecond ||
		waits[1] < 200*time.Millisecond || waits[1] >= 300*time.Millisecond {
		t.Errorf("waits = %v", waits)
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL)
	_, err := c.FetchMeta(context.Background(), "DGS10")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 4 { // 1 + 3 retries
		t.Errorf("calls = %d, want 4", calls.Load())
	}
}

func TestBadRequestNotRetriedAndKeyNotLeaked(t *testing.T) {
	var calls atomic.Int32
	srv := fixtureServer(t)
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		srv.Config.Handler.ServeHTTP(w, r)
	}))
	defer counting.Close()

	c := newTestClient(t, counting.URL)
	_, err := c.FetchMeta(context.Background(), "NOT_A_SERIES_ID")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "Invalid value for variable series_id") {
		t.Errorf("error message not taken from body: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, 400 must not be retried", calls.Load())
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("api key leaked: %v", err)
	}
}

func TestTransportErrorDoesNotLeakKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused from now on
	c := newTestClient(t, url, WithRetries(0, 0))
	_, err := c.FetchSeries(context.Background(), "DGS10", day("2026-01-01"))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "api_key") {
		t.Errorf("api key leaked: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "fred fetch DGS10:") {
		t.Errorf("error lacks context: %v", err)
	}
}

func TestCancelDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	c := newTestClient(t, srv.URL, WithRetries(5, time.Hour), WithSleep(sleepCtx))
	done := make(chan error)
	go func() { _, err := c.FetchMeta(ctx, "DGS10"); done <- err }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FetchMeta did not return after cancel")
	}
}

func TestLimiterSpacesRequests(t *testing.T) {
	l := newLimiter(20 * time.Millisecond)
	start := time.Now()
	for range 4 {
		if err := l.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// First is immediate, then three gaps of 20ms.
	if el := time.Since(start); el < 60*time.Millisecond {
		t.Errorf("4 requests took %v, want ≥ 60ms", el)
	}
}
