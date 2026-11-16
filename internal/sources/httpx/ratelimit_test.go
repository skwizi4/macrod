package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// arrivals is a test server that records when each request reaches it.
type arrivals struct {
	mu    sync.Mutex
	times []time.Time
}

func (a *arrivals) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.times = append(a.times, time.Now())
		a.mu.Unlock()
		w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// gaps are the spacings between consecutive arrivals.
func (a *arrivals) gaps() []time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	ts := slices.Clone(a.times)
	slices.SortFunc(ts, func(x, y time.Time) int { return x.Compare(y) })
	var out []time.Duration
	for i := 1; i < len(ts); i++ {
		out = append(out, ts[i].Sub(ts[i-1]))
	}
	return out
}

// Requests are spaced at least the interval apart (SEC: ≤ 10 per second),
// also when several goroutines share the client. Only the lower bound is
// checked: a slow machine may space them more, never less. The slack
// covers scheduling jitter between leaving the limiter and reaching the
// server.
func TestIntervalSpacesRequests(t *testing.T) {
	const (
		interval = 40 * time.Millisecond
		slack    = 15 * time.Millisecond
	)
	for _, tc := range []struct {
		name       string
		goroutines int
		perG       int
	}{
		{"sequential", 1, 5},
		{"concurrent", 6, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a arrivals
			srv := a.server(t)
			c := New(WithInterval(interval), WithSleep(noSleep))
			start := time.Now()
			var wg sync.WaitGroup
			for range tc.goroutines {
				wg.Go(func() {
					for range tc.perG {
						if _, err := c.Get(context.Background(), srv.URL); err != nil {
							t.Error(err)
						}
					}
				})
			}
			wg.Wait()
			n := tc.goroutines * tc.perG
			gaps := a.gaps()
			if len(gaps) != n-1 {
				t.Fatalf("%d arrivals, want %d", len(gaps)+1, n)
			}
			for i, g := range gaps {
				if g < interval-slack {
					t.Errorf("gap %d = %s, want ≥ %s", i, g, interval)
				}
			}
			if min := time.Duration(n-1) * interval; time.Since(start) < min {
				t.Errorf("%d requests took %s, want ≥ %s", n, time.Since(start), min)
			}
		})
	}
}

// The first request is not delayed, and a request waiting for its slot
// gives up when ctx is cancelled without reaching the server.
func TestIntervalFirstImmediateAndCancel(t *testing.T) {
	var a arrivals
	srv := a.server(t)
	c := New(WithInterval(time.Hour), WithSleep(noSleep))

	start := time.Now()
	if _, err := c.Get(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("first request took %s", d)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Get(ctx, srv.URL); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second request: %v, want deadline exceeded", err)
	}
	if len(a.gaps()) != 0 {
		t.Error("the cancelled request reached the server")
	}
}
