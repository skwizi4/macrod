package fred

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestFetchSeriesRelease(t *testing.T) {
	c := newTestClient(t, fixtureServer(t).URL)
	r, err := c.FetchSeriesRelease(context.Background(), "ICSA")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != 180 || r.Name != "Unemployment Insurance Weekly Claims Report" {
		t.Errorf("release = %+v", r)
	}
}

// releasesDatesServer serves the recorded fred/releases/dates response in
// pages of the requested limit/offset.
func releasesDatesServer(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	var full struct {
		Count        int               `json:"count"`
		ReleaseDates []json.RawMessage `json:"release_dates"`
	}
	if err := json.Unmarshal(readFixture(t, "releases_dates"), &full); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		q := r.URL.Query()
		if r.URL.Path != "/fred/releases/dates" || q.Get("realtime_start") != "2026-10-26" ||
			q.Get("realtime_end") != "2026-11-10" || q.Get("include_release_dates_with_no_data") != "true" {
			t.Errorf("unexpected request %s", r.URL)
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))
		end := min(offset+limit, len(full.ReleaseDates))
		json.NewEncoder(w).Encode(map[string]any{
			"count": full.Count, "offset": offset, "limit": limit,
			"release_dates": full.ReleaseDates[min(offset, end):end],
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchReleasesDatesPaginates(t *testing.T) {
	var calls int
	c := newTestClient(t, releasesDatesServer(t, &calls).URL, WithPageLimit(200))
	from, _ := time.Parse(time.DateOnly, "2026-10-26")
	to, _ := time.Parse(time.DateOnly, "2026-11-10")
	got, err := c.FetchReleasesDates(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 475 || calls != 3 {
		t.Fatalf("dates = %d in %d calls, want 475 in 3", len(got), calls)
	}
	var cpi []string
	for _, d := range got {
		if d.ReleaseID == 10 {
			cpi = append(cpi, d.Date.Format(time.DateOnly))
			if d.ReleaseName != "Consumer Price Index" {
				t.Errorf("CPI name = %q", d.ReleaseName)
			}
		}
	}
	if len(cpi) != 1 || cpi[0] != "2026-11-10" {
		t.Errorf("CPI dates = %v", cpi)
	}
}
