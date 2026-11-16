package ingest

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/sources/cboe"
	"macro-collector/internal/store"
)

type fakeCboe struct {
	days  map[string]float64 // date → total ratio; absent = no file
	fail  string             // date that errors
	asked []string
}

func (f *fakeCboe) Day(_ context.Context, d time.Time) (map[string]float64, bool, error) {
	s := d.Format(time.DateOnly)
	f.asked = append(f.asked, s)
	if s == f.fail {
		return nil, false, errors.New("status 500")
	}
	v, ok := f.days[s]
	if !ok {
		return nil, false, nil
	}
	return map[string]float64{cboe.SeriesTotal: v, cboe.SeriesEquity: v / 2}, true, nil
}

func TestCboeRunSkipsWeekendsAndResumes(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	client := &fakeCboe{days: map[string]float64{"2026-10-29": 0.85, "2026-10-30": 0.90}}
	// Monday 2 Nov, 15:00 ET: Monday's file is not out yet.
	now := time.Date(2026, 11, 2, 20, 0, 0, 0, time.UTC)
	ing := &Cboe{Client: client, Store: st, History: day("2026-10-29"), Agent: "usa_news",
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}

	res := ing.Run(ctx, Incremental)
	if res.Err != nil || res.Days != 2 || res.Last.Format(time.DateOnly) != "2026-10-30" {
		t.Fatalf("first run = %+v", res)
	}
	if got := client.asked; len(got) != 3 || got[2] != "2026-11-02" { // Thu, Fri, Mon — no weekend requests
		t.Errorf("asked = %v", got)
	}
	obs, err := st.Observations(ctx, cboe.Source, cboe.SeriesEquity, time.Time{})
	if err != nil || len(obs) != 2 || obs[1].Value != 0.45 {
		t.Errorf("equity = %+v %v", obs, err)
	}

	// Next run starts after the last stored day; an error is recorded.
	client.asked, client.fail = nil, "2026-11-02"
	if res := ing.Run(ctx, Incremental); res.Err == nil {
		t.Fatal("no error")
	}
	if got := client.asked; len(got) != 1 || got[0] != "2026-11-02" {
		t.Errorf("resumed asked = %v", got)
	}
	states, err := st.FetchStates(ctx)
	if err != nil || states[cboe.Source].LastError == "" {
		t.Errorf("fetch state = %+v %v", states[cboe.Source], err)
	}
}
