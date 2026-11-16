package forecast

import (
	"context"
	"database/sql"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	upBlock    = "```yaml\nforecast:\n  - instrument: NASDAQ\n    direction: up\n    invalidation: 24800\n```\n"
	downBlock  = "```yaml\nforecast:\n  - instrument: NASDAQ\n    direction: down\n    invalidation: 25600\n```\n"
	rangeBlock = "```yaml\nforecast:\n  - instrument: NASDAQ\n    direction: range\n    invalidation: 30000\n```\n"
)

// TestSyncScorecard is the acceptance test of phase 13: five weeks of the
// USA master's reports and closes of ^NDX and NASDAQCOM give the scorecard
// in testdata/scorecard_usa.golden.
func TestSyncScorecard(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.LoadAtWithDataDir(filepath.Join("..", "..", "presets", "us-equities.yaml"), filepath.Join("..", ".."), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	reports := map[string]string{
		"2026-W44": "Неделя: 2026-W44 | Агент: USA Master\n\n" + rangeBlock, // scored on the fallback
		"2026-W45": "Неделя: 2026-W45 | Агент: USA Master\n\n" + upBlock,    // hit
		"2026-W46": "Неделя: 2026-W46 | Агент: USA Master\n\n" + downBlock,  // invalidated
		"2026-W47": "Неделя: 2026-W47 | Агент: USA Master\n\nБез прогноза.\n",
		"2026-W48": "```yaml\nforecast:\n  - instrument: NASDAQ\n    direction: up\n    invalidation: 25 000\n```\n",
		"2026-W49": "Неделя: 2026-W49\n\n" + upBlock, // pending
	}
	for week, body := range reports {
		dir := filepath.Join(cfg.Export.ReportsDir, "usa", week)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "master.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A week folder with another agent's report only is not a master week.
	if err := os.MkdirAll(filepath.Join(cfg.Export.ReportsDir, "usa", "2026-W43"), 0o755); err != nil {
		t.Fatal(err)
	}

	ndx := []model.Observation{
		{Date: day("2026-11-06"), Value: 25000}, // reference of W45
		{Date: day("2026-11-09"), Value: 25100}, {Date: day("2026-11-10"), Value: 25200}, {Date: day("2026-11-11"), Value: 24900},
		{Date: day("2026-11-12"), Value: 25300}, {Date: day("2026-11-13"), Value: 25400}, // W46: up hit
		{Date: day("2026-11-16"), Value: 25500}, {Date: day("2026-11-17"), Value: 25700}, {Date: day("2026-11-18"), Value: 25300},
		{Date: day("2026-11-19"), Value: 25200}, {Date: day("2026-11-20"), Value: 25100}, // W47: above 25600 on Tue
		{Date: day("2026-12-04"), Value: 25000}, {Date: day("2026-12-07"), Value: 25100}, // W50 not over yet
	}
	comp := []model.Observation{{Date: day("2026-10-30"), Value: 23000}, {Date: day("2026-11-06"), Value: 23100}}
	put := func(name string, obs []model.Observation) {
		s, ok := cfg.ResolveSeries(name)
		if !ok {
			t.Fatalf("series %s", name)
		}
		if err := st.Write(ctx, func(tx *sql.Tx) error {
			_, err := store.UpsertObservations(tx, s.Source, s.ID, obs, time.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("^NDX", ndx)
	put("NASDAQCOM", comp)

	log := &Log{Store: st, Config: cfg}
	now := time.Date(2026, 12, 9, 12, 0, 0, 0, cfg.Location)
	res, err := log.Sync(ctx, "usa", now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Import.Weeks != 6 || res.Import.Items != 4 || len(res.Import.Notes) != 2 || res.Score.Scored != 3 || res.Score.Pending != 1 {
		t.Fatalf("result %+v", res)
	}
	golden := filepath.Join("testdata", "scorecard_usa.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(res.Scorecard), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if res.Scorecard != string(want) {
		t.Errorf("scorecard differs from %s (go test -run TestSyncScorecard -update):\n%s", golden, res.Scorecard)
	}

	// A second run changes nothing and keeps the scores.
	again, err := log.Sync(ctx, "usa", now)
	if err != nil || again.Import.Changed != 0 || again.Score.Scored != 0 || again.Scorecard != res.Scorecard {
		t.Fatalf("second run %+v, %v", again.Score, err)
	}
	// After W50 ends the pending forecast is scored (up: 25000 → 25100).
	later, err := log.Sync(ctx, "usa", time.Date(2026, 12, 13, 16, 0, 0, 0, cfg.Location))
	if err != nil || later.Score.Scored != 1 || !strings.Contains(later.Scorecard, "| 2026-W49 | 2026-W50 | NASDAQ | up | 24800 | 25000 (04.12) | 25100 (07.12) | +0.40 % | ✅ hit |") {
		t.Fatalf("later %+v, %v\n%s", later.Score, err, later.Scorecard)
	}
	// The Friday close comes in late (a failed poll): the recent score is
	// recomputed — 24700 closes below the level.
	put("^NDX", []model.Observation{{Date: day("2026-12-11"), Value: 24700}})
	fixed, err := log.Sync(ctx, "usa", time.Date(2026, 12, 14, 9, 0, 0, 0, cfg.Location))
	if err != nil || fixed.Score.Scored != 1 || !strings.Contains(fixed.Scorecard, "| 2026-W49 | 2026-W50 | NASDAQ | up | 24800 | 25000 (04.12) | 24700 (11.12) | -1.20 % | ⛔ invalidated |") {
		t.Fatalf("late close %+v, %v\n%s", fixed.Score, err, fixed.Scorecard)
	}
	// Old scores are not rechecked: W45's week ended a month ago.
	put("^NDX", []model.Observation{{Date: day("2026-11-13"), Value: 1}})
	if old, err := log.Sync(ctx, "usa", time.Date(2026, 12, 14, 10, 0, 0, 0, cfg.Location)); err != nil || old.Score.Scored != 0 {
		t.Fatalf("old score rechecked: %+v, %v", old.Score, err)
	}
}

// A pending forecast whose week has passed without closes is marked, not
// left as an ordinary "ожидает".
func TestRenderOverdue(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Bratislava")
	rows := []store.Forecast{
		{ISOWeek: "2026-W45", ForecastItem: store.ForecastItem{Instrument: "GER40", Direction: Range, Invalidation: 23500}, Outcome: store.OutcomePending},
		{ISOWeek: "2026-W46", ForecastItem: store.ForecastItem{Instrument: "GER40", Direction: Up, Invalidation: 23000}, Outcome: store.OutcomePending},
	}
	md := Render(Card{Title: "Европа", Rows: rows, RangePct: 1, Now: time.Date(2026, 11, 18, 12, 0, 0, 0, loc), Loc: loc})
	for _, want := range []string{
		"| 2026-W45 | 2026-W46 | GER40 | range | 23500 | — | — | — | ⚠ нет закрытий за неделю прогноза |",
		"| 2026-W46 | 2026-W47 | GER40 | up | 23000 | — | — | — | ⏳ ожидает |",
		"Ожидают оценки: 2 (из них 1 — неделя прошла, а закрытий нет: проверьте загрузку цен).",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("no %q in\n%s", want, md)
		}
	}
}
