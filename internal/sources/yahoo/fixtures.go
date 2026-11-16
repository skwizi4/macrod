package yahoo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Fixture is one recorded chart response in testdata/yahoo; `macroctl
// record-fixtures --source yahoo` refreshes them.
type Fixture struct {
	Symbol   string
	From, To string // dates, pinned
	Hourly   bool   // hourly bars (SetHourly symbols); Yahoo serves them for the last 730 days only
}

// File is the fixture's file name.
func (f Fixture) File() string {
	r := strings.NewReplacer("^", "idx_", "=", "_", ".", "_")
	name := "chart_" + r.Replace(f.Symbol)
	if f.Hourly {
		name += "_1h"
	}
	return name + ".json"
}

// Fixtures cover a year of an index (MA200), a 7-day crypto, futures and
// the ZQ strip as of 2026-10-23 (the export week W43).
var Fixtures = func() []Fixture {
	fs := []Fixture{
		{Symbol: "^NDX", From: "2025-06-01", To: "2026-10-24"},
		{Symbol: "^VIX", From: "2025-09-01", To: "2026-10-24"},
		{Symbol: "BTC-USD", From: "2026-09-01", To: "2026-10-24"},
		{Symbol: "CL=F", From: "2026-09-01", To: "2026-10-24"},
		// Euronext MTS 10Y yield of Italy: one valued bar a day at 17:00 Amsterdam.
		{Symbol: "MIT10.AS", From: "2026-10-01", To: "2026-11-10", Hourly: true},
	}
	for _, c := range []string{"V26", "X26", "Z26", "F27", "G27", "H27", "J27", "K27", "M27", "N27", "Q27", "U27", "V27"} {
		fs = append(fs, Fixture{Symbol: "ZQ" + c + ".CBT", From: "2026-09-01", To: "2026-10-24"})
	}
	return fs
}()

// RecordFixtures fetches every fixture into dir.
func (c *Client) RecordFixtures(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("yahoo record mkdir %s: %w", dir, err)
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
		body, err := c.http.Get(ctx, c.chartURL(f.Symbol, from, to, f.Hourly))
		if err != nil {
			return fmt.Errorf("yahoo record %s: %w", f.Symbol, err)
		}
		path := filepath.Join(dir, f.File())
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return fmt.Errorf("yahoo record %s: %w", f.Symbol, err)
		}
		c.log.Info("fixture recorded", "file", path, "bytes", len(body))
	}
	return nil
}
