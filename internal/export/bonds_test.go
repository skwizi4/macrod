package export

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/derived"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/finanzagentur"
	"macro-collector/internal/store"
)

// EU Fiscal & Bonds of W45 from the recorded Finanzagentur results (the
// auctions of 2026-11-02 … 11-06 against the six before them), the planned
// auctions of the next week and a few curve and spread series.
func TestEuropeBondsGolden(t *testing.T) {
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
	results, err := finanzagentur.ParseResults(read("finanzagentur", "emissionsergebnisse_aktuell_en.xlsx"), time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := finanzagentur.ParseOutlook(read("finanzagentur", "Issuance_outlook_2026_Update_Q4_en.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	next := collect.BundAuctionEvents(cfg, plan, time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	fetched := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := store.UpsertAuctions(tx, results, fetched); err != nil {
			return err
		}
		for _, e := range next {
			if _, err := store.SyncReleases(tx, e.Source, e.Name, e.ScheduledAt, e.ScheduledAt.Add(time.Second), []model.ReleaseEvent{e}); err != nil {
				return err
			}
		}
		for _, s := range []struct {
			source, id, freq, units string
			obs                     []model.Observation
		}{
			{"bundesbank", "BUND_10Y", "d", config.UnitsRate, weekdays("2026-11-06", 40, 3.28)},
			{"bundesbank", "BUND_2Y", "d", config.UnitsRate, weekdays("2026-11-06", 40, 2.99)},
			{"ecb", "IRS_DE_10Y", "m", config.UnitsRate, []model.Observation{{Date: day("2026-09-01"), Value: 3.473}, {Date: day("2026-10-01"), Value: 3.42}}},
			{"eurostat", "DE_DEBT_GDP", "q", "Percent of GDP", []model.Observation{{Date: day("2026-01-01"), Value: 63.4}, {Date: day("2026-04-01"), Value: 63.1}}},
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
	comp := &derived.Computer{Store: st, Config: cfg, Defs: cfg.Derived, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return fetched }}
	comp.Run(ctx) // BUND_2S10S; the others lack their inputs here

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
	for _, f := range files {
		if strings.HasSuffix(f.Path, "eu/fiscal_bonds/current_week.md") {
			md = f.Body
		}
	}
	for _, want := range []string{
		"| 2026-11-03 | Bobl 5 Y | DE000BU25075 | доразм. | 2.90% | 5.0 млрд € | 5.3 млрд € | 3.8 млрд € | 1.2 млрд € | 3.140% |",
		"2026-11-11 11:30 Bund 30 years (DE000BU2D012)",
		"| 2s10s Bund | 2026-11-06 | 29 б.п. |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("fiscal_bonds has no %q", want)
		}
	}
	golden := filepath.Join(repoRoot, "testdata", "export", "eu_fiscal_bonds_current_week.md")
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
