package export

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/store"
)

// EU Monetary & Macro of W45 (2026-11-01 … 11-08): the European tables, a
// ZEW release of the week (the calendar event names it), the ECB documents
// of the week and the next Governing Council meeting; no FOMC path and no
// Fed texts (the agent reads ecb_texts only).
func TestEuropeMonetaryGolden(t *testing.T) {
	t.Setenv(config.EnvDataDir, t.TempDir())
	cfg, err := presetConfigErr(t, "eu-equities", "eu", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	history := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC) // initial load: not a release
	release := time.Date(2026, 11, 3, 10, 7, 0, 0, time.UTC)
	monthly := func(first string, vals ...float64) []model.Observation {
		d := day(first)
		out := make([]model.Observation, len(vals))
		for i, v := range vals {
			out[i] = model.Observation{Date: d.AddDate(0, i, 0), Value: v}
		}
		return out
	}
	quarterly := func(first string, vals ...float64) []model.Observation {
		d := day(first)
		out := make([]model.Observation, len(vals))
		for i, v := range vals {
			out[i] = model.Observation{Date: d.AddDate(0, 3*i, 0), Value: v}
		}
		return out
	}
	put := func(tx *sql.Tx, source, id, freq, units string, obs []model.Observation, at time.Time) error {
		if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: source, SeriesID: id, Frequency: freq, Units: units}); err != nil {
			return err
		}
		_, err := store.UpsertObservations(tx, source, id, obs, at)
		return err
	}
	zewEvent := model.ReleaseEvent{Source: "relcal:zew", Name: "ZEW (Германия)", ScheduledAt: time.Date(2026, 11, 3, 10, 5, 0, 0, time.UTC),
		Region: "eu", Importance: "high", Related: model.Related{Series: []string{"ZEW_SENTIMENT", "ZEW_SITUATION"}}}
	meeting := model.ReleaseEvent{Source: ecbtexts.SourceMeeting, Name: ecbtexts.NameDecision, ScheduledAt: time.Date(2026, 12, 17, 13, 15, 0, 0, time.UTC),
		Region: "eu", Importance: "high"}
	speech := model.Document{Source: ecbtexts.Source, ExternalID: "ecb.sp261105~test", DocType: ecbtexts.DocSpeech, Region: "eu",
		PublishedAt: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC), Author: "Christine Lagarde", Title: "Monetary policy in a changing world",
		Body: "Inflation is close to our target.", URL: "https://www.ecb.europa.eu/press/key/date/2026/html/ecb.sp261105~test.en.html"}
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		for _, e := range []model.ReleaseEvent{zewEvent, meeting} {
			if _, err := store.SyncReleases(tx, e.Source, e.Name, e.ScheduledAt, e.ScheduledAt.Add(time.Second), []model.ReleaseEvent{e}); err != nil {
				return err
			}
		}
		if _, _, err := store.UpsertDocument(tx, speech, speech.PublishedAt); err != nil {
			return err
		}
		for _, s := range []struct {
			source, id, freq, units string
			obs                     []model.Observation
		}{
			{"eurostat", "GDP_EA", "q", "Millions of Euro", quarterly("2025-07-01", 3320000, 3333000, 3345000, 3359489, 3373440)},
			{"ecb", "HICP", "m", "Index", monthly("2025-10-01", 100.74, 100.9, 101.2, 101.0, 101.5, 102.1, 102.5, 102.8, 103.0, 103.24, 103.9, 104.31, 104.77)},
			{"ecb", "HICP_SA", "m", "Index", monthly("2026-04-01", 102.6, 102.9, 103.0, 103.04, 103.6, 104.17, 104.51)},
			{"ecb", "ECB_DFR", "d", config.UnitsRate, weekdays("2026-11-06", 40, 2.5)},
			{"ecb", "ECB_ASSETS", "w", config.UnitsMillionsEUR, []model.Observation{{Date: day("2026-10-09"), Value: 5944000}, {Date: day("2026-10-30"), Value: 5945410}, {Date: day("2026-11-06"), Value: 5913173}}},
		} {
			if err := put(tx, s.source, s.id, s.freq, s.units, s.obs, history); err != nil {
				return err
			}
		}
		// ZEW: history first, then the November release in the week.
		if err := put(tx, "zew", "ZEW_SENTIMENT", "m", "Balance", monthly("2026-08-01", 34.7, 37.3), history); err != nil {
			return err
		}
		return put(tx, "zew", "ZEW_SENTIMENT", "m", "Balance", monthly("2026-10-01", 28.1), release)
	}); err != nil {
		t.Fatal(err)
	}

	x := &Weekly{Store: st, Config: cfg, Region: &cfg.Regions[0], Now: func() time.Time { return time.Date(2026, 11, 8, 15, 0, 4, 0, time.UTC) }}
	end, err := PeriodEndOfWeek("2026-W45", cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	var md string
	var m MonetaryReport
	for _, f := range files {
		switch {
		case strings.HasSuffix(f.Path, "eu/monetary_macro/current_week.md"):
			md = f.Body
		case strings.HasSuffix(f.Path, "eu/monetary_macro/current_week.json"):
			if err := json.Unmarshal([]byte(f.Body), &m); err != nil {
				t.Fatal(err)
			}
		}
	}
	if md == "" {
		t.Fatal("no eu/monetary_macro/current_week.md")
	}
	if m.ECBTexts == nil || len(m.ECBTexts.Documents) != 1 || m.ECBTexts.Next == nil || len(m.FedPath.Rows) != 0 {
		t.Errorf("ecb texts %+v, fed path rows %d", m.ECBTexts, len(m.FedPath.Rows))
	}
	for _, bad := range []string{"USA", "ФРС", "FOMC", "ET)"} {
		if strings.Contains(md, bad) {
			t.Errorf("current_week.md mentions %q", bad)
		}
	}
	golden := filepath.Join(repoRoot, "testdata", "export", "eu_monetary_macro_current_week.md")
	if *update {
		if err := os.WriteFile(golden, []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil || !bytes.Equal(want, []byte(md)) {
		t.Errorf("current_week.md differs from %s (%v; -update):\n%s", golden, err, md)
	}
}
