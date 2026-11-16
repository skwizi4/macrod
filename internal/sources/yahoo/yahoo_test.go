package yahoo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/sources/httpx"
)

func fixture(t *testing.T, symbol string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "yahoo", Fixture{Symbol: symbol}.File()))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// After the recording (Saturday 2026-10-24): every session is closed.
var saturday = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func closes(s Series) map[string]float64 {
	m := map[string]float64{}
	for _, b := range s.Bars {
		m[b.Date.Format(time.DateOnly)] = b.Close
	}
	return m
}

func TestParseIndexAndFutures(t *testing.T) {
	ndx, err := Parse(fixture(t, "^NDX"), saturday)
	if err != nil {
		t.Fatal(err)
	}
	if ndx.Symbol != "^NDX" || ndx.Type != "INDEX" || ndx.Currency != "USD" || len(ndx.Bars) != 352 {
		t.Fatalf("ndx = %s %s %s %d bars", ndx.Symbol, ndx.Type, ndx.Currency, len(ndx.Bars))
	}
	c := closes(ndx)
	// Dates are New York days, not the UTC day of the 09:30 timestamp.
	if v := c["2026-10-23"]; v != 28957.53 {
		t.Errorf("ndx 10-23 = %v", v)
	}
	// Futures bars are stamped at 00:00 New York.
	zq, err := Parse(fixture(t, "ZQX26.CBT"), saturday)
	if err != nil {
		t.Fatal(err)
	}
	if v := closes(zq)["2026-10-23"]; v != 96.095 {
		t.Errorf("ZQX26 10-23 = %v", v)
	}
	if vols := zq.Volumes(); len(vols) == 0 || vols[len(vols)-1].Value != 264868 {
		t.Errorf("volumes = %v", vols[len(vols)-1:])
	}
	// VIX: null closes are skipped, not stored as zero; the real one-day
	// spike of 2025-10-16 (20.64 → 25.31 → 20.78) is kept.
	vix, err := Parse(fixture(t, "^VIX"), saturday)
	if err != nil {
		t.Fatal(err)
	}
	if len(vix.Bars) != 299-8 || len(vix.Dropped) != 0 {
		t.Errorf("vix bars %d, dropped %+v", len(vix.Bars), vix.Dropped)
	}
	// BTC trades every day; dates are UTC.
	btc, _ := Parse(fixture(t, "BTC-USD"), saturday)
	if _, ok := closes(btc)["2026-10-24"]; ok {
		t.Error("BTC 10-24 session still open at 12:00 UTC but kept")
	}
	if _, ok := closes(btc)["2026-10-23"]; !ok {
		t.Error("BTC 10-23 missing")
	}
}

// The bar of a session in progress is left out; futures sessions run to
// 23:59 New York, so on Friday evening their Friday bar is not final yet.
func TestParseDropsOpenSession(t *testing.T) {
	friEvening := time.Date(2026, 10, 23, 20, 30, 0, 0, time.UTC) // 16:30 ET, 22:30 Bratislava
	ndx, _ := Parse(fixture(t, "^NDX"), friEvening)
	if _, ok := closes(ndx)["2026-10-23"]; !ok {
		t.Error("NDX closed at 16:00 ET, its Friday bar must be kept")
	}
	zq, _ := Parse(fixture(t, "ZQX26.CBT"), friEvening)
	if _, ok := closes(zq)["2026-10-23"]; ok {
		t.Error("ZQ session open until 23:59 ET, Friday bar must wait")
	}
	if _, ok := closes(zq)["2026-10-22"]; !ok {
		t.Error("ZQ Thursday missing")
	}
}

func chart(ts []int64, cl []any) []byte {
	b, _ := json.Marshal(map[string]any{"chart": map[string]any{
		"result": []any{map[string]any{
			"meta":       map[string]any{"symbol": "X", "currency": "USD", "instrumentType": "INDEX", "exchangeTimezoneName": "America/New_York"},
			"timestamp":  ts,
			"indicators": map[string]any{"quote": []any{map[string]any{"close": cl}}},
		}},
		"error": nil,
	}})
	return b
}

