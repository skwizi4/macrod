package export

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/store"
)

var repoRoot = filepath.Join("..", "..")

// regionConfig loads the us-equities preset with its regions block replaced,
// as a user's config file would be: no Go code knows the new region.
func regionConfig(t *testing.T, regions string) *config.Config {
	t.Helper()
	t.Setenv(config.EnvDataDir, t.TempDir())
	raw, err := os.ReadFile(filepath.Join(repoRoot, "presets", "us-equities.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	from, to := strings.Index(s, "\nregions:\n"), strings.Index(s, "\ngit:\n")
	if from < 0 || to < from {
		t.Fatal("regions block not found")
	}
	s = s[:from] + "\n" + regions + s[to:]
	s = strings.ReplaceAll(s, "region: usa,", "region: testland,")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadAt(path, repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

const testlandRegions = `templates_dir: "testdata/templates"
regions:
  - id: testland
    yahoo_poll: "21:00"
    agents:
      - id: desk
        folder: desk_folder
        series_tags: [usa_monetary, usa_fiscal, usa_master, usa_news, usa_micro]
        reports:
          - report: markets
            file: overview.md
            title: "Testland Overview"
            template: testland_markets
            tables: { rows: ["^NDX", DGS10] }
`

// ТЗ §21.3: a region of one agent, with its own folder and template, comes
// from the config alone.
func TestRegionFromConfigOnly(t *testing.T) {
	cfg := regionConfig(t, testlandRegions)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seed(t, cfg, st)
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts(scenarioNow) }}
	end, err := PeriodEndOfWeek("2026-W43", cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	res, err := x.Run(ctx, end, false)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range res.Files {
		paths = append(paths, f.Path)
	}
	want := []string{filepath.Join("testland", "desk_folder", "overview.md"), filepath.Join("json", "testland", "desk_folder", "overview.json")}
	if !slices.Equal(paths, want) {
		t.Fatalf("files = %v, want %v", paths, want)
	}
	golden := filepath.Join(repoRoot, "testdata", "export", "region_testland_overview.md")
	if *update {
		if err := os.WriteFile(golden, []byte(res.Files[0].Body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wantBody, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files[0].Body != string(wantBody) {
		t.Errorf("overview.md = %q, want %q", res.Files[0].Body, wantBody)
	}
	var m MarketsReport
	if err := json.Unmarshal([]byte(res.Files[1].Body), &m); err != nil {
		t.Fatal(err)
	}
	if m.Header.Region != "testland" || m.Header.Agent != "desk" || !strings.HasPrefix(m.Header.Title, "Testland Overview — ") || len(m.Rows) != 2 {
		t.Errorf("model = %+v", m.Header)
	}
	if _, err := os.Stat(filepath.Join(cfg.Export.ExchangeDir, "testland", "desk_folder", "overview.md")); err != nil {
		t.Error(err)
	}
	if _, ok, err := st.LastExport(ctx, KindWeekly, "testland"); err != nil || !ok {
		t.Errorf("export of the region not recorded: %v %v", ok, err)
	}
}

func TestPresetsRender(t *testing.T) {
	entries, err := filepath.Glob(filepath.Join(repoRoot, "presets", "*.yaml"))
	if err != nil || len(entries) < 2 {
		t.Fatalf("presets = %v, %v", entries, err)
	}
	for _, p := range entries {
		t.Run(filepath.Base(p), func(t *testing.T) {
			t.Setenv(config.EnvDataDir, t.TempDir())
			cfg, err := config.LoadAt(p, repoRoot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := LoadTemplates(cfg); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			st, err := store.Open(ctx, cfg.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			for i := range cfg.Regions {
				x := &Weekly{Store: st, Config: cfg, Region: &cfg.Regions[i], Now: func() time.Time { return ts(scenarioNow) }}
				end, err := PeriodEndOfWeek("2026-W43", cfg.Location)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := x.Render(ctx, end); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// The example config is the us-equities preset, kept in step.
func TestExampleConfigIsPreset(t *testing.T) {
	example, err := os.ReadFile(filepath.Join(repoRoot, "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	preset, err := os.ReadFile(filepath.Join(repoRoot, "presets", "us-equities.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(example) != string(preset) {
		t.Error("config.example.yaml differs from presets/us-equities.yaml")
	}
}

// Tables can name exactly the Treasury series the export can load.
func TestTreasurySeriesInConfig(t *testing.T) {
	var keys []string
	for id := range treasurySpecs {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	want := slices.Sorted(slices.Values(config.TreasurySeries))
	if !slices.Equal(keys, want) {
		t.Errorf("treasurySpecs = %v, config.TreasurySeries = %v", keys, want)
	}
}

func TestLoadTemplatesMissing(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.Regions[0].Agents[0].Reports[0].Template = "no_such_template"
	if _, err := LoadTemplates(cfg); err == nil || !strings.Contains(err.Error(), "no_such_template") {
		t.Errorf("err = %v", err)
	}
}

// ТЗ §6.1: a table names a series by source/id as well as by id, and a
// series marked as a stand-in says so in both renders.
func TestSeriesAddressAndQuality(t *testing.T) {
	regions := strings.Replace(testlandRegions, `rows: ["^NDX", DGS10]`, `rows: ["fred/DGS10", UST_2S10S]`, 1)
	if regions == testlandRegions {
		t.Fatal("fixture changed")
	}
	cfg := regionConfig(t, regions)
	for i := range cfg.Derived {
		if cfg.Derived[i].Name == "UST_2S10S" {
			cfg.Derived[i].Quality = config.QualityProxy
		}
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seed(t, cfg, st)
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts(scenarioNow) }}
	end, err := PeriodEndOfWeek("2026-W43", cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	res, err := x.Run(ctx, end, false)
	if err != nil {
		t.Fatal(err)
	}
	var m MarketsReport
	if err := json.Unmarshal([]byte(res.Files[1].Body), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(m.Rows))
	}
	if r := m.Rows[0]; r.Source != config.SourceFRED || r.ID != "DGS10" || r.Quality != config.QualityOfficial || slices.Contains(r.Flags, "прокси") {
		t.Errorf("row 0 = %s/%s quality %q flags %v, want fred/DGS10, official, no mark", r.Source, r.ID, r.Quality, r.Flags)
	}
	if r := m.Rows[1]; r.Source != config.SourceDerived || r.Quality != config.QualityProxy || !slices.Contains(r.Flags, "прокси") {
		t.Errorf("row 1 = %s/%s quality %q flags %v, want a derived series marked as proxy", r.Source, r.ID, r.Quality, r.Flags)
	}
}
