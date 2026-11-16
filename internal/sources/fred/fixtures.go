package fred

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// Fixture is one recorded API response in testdata/fred. Tests serve these
// files instead of calling FRED; `macroctl record-fixtures` refreshes them.
type Fixture struct {
	Name       string // file name without .json
	Endpoint   string
	Params     url.Values
	WantStatus int // expected HTTP status of the recording
}

// Fixtures are pinned to fixed date ranges so re-recording only picks up
// revisions, not new observations.
var Fixtures = []Fixture{
	{"series_DGS10", "series", url.Values{"series_id": {"DGS10"}}, 200},
	{"series_PAYEMS", "series", url.Values{"series_id": {"PAYEMS"}}, 200},
	{"error_bad_series", "series", url.Values{"series_id": {"NOT_A_SERIES_ID"}}, 400},
	// Daily with "." holidays (2025-12-25, 2026-01-01).
	{"observations_DGS10", "series/observations", obs("DGS10", "2025-12-15", "2026-01-09", "lin"), 200},
	{"observations_PAYEMS", "series/observations", obs("PAYEMS", "2024-01-01", "2026-09-01", "lin"), 200},
	// Levels plus FRED's own transforms, reference values for internal/transform.
	{"observations_CPIAUCSL_lin", "series/observations", obs("CPIAUCSL", "2018-01-01", "2026-09-01", "lin"), 200},
	{"observations_CPIAUCSL_pc1", "series/observations", obs("CPIAUCSL", "2018-01-01", "2026-09-01", "pc1"), 200},
	{"observations_CPIAUCSL_pch", "series/observations", obs("CPIAUCSL", "2018-01-01", "2026-09-01", "pch"), 200},
	{"observations_GDPC1_lin", "series/observations", obs("GDPC1", "2015-01-01", "2026-04-01", "lin"), 200},
	{"observations_GDPC1_pca", "series/observations", obs("GDPC1", "2015-01-01", "2026-04-01", "pca"), 200},
	// Release calendar (internal/calendar): one series per time-of-day group.
	{"series_release_CPIAUCSL", "series/release", url.Values{"series_id": {"CPIAUCSL"}}, 200},
	{"series_release_ICSA", "series/release", url.Values{"series_id": {"ICSA"}}, 200},
	{"series_release_DGS10", "series/release", url.Values{"series_id": {"DGS10"}}, 200},
	{"series_release_WALCL", "series/release", url.Values{"series_id": {"WALCL"}}, 200},
	{"series_release_JTSJOL", "series/release", url.Values{"series_id": {"JTSJOL"}}, 200},
	// 16 days across the 2026-11-01 DST switch, up to CPI on 2026-11-10.
	{"releases_dates", "releases/dates", url.Values{
		"realtime_start":                     {"2026-10-26"},
		"realtime_end":                       {"2026-11-10"},
		"include_release_dates_with_no_data": {"true"},
		"order_by":                           {"release_date"},
		"sort_order":                         {"asc"},
		"limit":                              {"1000"},
	}, 200},
}

func obs(id, start, end, units string) url.Values {
	return url.Values{
		"series_id":         {id},
		"observation_start": {start},
		"observation_end":   {end},
		"units":             {units},
		"sort_order":        {"asc"},
	}
}

// RecordFixtures fetches every fixture from the live API into dir.
func (c *Client) RecordFixtures(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fred record mkdir %s: %w", dir, err)
	}
	for _, f := range Fixtures {
		status, body, err := c.raw(ctx, f.Endpoint, f.Params)
		if err != nil {
			return fmt.Errorf("fred record %s: %w", f.Name, err)
		}
		if status != f.WantStatus {
			return fmt.Errorf("fred record %s: status %d, want %d: %s", f.Name, status, f.WantStatus, body)
		}
		path := filepath.Join(dir, f.Name+".json")
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return fmt.Errorf("fred record %s: %w", f.Name, err)
		}
		c.log.Info("fixture recorded", "file", path, "bytes", len(body))
	}
	return nil
}
