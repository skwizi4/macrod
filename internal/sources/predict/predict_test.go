package predict

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/sources/httpx"
)

// historyCalls counts CLOB history requests of the last server.
var historyCalls int

// server answers with the recorded responses in testdata/predict.
func server(t *testing.T) *httptest.Server {
	t.Helper()
	historyCalls = 0
	files := map[string]string{
		"/k/markets?KXFEDDECISION":                                  "kalshi_markets_KXFEDDECISION.json",
		"/k/markets?KXRECSSNBER":                                    "kalshi_markets_KXRECSSNBER.json",
		"/k/series/KXRECSSNBER/markets/KXRECSSNBER-27/candlesticks": "kalshi_candles_KXRECSSNBER-27.json",
		"/k/events/KXFEDDECISION-26DEC":                             "kalshi_event_KXFEDDECISION-26DEC.json",
		"/k/events/KXRECSSNBER-27":                                  "kalshi_event_KXRECSSNBER-27.json",
		"/g/events?us-recession-by-end-of-2026":                     "poly_event_us-recession-by-end-of-2026.json",
		"/g/public-search?Fed Decision in":                          "poly_search_fed.json",
		"/c/prices-history":                                         "poly_history_recession.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		for _, p := range []string{"series_ticker", "slug", "q"} {
			if v := r.URL.Query().Get(p); v != "" {
				key += "?" + v
			}
		}
		if r.URL.Path == "/c/prices-history" {
			historyCalls++
		}
		name, ok := files[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "predict", name))
		if err != nil {
			t.Error(err)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func client() *httpx.Client { return httpx.New(httpx.WithInterval(0), httpx.WithRetries(0, 0)) }

func TestKalshiNearestFedMeeting(t *testing.T) {
	srv := server(t)
	k := NewKalshi(srv.URL+"/k", client())
	ms, err := k.Open(context.Background(), "KXFEDDECISION", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 5 {
		t.Fatalf("markets of the nearest meeting = %d, want 5 outcomes", len(ms))
	}
	sum := 0.0
	for _, m := range ms {
		if m.Event != "KXFEDDECISION-26DEC" {
			t.Errorf("%s: event %s", m.ID, m.Event)
		}
		// The question is the event's title, the same for all outcomes.
		if m.Question != "Fed decision in Dec 2026? (On Dec 9, 2026)" || m.Outcome == "" || m.Label != m.Question+" — "+m.Outcome {
			t.Errorf("%s: question %q, outcome %q, label %q", m.ID, m.Question, m.Outcome, m.Label)
		}
		sum += m.Price
	}
	if sum < 0.9 || sum > 1.1 {
		t.Errorf("outcome prices sum to %.2f", sum)
	}
}

func TestKalshiRecessionAndHistory(t *testing.T) {
	srv := server(t)
	k := NewKalshi(srv.URL+"/k", client())
	ms, err := k.Open(context.Background(), "KXRECSSNBER", "KXRECSSNBER-27", false)
	if err != nil || len(ms) != 1 || ms[0].ID != "KXRECSSNBER-27" || ms[0].Label != "Recession in 2027? (In 2027)" || ms[0].Outcome != "" {
		t.Fatalf("markets = %+v %v", ms, err)
	}
	to := time.Unix(1793620800, 0)
	ps, err := k.History(context.Background(), "KXRECSSNBER", "KXRECSSNBER-27", to.AddDate(0, 0, -14), to)
	if err != nil || len(ps) < 5 {
		t.Fatalf("history = %+v %v", ps, err)
	}
	for i := 1; i < len(ps); i++ {
		if !ps[i].Date.After(ps[i-1].Date) {
			t.Errorf("dates not ascending: %v", ps)
		}
	}
	if last := ps[len(ps)-1]; last.Date.Format(time.DateOnly) > "2026-11-01" || last.Price <= 0 || last.Price >= 1 {
		t.Errorf("last = %+v", last)
	}
}

// Without the event title the market's own title is the question.
func TestKalshiEventTitleMissing(t *testing.T) {
	srv := server(t)
	k := NewKalshi(srv.URL+"/k", client())
	ms, err := k.Open(context.Background(), "KXRECSSNBER", "KXRECSSNBER-26", false)
	if err != nil || len(ms) != 1 || ms[0].Question != "Will there be a recession in 2026?" || ms[0].Outcome != "Starts" {
		t.Fatalf("markets = %+v %v", ms, err)
	}
}

func TestPolymarketEventSearchAndHistory(t *testing.T) {
	srv := server(t)
	p := NewPolymarket(srv.URL+"/g", srv.URL+"/c", client())
	ms, err := p.Event(context.Background(), "us-recession-by-end-of-2026")
	if err != nil || len(ms) != 1 || ms[0].Token == "" || ms[0].Price != 0.055 {
		t.Fatalf("event = %+v %v", ms, err)
	}
	fed, err := p.Search(context.Background(), "Fed Decision in")
	if err != nil || len(fed) < 4 {
		t.Fatalf("search = %+v %v", fed, err)
	}
	for _, m := range fed {
		if m.Question != "Fed Decision in December?" || m.Outcome == "" || m.Event == "" {
			t.Errorf("not the nearest meeting: %s", m.Label)
		}
	}
	to := time.Unix(1793620800, 0)
	ps, err := p.History(context.Background(), ms[0].Token, to.AddDate(0, 0, -35), to)
	if err != nil || len(ps) == 0 {
		t.Fatalf("history = %+v %v", ps, err)
	}
	if historyCalls != 3 { // 35 days in windows of 14
		t.Errorf("history requests = %d, want 3", historyCalls)
	}
}

func TestSeriesIDRoundTrip(t *testing.T) {
	topic, market, ok := TopicOf(SeriesID("recession", "KXRECSSNBER-27"))
	if !ok || topic != "recession" || market != "KXRECSSNBER-27" {
		t.Errorf("%s %s %v", topic, market, ok)
	}
}
