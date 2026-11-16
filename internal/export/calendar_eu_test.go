package export

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/finanzagentur"
	"macro-collector/internal/sources/relcal"
	"macro-collector/internal/store"
)

// calendar_eu.md of W46 from the recorded calendars (Eurostat .ics, ECB
// statistical calendar, Bund issuance outlook) and the config's dates (ZEW,
// EC survey): times in CET/CEST, the previous values of the series and of
// the last Bund auction of the tenor.
func TestEuropeCalendarGolden(t *testing.T) {
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
	read := func(parts ...string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(append([]string{repoRoot, "testdata"}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	// Events of 2026-11-01 … 2026-12-01 as the calendar refresh stores them.
	from, to := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	var events []model.ReleaseEvent
	for _, c := range cfg.ReleaseCalendars {
		var entries []relcal.Entry
		switch c.Kind {
		case config.RelCalICS:
			entries, err = relcal.ParseICS(read("relcal", "eurostat.ics"))
		case config.RelCalECBStats:
			entries, err = relcal.ParseECBStats(read("relcal", "ecb_statscal.html"))
		}
		if err != nil {
			t.Fatal(err)
		}
		evs, err := collect.ReleaseCalendarEvents(cfg, c, entries, from, to)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, evs...)
	}
	plan, err := finanzagentur.ParseOutlook(read("finanzagentur", "Issuance_outlook_2026_Update_Q4_en.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, collect.BundAuctionEvents(cfg, plan, from, to)...)
	results, err := finanzagentur.ParseResults(read("finanzagentur", "emissionsergebnisse_aktuell_en.xlsx"), time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	fetched := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	monthly := func(first string, vals ...float64) []model.Observation {
		d := day(first)
		out := make([]model.Observation, len(vals))
		for i, v := range vals {
			out[i] = model.Observation{Date: d.AddDate(0, i, 0), Value: v}
		}
		return out
	}
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		for _, e := range events {
			if _, err := store.SyncReleases(tx, e.Source, e.Name, e.ScheduledAt, e.ScheduledAt.Add(time.Second), []model.ReleaseEvent{e}); err != nil {
				return err
			}
		}
		if _, err := store.UpsertAuctions(tx, results, fetched); err != nil {
			return err
		}
		for _, s := range []struct {
			source, id, freq, units string
			obs                     []model.Observation
		}{
			{"zew", "ZEW_SENTIMENT", "m", "Balance", monthly("2026-08-01", 34.7, 37.3, 28.1)},
			{"ecb", "HICP_CORE", "m", "Index", monthly("2025-10-01", 128.0, 127.9, 128.4, 127.2, 127.6, 128.5, 129.0, 129.3, 129.6, 129.9, 129.8, 129.9, 130.6)},
		} {
			if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: s.source, SeriesID: s.id, Frequency: s.freq, Units: s.units}); err != nil {
				return err
			}
			if _, err := store.UpsertObservations(tx, s.source, s.id, s.obs, fetched); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	region := &cfg.Regions[0]
	x := &Weekly{Store: st, Config: cfg, Region: region, Now: func() time.Time { return time.Date(2026, 11, 15, 15, 0, 4, 0, time.UTC) }}
	end, err := PeriodEndOfWeek("2026-W46", cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	var md string
	for _, f := range files {
		if strings.HasSuffix(f.Path, "calendar_eu.md") {
			md = f.Body
		}
	}
	if md == "" {
		t.Fatal("no calendar_eu.md")
	}
	golden := filepath.Join(repoRoot, "testdata", "export", "eu_master_calendar_eu.md")
	if *update {
		if err := os.WriteFile(golden, []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil || !bytes.Equal(want, []byte(md)) {
		t.Errorf("calendar_eu.md differs from %s (%v; -update):\n%s", golden, err, md)
	}
}
