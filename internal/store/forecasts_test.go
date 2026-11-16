package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestReplaceForecasts(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 16, 0, 0, 0, time.UTC)
	replace := func(items ...ForecastItem) int {
		t.Helper()
		var n int
		if err := s.Write(ctx, func(tx *sql.Tx) error {
			var err error
			n, err = ReplaceForecasts(tx, "usa", "2026-W46", "reports/usa/2026-W46/master.md", items, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ndx := ForecastItem{Instrument: "NASDAQ", Direction: "up", Invalidation: 24500}
	ger := ForecastItem{Instrument: "GER40", Direction: "range", Invalidation: 23000}
	if n := replace(ndx, ger); n != 2 {
		t.Fatalf("first import changed %d, want 2", n)
	}
	got, err := s.Forecasts(ctx, "usa")
	if err != nil || len(got) != 2 || got[1].Instrument != "NASDAQ" || got[1].Outcome != OutcomePending {
		t.Fatalf("forecasts = %+v, %v", got, err)
	}
	sc := ForecastScore{PriceSeries: "yahoo/^NDX", RefDate: d("2026-11-13"), RefPrice: 25000,
		ResultDate: d("2026-11-20"), ResultPrice: 25500, ChangePct: 2, Outcome: OutcomeHit}
	if err := s.Write(ctx, func(tx *sql.Tx) error { return SetForecastScore(tx, got[1].ID, sc, now) }); err != nil {
		t.Fatal(err)
	}

	// Same items: nothing changes, the score stays.
	if n := replace(ndx, ger); n != 0 {
		t.Fatalf("same import changed %d, want 0", n)
	}
	got, _ = s.Forecasts(ctx, "usa")
	f := got[1]
	if f.Outcome != OutcomeHit || f.PriceSeries != "yahoo/^NDX" || !f.RefDate.Equal(d("2026-11-13")) || f.ResultPrice != 25500 {
		t.Fatalf("score lost: %+v", f)
	}

	// Changed level resets to pending; GER40 dropped from the report is deleted.
	ndx.Invalidation = 24400
	if n := replace(ndx); n != 2 {
		t.Fatalf("changed import changed %d, want 2", n)
	}
	got, _ = s.Forecasts(ctx, "usa")
	if len(got) != 1 || got[0].Outcome != OutcomePending || got[0].Invalidation != 24400 || got[0].PriceSeries != "" {
		t.Fatalf("after change: %+v", got)
	}
	if other, _ := s.Forecasts(ctx, "eu"); len(other) != 0 {
		t.Fatalf("eu = %+v", other)
	}
}
