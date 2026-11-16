package nasdaq

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
)

func fixture(t *testing.T, ticker string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "nasdaq", "earnings_date_"+ticker+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Recorded 2026-10-25 (LOOKUP E5).
func TestParseFixtures(t *testing.T) {
	for _, tt := range []struct {
		ticker, date, time string
		estimated, ok      bool
	}{
		{"MSFT", "2026-10-28", TimeAfterClose, false, true},
		{"GOOGL", "2026-10-28", TimeAfterClose, false, true},
		{"AMZN", "2026-10-29", TimeAfterClose, false, true},
		{"AAPL", "2026-11-02", TimeAfterClose, false, true},
		{"NVDA", "2026-11-18", TimeUnknown, true, true},
		{"AVGO", "2026-12-10", TimeUnknown, true, true},
		{"META", "", "", false, false}, // reported 2026-10-21, next date not announced
		{"TSLA", "", "", false, false},
	} {
		d, err := Parse(fixture(t, tt.ticker), tt.ticker)
		if err != nil {
			t.Errorf("%s: %v", tt.ticker, err)
			continue
		}
		if d.OK != tt.ok || d.Time != tt.time || d.Estimated != tt.estimated || (tt.ok && d.Date.Format(time.DateOnly) != tt.date) {
			t.Errorf("%s = %+v, want %s %q estimated=%v ok=%v", tt.ticker, d, tt.date, tt.time, tt.estimated, tt.ok)
		}
	}
}

func TestParseUnknownText(t *testing.T) {
	_, err := Parse([]byte(`{"data":{"reportText":"Something new"}}`), "X")
	if err == nil || !strings.Contains(err.Error(), "unrecognised") {
		t.Errorf("err = %v", err)
	}
	_, err = Parse([]byte(`{"data":null,"status":{"rCode":400}}`), "X")
	if err == nil || !strings.Contains(err.Error(), "rCode 400") {
		t.Errorf("err = %v", err)
	}
	d, err := Parse([]byte(`{"data":{"reportText":"Foo is expected* to report earnings on  01/27/2027 before market open."}}`), "X")
	if err != nil || d.Time != TimeBeforeOpen {
		t.Errorf("before open = %+v, %v", d, err)
	}
}

func TestEvent(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	ec := &config.EarningsCalendar{AfterClose: config.ClockTime{Hour: 16}, PreMarket: config.ClockTime{Hour: 7}, Unknown: config.ClockTime{Hour: 16}}
	c := config.Company{Ticker: "MSFT", Importance: "high"}
	day, _ := time.Parse(time.DateOnly, "2026-10-28")
	e := Event(EarningsDate{Ticker: "MSFT", Date: day, Time: TimeAfterClose, OK: true}, c, ec, ny)
	if e.Source != Source || e.Name != "MSFT earnings" || e.ScheduledAt.Format(time.RFC3339) != "2026-10-28T20:00:00Z" ||
		e.Importance != "high" || !reflect.DeepEqual(e.Related, model.Related{Ticker: "MSFT", ReportTime: TimeAfterClose}) {
		t.Errorf("event = %+v", e)
	}
	// After the US switch to winter time (2026-11-01) 07:00 ET is 12:00 UTC.
	day, _ = time.Parse(time.DateOnly, "2026-11-03")
	if e := Event(EarningsDate{Ticker: "MSFT", Date: day, Time: TimeBeforeOpen, OK: true}, c, ec, ny); e.ScheduledAt.Format(time.RFC3339) != "2026-11-03T12:00:00Z" {
		t.Errorf("pre-market in winter = %s", e.ScheduledAt)
	}
}

func TestClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analyst/MSFT/earnings-date" {
			http.NotFound(w, r)
			return
		}
		w.Write(fixture(t, "MSFT"))
	}))
	defer srv.Close()
	c := New(srv.URL+"/api/analyst/{ticker}/earnings-date", httpx.New(httpx.WithInterval(0), httpx.WithRetries(0, 0)), nil)
	d, err := c.EarningsDate(context.Background(), "MSFT")
	if err != nil || !d.OK || d.Time != TimeAfterClose {
		t.Errorf("MSFT = %+v, %v", d, err)
	}
	if _, err := c.EarningsDate(context.Background(), "NOPE"); err == nil || !strings.Contains(err.Error(), "nasdaq earnings NOPE") {
		t.Errorf("err = %v", err)
	}
}
