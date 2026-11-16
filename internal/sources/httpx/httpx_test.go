package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func noSleep(context.Context, time.Duration) error { return nil }

func TestGetRetriesServerErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "Mozilla/5.0 (compatible; macro-collector") {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		if calls.Add(1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := New(WithInterval(0), WithSleep(noSleep))
	body, err := c.Get(context.Background(), srv.URL)
	if err != nil || string(body) != "ok" || calls.Load() != 3 {
		t.Fatalf("body %q, err %v, calls %d", body, err, calls.Load())
	}
}

func TestGetNoRetryOnClientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, `{"message":"bad filter"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	c := New(WithInterval(0), WithSleep(noSleep))

	_, err := c.Get(context.Background(), srv.URL+"/bad")
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 400 || !strings.Contains(he.Body, "bad filter") {
		t.Errorf("err = %v", err)
	}
	if _, err := c.Get(context.Background(), srv.URL+"/missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("404 err = %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (no retries)", calls.Load())
	}
}

func TestGetGivesUpAfterRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := New(WithInterval(0), WithRetries(2, time.Millisecond), WithSleep(noSleep))
	if _, err := c.Get(context.Background(), srv.URL); err == nil {
		t.Fatal("want error")
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}
