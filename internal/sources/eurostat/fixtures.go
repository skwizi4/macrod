package eurostat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Fixture is one recorded response in testdata/eurostat; `macroctl
// record-fixtures --source eurostat` refreshes them.
type Fixture struct {
	Name, Key string
	From, To  string // dates, pinned (the request is cut by year)
}

// File is the fixture's file name.
func (f Fixture) File() string { return f.Name + ".json" }

// Fixtures cover a quarterly and a monthly series, a series with missing
// first periods, and a key that leaves a dimension open.
var Fixtures = []Fixture{
	{"gdp_ea20", "namq_10_gdp?geo=EA20&unit=CLV20_MEUR&s_adj=SCA&na_item=B1GQ", "2024-01-01", "2026-11-09"},
	{"ici_de", "ei_bssi_m_r2?geo=DE&indic=BS-ICI-BAL&s_adj=SA", "2025-01-01", "2026-11-09"},
	{"debt_de", "gov_10q_ggdebt?geo=DE&unit=PC_GDP&sector=S13&na_item=GD", "2024-01-01", "2026-11-09"},
	{"deficit_it_open_s_adj", "gov_10q_ggnfa?geo=IT&unit=PC_GDP&sector=S13&na_item=B9", "2025-01-01", "2026-11-09"},
}

// RecordFixtures fetches every fixture into dir.
func (c *Client) RecordFixtures(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("eurostat record mkdir %s: %w", dir, err)
	}
	for _, f := range Fixtures {
		from, err := time.Parse(time.DateOnly, f.From)
		if err != nil {
			return err
		}
		to, err := time.Parse(time.DateOnly, f.To)
		if err != nil {
			return err
		}
		u, err := c.URL(f.Key, from, to)
		if err != nil {
			return err
		}
		body, err := c.http.Get(ctx, u)
		if err != nil {
			return fmt.Errorf("eurostat record %s: %w", f.Name, err)
		}
		// The DOI annotations carry nothing the parser reads; keep the file as sent.
		path := filepath.Join(dir, f.File())
		if err := os.WriteFile(path, []byte(strings.TrimSpace(string(body))+"\n"), 0o644); err != nil {
			return fmt.Errorf("eurostat record %s: %w", f.Name, err)
		}
		c.log.Info("fixture recorded", "file", path, "bytes", len(body))
	}
	return nil
}
