package ingest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/store"
)

func TestManualInput(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{Manual: config.Manual{Series: []config.SourceSeries{
		{ID: "IFO_CLIMATE", Agent: "eu_monetary", Label: "Ifo Business Climate", Frequency: "m", Units: config.UnitsIndex},
	}}}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	res, err := Manual(ctx, cfg, st, "IFO_CLIMATE", 90.2, "2026-10", now)
	if err != nil || res.Inserted != 1 || res.Date.Format(time.DateOnly) != "2026-10-01" {
		t.Fatalf("first input = %+v, %v", res, err)
	}
	meta, ok, err := st.Meta(ctx, config.SourceManual, "IFO_CLIMATE")
	if err != nil || !ok || meta.Frequency != "m" || meta.Units != config.UnitsIndex || meta.Label != "Ifo Business Climate" || meta.Agent != "eu_monetary" {
		t.Errorf("meta = %+v, %v, %v", meta, ok, err)
	}
	if fs, ok, _ := st.FetchStateOf(ctx, "manual:IFO_CLIMATE"); !ok || fs.Cursor != "2026-10-01" || fs.LastError != "" {
		t.Errorf("fetch state = %+v", fs)
	}

	// The same value again changes nothing; a corrected one is a revision.
	if res, err := Manual(ctx, cfg, st, "IFO_CLIMATE", 90.2, "2026-10", now); err != nil || res.Unchanged != 1 {
		t.Errorf("repeat = %+v, %v", res, err)
	}
	res, err = Manual(ctx, cfg, st, "IFO_CLIMATE", 90.4, "2026-10", now.Add(time.Hour))
	if err != nil || res.Revised != 1 || res.Old != 90.2 {
		t.Errorf("correction = %+v, %v", res, err)
	}
	revs, err := st.Revisions(ctx, config.SourceManual, "IFO_CLIMATE", now, now.Add(2*time.Hour))
	if err != nil || len(revs) != 1 || revs[0].Old != 90.2 || revs[0].New != 90.4 {
		t.Errorf("revisions = %+v, %v", revs, err)
	}
	if _, err := Manual(ctx, cfg, st, "IFO_CLIMATE", 89.9, "2026-09", now); err != nil {
		t.Fatal(err)
	}
	obs, err := st.Observations(ctx, config.SourceManual, "IFO_CLIMATE", time.Time{})
	if err != nil || len(obs) != 2 || obs[0].Value != 89.9 || obs[1].Value != 90.4 {
		t.Errorf("stored = %+v, %v", obs, err)
	}

	for name, tt := range map[string]struct {
		id, period, want string
	}{
		"unknown series": {"ZEW", "2026-10", `series "ZEW" is not in manual.series of the config (have: IFO_CLIMATE)`},
		"day for month":  {"IFO_CLIMATE", "2026-10-26", "series IFO_CLIMATE is m, want YYYY-MM"},
		"quarter":        {"IFO_CLIMATE", "2026-Q3", "want YYYY-MM"},
		"bad month":      {"IFO_CLIMATE", "2026-13", "want YYYY-MM"},
		"future":         {"IFO_CLIMATE", "2026-12", "period 2026-12 is in the future"},
	} {
		if _, err := Manual(ctx, cfg, st, tt.id, 1, tt.period, now); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tt.want)
		}
	}
	if obs, _ := st.Observations(ctx, config.SourceManual, "IFO_CLIMATE", time.Time{}); len(obs) != 2 {
		t.Errorf("a rejected input was stored: %+v", obs)
	}
}