func TestCleanAnomalies(t *testing.T) {
	var ts []int64
	for i := range 7 {
		ts = append(ts, time.Date(2026, 10, 12+i, 13, 30, 0, 0, time.UTC).Unix())
	}
	// 10-14 zero, 10-16 a one-day spike that returns; 10-18 a real jump
	// that stays.
	s, err := Parse(chart(ts, []any{100.0, 101.0, 0.0, 100.5, 180.0, 101.0, 140.0}), saturday)
	if err != nil {
		t.Fatal(err)
	}
	got := closes(s)
	if len(s.Dropped) != 2 || s.Dropped[0].Reason != "zero close" || !strings.HasPrefix(s.Dropped[1].Reason, "spike") {
		t.Fatalf("dropped = %+v", s.Dropped)
	}
	if _, ok := got["2026-10-16"]; ok {
		t.Error("spike kept")
	}
	if got["2026-10-18"] != 140 || len(s.Bars) != 5 {
		t.Errorf("bars = %v", got)
	}
}

func TestFetchErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "interval=1d") || !strings.Contains(r.URL.RawQuery, "period1=") {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		switch r.URL.Path {
		case "/chart/BAD":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"chart":{"result":null,"error":{"code":"Not Found","description":"No data found, symbol may be delisted"}}}`))
		case "/chart/LIMIT":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"chart":{"result":null,"error":{"code":"Bad Request","description":"Invalid input"}}}`))
		case "/chart/^GSPC":
			w.Write(chart(nil, nil))
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/chart/{symbol}", httpx.New(httpx.WithInterval(0), httpx.WithRetries(0, 0)), nil)
	ctx := context.Background()
	if _, err := c.Fetch(ctx, "BAD", saturday.AddDate(0, -1, 0), saturday); err == nil || !strings.Contains(err.Error(), "yahoo BAD") {
		t.Errorf("404 err = %v", err)
	}
	if _, err := c.Fetch(ctx, "LIMIT", saturday.AddDate(0, -1, 0), saturday); err == nil || !strings.Contains(err.Error(), "Invalid input") {
		t.Errorf("400 err = %v", err)
	}
	s, err := c.Fetch(ctx, "^GSPC", saturday.AddDate(0, -1, 0), saturday)
	if err != nil || len(s.Bars) != 0 {
		t.Errorf("empty = %+v, %v", s, err)
	}
}

// The Euronext MTS yields have one valued bar a day at the hourly interval
// (17:00 Amsterdam) and nulls around it: the day's bar is that value.
func TestParseHourlyYield(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "yahoo", Fixture{Symbol: "MIT10.AS", Hourly: true}.File()))
	if err != nil {
		t.Fatal(err)
	}
	// Tuesday 2026-11-10 12:00 Amsterdam: the session of the day is open.
	s, err := Parse(body, time.Date(2026, 11, 10, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Bars) != 28 || len(s.Dropped) != 0 { // weekdays of 1 October – 9 November
		t.Fatalf("bars = %d, dropped %v", len(s.Bars), s.Dropped)
	}
	got := closes(s)
	if got["2026-11-09"] != 4.3 || got["2026-10-01"] == 0 {
		t.Errorf("2026-11-09 = %v, 2026-10-01 = %v", got["2026-11-09"], got["2026-10-01"])
	}
	for _, b := range s.Bars {
		if wd := b.Date.Weekday(); wd == time.Saturday || wd == time.Sunday {
			t.Errorf("bar on %s", b.Date.Format(time.DateOnly))
		}
	}
	if s.Currency != "EUR" || s.Type != "INDEX" {
		t.Errorf("meta = %s %s", s.Currency, s.Type)
	}
}

func TestHourlySymbolRequest(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Path+"?"+r.URL.RawQuery)
		w.Write(chart(nil, nil))
	}))
	defer srv.Close()
	c := New(srv.URL+"/chart/{symbol}", httpx.New(httpx.WithInterval(0), httpx.WithRetries(0, 0)), nil)
	c.SetHourly([]string{"MIT10.AS"})
	ctx := context.Background()
	history := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, sym := range []string{"MIT10.AS", "^GDAXI"} {
		if _, err := c.Fetch(ctx, sym, history, saturday); err != nil {
			t.Fatal(err)
		}
	}
	// Hourly bars exist for the last 730 days only: the request starts there.
	first := saturday.Add(-HourlyHistory).Unix()
	if q := queries[0]; !strings.Contains(q, "interval=1h") || !strings.Contains(q, "period1="+strconv.FormatInt(first, 10)) {
		t.Errorf("hourly query = %s", q)
	}
	if q := queries[1]; !strings.Contains(q, "interval=1d") || !strings.Contains(q, "period1="+strconv.FormatInt(history.Unix(), 10)) {
		t.Errorf("daily query = %s", q)
	}
}
