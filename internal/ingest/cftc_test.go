package ingest

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/cftc"
	"macro-collector/internal/store"
)

type fakeCFTC struct {
	data  map[string][]model.Observation
	err   error
	since time.Time
	codes []string
}

func (f *fakeCFTC) Fetch(_ context.Context, codes []string, since time.Time) (map[string][]model.Observation, error) {
	f.codes, f.since = codes, since
	return f.data, f.err
}

func weekly(from string, vals ...float64) []model.Observation {
	var out []model.Observation
	for i, v := range vals {
		out = append(out, model.Observation{Date: day(from).AddDate(0, 0, 7*i), Value: v})
	}
	return out
}

func TestCFTCRun(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := &fakeCFTC{data: map[string][]model.Observation{
		cftc.SeriesID("209742", cftc.FieldLevLong):      weekly("2026-10-06", 48000, 48106, 51749),
		cftc.SeriesID("209742", cftc.FieldOpenInterest): weekly("2026-10-06", 270000, 279515, 272462),
	}}
	ing := &CFTC{Client: client, Store: st, History: day("2000-01-01"), Log: slog.New(slog.DiscardHandler),
		Contracts: []config.CFTCContract{{Name: "Nasdaq-100 E-mini", Code: "209742", Agent: "usa_master"}},
		Now:       func() time.Time { return time.Date(2026, 10, 23, 20, 0, 0, 0, time.UTC) }}
	ctx := context.Background()
	res := ing.Run(ctx, Incremental)
	if res.Err != nil || res.Inserted != 6 || !res.LastReport.Equal(day("2026-10-20")) || !client.since.Equal(day("2000-01-01")) {
		t.Fatalf("first = %+v since %s", res, client.since)
	}
	if fs, _, _ := st.FetchStateOf(ctx, "cftc"); fs.Cursor != "2026-10-20" {
		t.Errorf("cursor = %q", fs.Cursor)
	}
	res = ing.Run(ctx, Incremental)
	if res.Unchanged != 6 || client.since.Format(time.DateOnly) != "2026-09-22" {
		t.Errorf("second = %+v since %s", res, client.since)
	}
	// A contract added to the config: back to history_start.
	ing.Contracts = append(ing.Contracts, config.CFTCContract{Name: "10Y T-Note", Code: "043602", Agent: "usa_master"})
	ing.Run(ctx, Incremental)
	if !client.since.Equal(day("2000-01-01")) || len(client.codes) != 2 {
		t.Errorf("new contract: since %s, codes %v", client.since, client.codes)
	}
	client.err = errors.New("503")
	if res = ing.Run(ctx, Incremental); res.Err == nil {
		t.Fatal("want error")
	}
	if fs, _, _ := st.FetchStateOf(ctx, "cftc"); fs.LastError != "503" || fs.Cursor != "2026-10-20" {
		t.Errorf("state after error = %+v", fs)
	}
}
