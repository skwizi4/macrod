package ecbdata

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Fixture is one recorded response in testdata/ecb; `macroctl
// record-fixtures --source ecb` refreshes them.
type Fixture struct {
	Key      string
	From, To string // dates, pinned
}

// File is the fixture's file name.
func (f Fixture) File() string {
	return strings.NewReplacer("/", "_", ".", "_").Replace(f.Key) + ".csv"
}

// Fixtures cover a daily, a business-daily, a weekly and a monthly series.
var Fixtures = []Fixture{
	{"FM/D.U2.EUR.4F.KR.DFR.LEV", "2026-10-01", "2026-11-09"},
	{"EST/B.EU000A2X2A25.WT", "2026-10-01", "2026-11-09"},
	{"ILM/W.U2.C.T000000.Z5.Z01", "2026-09-01", "2026-11-09"},
	{"HICP/M.U2.N.000000.4D0.INX", "2025-01-01", "2026-11-09"},
}

// RecordFixtures fetches every fixture into dir.
func (c *Client) RecordFixtures(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ecb record mkdir %s: %w", dir, err)
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
		body, err := c.http.Get(ctx, c.URL(f.Key, from, to))
		if err != nil {
			return fmt.Errorf("ecb record %s: %w", f.Key, err)
		}
		path := filepath.Join(dir, f.File())
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return fmt.Errorf("ecb record %s: %w", f.Key, err)
		}
		c.log.Info("fixture recorded", "file", path, "bytes", len(body))
	}
	return nil
}
