package ecbdata

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
)

func fixture(t *testing.T, key string) []model.Observation {
	t.Helper()
	for _, f := range Fixtures {
		if f.Key == key {
			body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "ecb", f.File()))
			if err != nil {
				t.Fatal(err)
			}
			obs, err := Parse(body)
			if err != nil {
				t.Fatal(err)
			}
			return obs
		}
	}
	t.Fatalf("no fixture for %s", key)
	return nil
}

func TestParseFixtures(t *testing.T) {
	for _, tt := range []struct {
		key         string
		n           int
		first, last string
		lastValue   float64
	}{
		{"FM/D.U2.EUR.4F.KR.DFR.LEV", 40, "2026-10-01", "2026-11-09", 2.5}, // every calendar day
		{"EST/B.EU000A2X2A25.WT", 28, "2026-10-01", "2026-11-09", 2.442},   // business days
		{"ILM/W.U2.C.T000000.Z5.Z01", 9, "2026-09-11", "2026-11-06", 5913173},
		{"HICP/M.U2.N.000000.4D0.INX", 22, "2025-01-01", "2026-10-01", 104.77},
	} {
		obs := fixture(t, tt.key)
		if len(obs) != tt.n {
			t.Errorf("%s: %d observations, want %d", tt.key, len(obs), tt.n)
			continue
		}
		f, l := obs[0], obs[len(obs)-1]
		if f.Date.Format(time.DateOnly) != tt.first || l.Date.Format(time.DateOnly) != tt.last || l.Value != tt.lastValue {
			t.Errorf("%s: %s … %s = %v", tt.key, f.Date.Format(time.DateOnly), l.Date.Format(time.DateOnly), l.Value)
		}
	}
}

// The weekly statement of week 44 is drawn up as at Friday 30 October.
func TestWeeklyPeriodIsFriday(t *testing.T) {
	for _, o := range fixture(t, "ILM/W.U2.C.T000000.Z5.Z01") {
		if o.Date.Weekday() != time.Friday {
			t.Errorf("%s is a %s", o.Date.Format(time.DateOnly), o.Date.Weekday())
		}
		if o.Date.Format(time.DateOnly) == "2026-10-30" && o.Value != 5945410 {
			t.Errorf("W44 = %v", o.Value)
		}
	}
}

func TestParseErrors(t *testing.T) {
	head := "KEY,TIME_PERIOD,OBS_VALUE\n"
	for name, body := range map[string]string{
		"no columns":   "KEY,VALUE\nK,1\n",
		"bad value":    head + "K,2026-10,abc\n",
		"bad period":   head + "K,October,1.5\n",
		"out of order": head + "K,2026-10,1.5\nK,2026-09,1.4\n",
		"html":         "<html><body>We are experiencing some problems</body></html>",
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	obs, err := Parse([]byte(head + "K,2026-09,\nK,2026-10,NaN\nK,2026-11,2.5\n"))
	if err != nil || len(obs) != 1 || obs[0].Value != 2.5 {
		t.Errorf("missing values: %v, %v", obs, err)
	}
	if obs, err := Parse(nil); err != nil || obs != nil {
		t.Errorf("empty body: %v, %v", obs, err)
	}
}

func TestURL(t *testing.T) {
	c := New("https://example.org/service/data/", nil, nil)
	got := c.URL("FM/D.U2.EUR.4F.KR.DFR.LEV", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Time{})
	want := "https://example.org/service/data/FM/D.U2.EUR.4F.KR.DFR.LEV?detail=dataonly&format=csvdata&startPeriod=2026-10-01"
	if got != want {
		t.Errorf("URL = %s", got)
	}
	if u := c.URL("EST/B.X.WT", time.Time{}, time.Time{}); strings.Contains(u, "Period") {
		t.Errorf("URL without dates = %s", u)
	}
}

// SPF next year (batch 11f): one request with the target year open; each
// survey quarter keeps its forecast for the next calendar year — 2025-Q4
// for 2026 (1.83), 2026-Q4 for 2027 (2.63); LT and other years are dropped.
func TestParseNextYear(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "ecb", "SPF_Q_U2_HICP_POINT_next_year.csv"))
	if err != nil {
		t.Fatal(err)
	}
	obs, err := ParseNextYear(body, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 8 {
		t.Fatalf("observations = %d, want 8 (2025-Q1 … 2026-Q4)", len(obs))
	}
	got := map[string]float64{}
	for _, o := range obs {
		got[o.Date.Format(time.DateOnly)] = o.Value
	}
	if math.Abs(got["2025-10-01"]-1.8251046214035087) > 1e-9 || math.Abs(got["2026-10-01"]-2.633975002156863) > 1e-9 {
		t.Errorf("stitched %v", got)
	}
}

func TestFetchNextYearOpensTheDimension(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "ecb", "SPF_Q_U2_HICP_POINT_next_year.csv"))
	if err != nil {
		t.Fatal(err)
	}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := New(srv.URL, httpx.New(httpx.WithInterval(0)), nil)
	obs, err := c.FetchSeries(context.Background(), "SPF/Q.U2.HICP.POINT."+NextYear+".Q.AVG", time.Time{})
	if err != nil || len(obs) != 8 {
		t.Fatalf("obs %d, %v", len(obs), err)
	}
	if path != "/SPF/Q.U2.HICP.POINT..Q.AVG" {
		t.Errorf("requested %s", path)
	}
	if _, err := c.FetchSeries(context.Background(), "SPF/Q.U2.HICP.POINT.x"+NextYear+".Q.AVG", time.Time{}); err == nil {
		t.Error("a placeholder inside a dimension is accepted")
	}
}
