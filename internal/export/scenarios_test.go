package export

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/store"
)

// These golden files pin the Markdown of paths the main W43 golden does not
// reach: other weeks, failed sources, the size limits, an empty database and
// the variants of fomc_diff.md. Phase U2 (report models) must keep them
// byte for byte: never -update them during that phase.

const scenarioNow = "2026-11-08T15:00:04Z"

// scenarioRun opens a store, runs prepare on it and renders week.
func scenarioRun(t *testing.T, iso string, prepare func(*config.Config, *store.Store)) []File {
	t.Helper()
	cfg := testConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	prepare(cfg, st)
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts(scenarioNow) }}
	end, err := PeriodEndOfWeek(iso, cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func seedAll(t *testing.T, cfg *config.Config, st *store.Store) {
	t.Helper()
	seed(t, cfg, st)
	seedPosts(t, st, weekPosts)
	seedMicro(t, cfg, st)
}

// scenarioCheck compares the Markdown files of a scenario, those whose path
// contains one of only (all without it), with their golden files.
func scenarioCheck(t *testing.T, scenario string, files []File, only ...string) {
	t.Helper()
	checkReportJSON(t, files)
	seen := 0
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".md") {
			continue
		}
		if len(only) > 0 {
			match := false
			for _, s := range only {
				match = match || strings.Contains(f.Path, s)
			}
			if !match {
				continue
			}
		}
		seen++
		name := "scenario_" + scenario + "_" + strings.ReplaceAll(f.Path, string(filepath.Separator), "_")
		golden := filepath.Join("..", "..", "testdata", "export", name)
		if *update {
			if err := os.WriteFile(golden, []byte(f.Body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if f.Body != string(want) {
			t.Errorf("%s differs from %s", f.Path, golden)
		}
	}
	if seen == 0 {
		t.Errorf("scenario %s: no files compared", scenario)
	}
}

func TestScenarioOtherWeeks(t *testing.T) {
	t.Parallel()
	for _, iso := range []string{"2026-W42", "2026-W44"} {
		t.Run(iso, func(t *testing.T) {
			t.Parallel()
			scenarioCheck(t, iso, scenarioRun(t, iso, func(cfg *config.Config, st *store.Store) { seedAll(t, cfg, st) }))
		})
	}
}

func TestScenarioEmptyDatabase(t *testing.T) {
	t.Parallel()
	scenarioCheck(t, "empty", scenarioRun(t, "2026-W43", func(*config.Config, *store.Store) {}))
}

func TestScenarioFailedSources(t *testing.T) {
	t.Parallel()
	files := scenarioRun(t, "2026-W43", func(cfg *config.Config, st *store.Store) {
		seedAll(t, cfg, st)
		at := ts("2026-10-25T05:15:00Z")
		keys := []string{"fred:DGS10", "fred:WALCL", "treasury:auctions", "treasury:dts", "fed:rss", "edgar:MSFT",
			"calendar:earnings", "cboe:putcall", "kalshi:KXFED", "telegram:macro_views"}
		for _, s := range cfg.Yahoo.Symbols {
			keys = append(keys, "yahoo:"+s.Symbol)
		}
		// Symbols of another region fail too: their errors are still listed
		// by source name in this golden.
		for _, s := range []string{"^GDAXI", "^STOXX50E", "EURUSD=X", "CNY=X", "BZ=F", "TTF=F"} {
			keys = append(keys, "yahoo:"+s)
		}
		if err := st.Write(context.Background(), func(tx *sql.Tx) error {
			for _, k := range keys {
				if err := store.SetFetchError(tx, k, errors.New(k+": status 503"), at); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	scenarioCheck(t, "failed", files)
}

func TestScenarioSizeLimits(t *testing.T) {
	t.Parallel()
	render := func(t *testing.T, limit int) []File {
		return scenarioRun(t, "2026-W43", func(cfg *config.Config, st *store.Store) {
			cfg.Export.MaxFileChars = limit
			seedAll(t, cfg, st)
		})
	}
	size := func(files []File, part string) int {
		for _, f := range files {
			if strings.Contains(f.Path, part) && strings.HasSuffix(f.Path, "current_week.md") {
				return len([]rune(f.Body))
			}
		}
		t.Fatalf("no file %s", part)
		return 0
	}
	whole := render(t, 0)
	news, micro := size(whole, "news"), size(whole, "micro")
	for name, c := range map[string]struct {
		limit int
		part  string
	}{
		"news_some":   {news - 200, filepath.Join("usa", "news", "current_week")},
		"news_all":    {100, filepath.Join("usa", "news", "current_week")},
		"micro_near":  {micro - 50, filepath.Join("usa", "micro")},
		"micro_half":  {micro / 2, filepath.Join("usa", "micro")},
		"micro_floor": {100, filepath.Join("usa", "micro")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			scenarioCheck(t, name, render(t, c.limit), c.part)
		})
	}
}

// fomcDocs are a meeting with its note and transcript and, optionally, the
// meeting before it.
func fomcDocs(previous bool) []model.Document {
	docs := []model.Document{
		{Source: fedtexts.Source, ExternalID: "monetary20261028a", DocType: fedtexts.DocStatement, Region: "usa", PublishedAt: ts("2026-10-28T18:00:00Z"), URL: "https://example.test/oct",
			Title: "Federal Reserve issues FOMC statement", Body: "The Federal Open Market Committee approved the following statement for release by a 10 – 2 vote:\n" +
				"The Committee decided to maintain the target range for the federal funds rate at 3-3/4 to 4 percent.\n" +
				"Voting against the monetary policy action were Lisa D. Cook and Beth M. Hammack, who preferred to raise the target range for the federal funds rate by 1/4 percentage point at this meeting.\n"},
		{Source: fedtexts.Source, ExternalID: "monetary20261028a1", DocType: fedtexts.DocImplementationNote, Region: "usa", PublishedAt: ts("2026-10-28T18:00:00Z"), URL: "https://example.test/note",
			Title: "Implementation Note issued October 28, 2026", Body: "The Board voted to maintain IORB at 3.90 percent."},
		{Source: fedtexts.Source, ExternalID: "FOMCpresconf20261028", DocType: fedtexts.DocPressConference, Region: "usa", PublishedAt: ts("2026-10-29T15:22:11Z"),
			Title: "Transcript of Chair's Press Conference", Body: "CHAIRMAN. Good day."},
	}
	if previous {
		docs = append(docs, model.Document{Source: fedtexts.Source, ExternalID: "monetary20260916a", DocType: fedtexts.DocStatement, Region: "usa", PublishedAt: ts("2026-09-16T18:00:00Z"),
			Title: "Federal Reserve issues FOMC statement", Body: "The Committee decided to raise the target range for the federal funds rate by 1/4 percentage point to 3-3/4 to 4 percent.\n"})
	}
	return docs
}

func TestScenarioFOMC(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, iso string
		previous  bool
	}{
		{"fomc_week", "2026-W44", true},    // the meeting's week: every file with the Fed texts
		{"fomc_later", "2026-W45", true},   // a later week: the notice
		{"fomc_noprev", "2026-W44", false}, // no earlier statement
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			files := scenarioRun(t, c.iso, func(cfg *config.Config, st *store.Store) {
				seed(t, cfg, st)
				for _, d := range fomcDocs(c.previous) {
					seeder{t, st}.write(func(tx *sql.Tx) error { _, _, err := store.UpsertDocument(tx, d, initialLoadAt); return err })
				}
			})
			if c.name == "fomc_week" {
				scenarioCheck(t, c.name, files)
			} else {
				scenarioCheck(t, c.name, files, "fomc_diff")
			}
		})
	}
}
