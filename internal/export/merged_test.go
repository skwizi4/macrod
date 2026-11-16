package export

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/store"
)

// The merged USA + Europe config of the README migration (step 20) loads
// and exports both regions: shared news, Euro FX in markets_eu, the EU
// Micro issuers, the regions' titles.
func TestMergedConfigExportsBothRegions(t *testing.T) {
	t.Setenv(config.EnvDataDir, t.TempDir())
	cfg, err := config.LoadAt(filepath.Join(repoRoot, "testdata", "config", "us_eu.yaml"), repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if ids := cfg.RegionIDs(); !slices.Equal(ids, []string{"usa", "eu"}) {
		t.Fatalf("regions %v", ids)
	}
	if cfg.RegionTitle("usa") != "США" || cfg.RegionTitle("eu") != "Европа" {
		t.Errorf("titles %s, %s", cfg.RegionTitle("usa"), cfg.RegionTitle("eu"))
	}
	if r := cfg.RegionOfTag("eu_master"); r != "eu" {
		t.Errorf("eu_master in %q", r)
	}
	for _, c := range cfg.CFTC.Contracts {
		if c.Code == "099741" && c.Agent != "eu_master" {
			t.Errorf("Euro FX agent %s", c.Agent)
		}
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	paths := map[string][]string{}
	for i := range cfg.Regions {
		r := &cfg.Regions[i]
		x := &Weekly{Store: st, Config: cfg, Region: r, Now: func() time.Time { return ts(scenarioNow) }}
		end, err := PeriodEndOfWeek("2026-W43", cfg.Location)
		if err != nil {
			t.Fatal(err)
		}
		files, _, err := x.Render(ctx, end)
		if err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f.Path, ".md") && !f.ArchiveOnly {
				paths[r.ID] = append(paths[r.ID], f.Path)
			}
		}
	}
	for _, want := range []string{"usa/news/current_week.md", "usa/micro/current_week.md", "usa/master/markets_usa.md"} {
		if !slices.Contains(paths["usa"], want) {
			t.Errorf("usa lacks %s: %v", want, paths["usa"])
		}
	}
	for _, want := range []string{"eu/monetary_macro/current_week.md", "eu/fiscal_bonds/current_week.md", "eu/micro/current_week.md",
		"eu/news/current_week.md", "eu/news/sentiment.md", "eu/master/markets_eu.md", "eu/master/calendar_eu.md", "eu/master/key_metrics_eu.md"} {
		if !slices.Contains(paths["eu"], want) {
			t.Errorf("eu lacks %s: %v", want, paths["eu"])
		}
	}
}
