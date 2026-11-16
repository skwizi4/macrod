package fedtexts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"macro-collector/internal/sources/httpx"
)

func newTestClient() *Client {
	return New(httpx.WithInterval(0), httpx.WithRetries(2, time.Millisecond),
		httpx.WithSleep(func(context.Context, time.Duration) error { return nil }))
}

func TestClientGet(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "macro-collector/") {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/flaky":
			if n == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte("ok"))
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
		case "/down":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	c := newTestClient()
	ctx := context.Background()

	body, err := c.Get(ctx, srv.URL+"/flaky")
	if err != nil || string(body) != "ok" || calls.Load() != 2 {
		t.Fatalf("flaky: %q %v after %d calls", body, err, calls.Load())
	}

	calls.Store(0)
	if _, err := c.Get(ctx, srv.URL+"/missing"); !errors.Is(err, ErrNotFound) || calls.Load() != 1 {
		t.Errorf("missing: %v after %d calls", err, calls.Load())
	}

	calls.Store(0)
	var he *HTTPError
	if _, err := c.Get(ctx, srv.URL+"/forbidden"); !errors.As(err, &he) || he.Status != 403 || calls.Load() != 1 {
		t.Errorf("forbidden: %v after %d calls", err, calls.Load())
	}

	calls.Store(0)
	if _, err := c.Get(ctx, srv.URL+"/down"); !errors.As(err, &he) || he.Status != 500 || calls.Load() != 3 {
		t.Errorf("down: %v after %d calls", err, calls.Load())
	}
}
