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
	"macro-collector/internal/store"
)

type fakeNumeric struct {
	series map[string][]model.Observation // by key
	fail   map[string]error
	since  map[string]time.Time
}

func (f *fakeNumeric) Name() string { return "ecb" }

func (f *fakeNumeric) FetchSeries(_ context.Context, key string, since time.Time) ([]model.Observation, error) {
	if f.since == nil {
		f.since = map[string]time.Time{}
	}
	f.since[key] = since
	if err := f.fail[key]; err != nil {
		return nil, err
	}
	var out []model.Observation
	for _, o := range f.series[key] {
		if !o.Date.Before(since) {
			out = append(out, o)
		}
	}
	return out, nil
}

func TestNumericRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	src := &fakeNumeric{
		series: map[string][]model.Observation{
			"FM/DFR": {{Date: day("2026-11-06"), Value: 2.5}, {Date: day("2026-11-09"), Value: 2.5}},
			"HICP/X": {{Date: day("2026-09-01"), Value: 104.31}, {Date: day("2026-10-01"), Value: 104.7}},
		},
		fail: map[string]error{"ZEW/BAD": errors.New("values out of range")},
	}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	ing := &Numeric{Source: src, Store: st, History: day("2000-01-01"), Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return now },
		Series: []config.SourceSeries{
			{ID: "ECB_DFR", Key: "FM/DFR", Agent: "eu_monetary", Label: "DFR", Frequency: "d", Units: config.UnitsRate},
			{ID: "BAD", Key: "ZEW/BAD", Agent: "eu_monetary", Frequency: "m", Units: config.UnitsBalance},
			{ID: "HICP", Key: "HICP/X", Agent: "eu_monetary", Frequency: "m", Units: config.UnitsIndex},
		}}

	rep := ing.Run(ctx, Incremental)
	if len(rep.Results) != 3 || len(rep.Failed()) != 1 || rep.Failed()[0].ID != "BAD" {
		t.Fatalf("report = %+v", rep)
	}
	if !src.since["FM/DFR"].Equal(day("2000-01-01")) {
		t.Errorf("first fetch since = %v", src.since["FM/DFR"])
	}
	obs, err := st.Observations(ctx, "ecb", "ECB_DFR", time.Time{})
	if err != nil || len(obs) != 2 {
		t.Fatalf("stored = %v, %v", obs, err)
	}
	meta, ok, err := st.Meta(ctx, "ecb", "HICP")
	if err != nil || !ok || meta.Frequency != "m" || meta.Units != config.UnitsIndex || meta.Agent != "eu_monetary" {
		t.Errorf("meta = %+v, %v, %v", meta, ok, err)
	}
	// The failed series stored nothing and its error is recorded.
	if bad, _ := st.Observations(ctx, "ecb", "BAD", time.Time{}); len(bad) != 0 {
		t.Errorf("failed series stored %d rows", len(bad))
	}
	fs, ok, err := st.FetchStateOf(ctx, "ecb:BAD")
	if err != nil || !ok || fs.LastError == "" {
		t.Errorf("fetch state of the failed series = %+v, %v, %v", fs, ok, err)
	}
	if fs, _, _ := st.FetchStateOf(ctx, "ecb:ECB_DFR"); fs.Cursor != "2026-11-09" || fs.LastError != "" {
		t.Errorf("fetch state = %+v", fs)
	}

	// The next run re-reads a revision window and stores a revised value.
	src.series["HICP/X"][1].Value = 104.77
	src.series["FM/DFR"] = append(src.series["FM/DFR"], model.Observation{Date: day("2026-11-10"), Value: 2.5})
	rep = ing.Run(ctx, Incremental)
	if !src.since["FM/DFR"].Equal(day("2025-11-09")) || !src.since["HICP/X"].Equal(day("2021-10-01")) {
		t.Errorf("incremental since = %v, %v", src.since["FM/DFR"], src.since["HICP/X"])
	}
	var ins, rev int
	for _, r := range rep.Results {
		ins, rev = ins+r.Inserted, rev+r.Revised
	}
	if ins != 1 || rev != 1 {
		t.Errorf("second run: inserted %d, revised %d", ins, rev)
	}
}
