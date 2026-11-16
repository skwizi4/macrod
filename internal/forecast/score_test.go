package forecast

import (
	"math"
	"testing"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

// closes from Mon 2026-11-09: the report week W46 (9–13.11), the forecast
// week W47 (16–20.11).
func closes(vals ...float64) []model.Observation {
	start := day("2026-11-09")
	var out []model.Observation
	for i, v := range vals {
		d := start.AddDate(0, 0, i+2*(i/5)) // skip weekends
		out = append(out, model.Observation{Date: d, Value: v})
	}
	return out
}

func TestScore(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Bratislava")
	w := Window{From: time.Date(2026, 11, 15, 16, 0, 0, 0, loc), To: time.Date(2026, 11, 22, 16, 0, 0, 0, loc)}
	after := w.To.Add(time.Minute)
	week := func(fri float64, next ...float64) []model.Observation {
		return closes(append([]float64{99, 99, 99, 99, fri}, next...)...)
	}
	tests := []struct {
		name    string
		f       store.ForecastItem
		closes  []model.Observation
		want    string
		wantChg float64
	}{
		{"up hit", store.ForecastItem{Direction: Up, Invalidation: 95}, week(100, 101, 99, 102, 103, 104), store.OutcomeHit, 4},
		{"up miss", store.ForecastItem{Direction: Up, Invalidation: 95}, week(100, 101, 99, 98, 97, 99), store.OutcomeMiss, -1},
		{"up flat is miss", store.ForecastItem{Direction: Up, Invalidation: 95}, week(100, 101, 100, 100, 100, 100), store.OutcomeMiss, 0},
		{"up invalidated midweek", store.ForecastItem{Direction: Up, Invalidation: 95}, week(100, 94, 99, 102, 103, 106), store.OutcomeInvalidated, 6},
		{"up close at level is not a break", store.ForecastItem{Direction: Up, Invalidation: 95}, week(100, 95, 99, 102, 103, 106), store.OutcomeHit, 6},
		{"down hit", store.ForecastItem{Direction: Down, Invalidation: 105}, week(100, 101, 104, 99, 98, 97), store.OutcomeHit, -3},
		{"down invalidated", store.ForecastItem{Direction: Down, Invalidation: 105}, week(100, 106, 104, 99, 98, 97), store.OutcomeInvalidated, -3},
		{"range hit", store.ForecastItem{Direction: Range, Invalidation: 103}, week(100, 101, 102, 100, 100, 100.5), store.OutcomeHit, 0.5},
		{"range miss on move", store.ForecastItem{Direction: Range, Invalidation: 90}, week(100, 99, 98, 98, 99, 98.9), store.OutcomeMiss, -1.1},
		{"range level above breaks up", store.ForecastItem{Direction: Range, Invalidation: 103}, week(100, 104, 102, 100, 100, 100), store.OutcomeInvalidated, 0},
		{"range level below breaks down", store.ForecastItem{Direction: Range, Invalidation: 97}, week(100, 101, 96, 100, 100, 100), store.OutcomeInvalidated, 0},
		{"short week: result is the last close", store.ForecastItem{Direction: Up, Invalidation: 95}, week(100, 101, 102), store.OutcomeHit, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc, ok := Score(tt.f, w, Prices{Series: "yahoo/^NDX", Closes: tt.closes}, Prices{}, 1, after)
			if !ok || sc.Outcome != tt.want || math.Abs(sc.ChangePct-tt.wantChg) > 1e-9 {
				t.Fatalf("got %+v %v, want %s %v", sc, ok, tt.want, tt.wantChg)
			}
			if sc.RefPrice != 100 || !sc.RefDate.Equal(day("2026-11-13")) || sc.PriceSeries != "yahoo/^NDX" {
				t.Fatalf("reference %+v", sc)
			}
		})
	}
}

func TestScorePendingAndFallback(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Bratislava")
	w := Window{From: time.Date(2026, 11, 15, 16, 0, 0, 0, loc), To: time.Date(2026, 11, 22, 16, 0, 0, 0, loc)}
	f := store.ForecastItem{Direction: Up, Invalidation: 200} // far above: a break on the primary
	full := closes(99, 99, 99, 99, 100, 101, 102, 103, 104, 105)
	if _, ok := Score(f, w, Prices{Series: "p", Closes: full}, Prices{}, 1, w.To.Add(-time.Minute)); ok {
		t.Fatal("scored before the forecast week ended")
	}
	if _, ok := Score(f, w, Prices{Series: "p", Closes: full[:5]}, Prices{}, 1, w.To); ok {
		t.Fatal("scored without a close in the forecast week")
	}
	// The reference is stale (last close a week before the report week).
	stale := closes(99, 99, 99, 99, 100)
	for i := range stale {
		stale[i].Date = stale[i].Date.AddDate(0, 0, -7)
	}
	stale = append(stale, full[5:]...)
	if _, ok := Score(f, w, Prices{Series: "p", Closes: stale}, Prices{}, 1, w.To); ok {
		t.Fatal("scored on a stale reference")
	}
	// The primary lacks the result: the fallback is used, the level not checked.
	fb := Prices{Series: "fred/NASDAQCOM", Closes: closes(50, 50, 50, 50, 50, 49, 48, 50, 51, 52)}
	sc, ok := Score(f, w, Prices{Series: "p", Closes: full[:5]}, fb, 1, w.To)
	if !ok || sc.Outcome != store.OutcomeHit || sc.PriceSeries != "fred/NASDAQCOM" || sc.ResultPrice != 52 {
		t.Fatalf("fallback: %+v %v", sc, ok)
	}
}
