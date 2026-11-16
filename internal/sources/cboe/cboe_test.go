package cboe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/sources/httpx"
)

func TestDayFromRecordedFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "cboe", name+".json"))
		if err != nil {
			// Cboe answers a day without a file with 403, not 404.
			http.Error(w, "<Error>AccessDenied</Error>", http.StatusForbidden)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	c := New(srv.URL+"/{date}_daily_options", httpx.New(httpx.WithInterval(0), httpx.WithRetries(0, 0)), nil)

	day, _ := time.Parse(time.DateOnly, "2026-10-30")
	got, ok, err := c.Day(context.Background(), day)
	if err != nil || !ok {
		t.Fatalf("day: %v %v", ok, err)
	}
	if got[SeriesTotal] != 0.90 || got[SeriesEquity] != 0.66 || got[SeriesIndex] != 1.03 {
		t.Errorf("ratios = %v", got)
	}
	sat := day.AddDate(0, 0, 1)
	if _, ok, err := c.Day(context.Background(), sat); ok || err != nil {
		t.Errorf("saturday: %v %v, want no file and no error", ok, err)
	}
}

func TestParseRejectsFileWithoutRatios(t *testing.T) {
	if _, err := Parse([]byte(`{"ratios":[{"name":"OEX PUT/CALL RATIO","value":"4.00"}]}`)); err == nil {
		t.Error("no error for a file without the stored ratios")
	}
	if _, err := Parse([]byte(`{"ratios":[{"name":"TOTAL PUT/CALL RATIO","value":"n/a"}]}`)); err == nil {
		t.Error("no error for a bad value")
	}
}
