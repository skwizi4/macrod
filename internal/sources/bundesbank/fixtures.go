package bundesbank

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Fixture is one recorded response in testdata/bundesbank; `macroctl
// record-fixtures --source bundesbank` refreshes them.
type Fixture struct {
	Key      string
	From, To string // dates, pinned
}

// File is the fixture's file name.
func (f Fixture) File() string {
	return strings.NewReplacer("/", "_", ".", "_").Replace(f.Key) + ".csv"
}

// Fixtures cover a daily yield with weekends and a monthly index.
var Fixtures = []Fixture{
	{"BBSIS/D.I.ZAR.ZI.EUR.S1311.B.A604.R10XX.R.A.A._Z._Z.A", "2026-10-01", "2026-11-09"},
	{"BBSIS/D.I.ZAR.ZI.EUR.S1311.B.A604.R02XX.R.A.A._Z._Z.A", "2026-10-01", "2026-11-09"},
	{"BBDE1/M.DE.Y.AEA1.A2P300000.F.C.I21.A", "2025-01-01", "2026-11-09"},
}

// RecordFixtures fetches every fixture into dir.
func (c *Client) RecordFixtures(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("bundesbank record mkdir %s: %w", dir, err)
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
			return fmt.Errorf("bundesbank record %s: %w", f.Key, err)
		}
		path := filepath.Join(dir, f.File())
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return fmt.Errorf("bundesbank record %s: %w", f.Key, err)
		}
		c.log.Info("fixture recorded", "file", path, "bytes", len(body))
	}
	return nil
}
