package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/collect"
	"macro-collector/internal/ingest"
	"macro-collector/internal/sources/finanzagentur"
)

// runBundAuctions loads the Bund auction results (backfill: the history
// since 1999 too).
func runBundAuctions(env *app.Env, mode ingest.Mode) error {
	c := collect.NewFinanzagenturClient(env.Config, env.Root)
	if c == nil {
		fmt.Println("finanzagentur: disabled in config")
		return nil
	}
	ctx, done, st, err := openForRun(env)
	if err != nil {
		return err
	}
	defer done()
	start := time.Now()
	res := collect.RunBundAuctions(ctx, env.Config, st, c, env.Root, mode, start)
	if res.Err != nil {
		return fmt.Errorf("finanzagentur: %w", res.Err)
	}
	fmt.Printf("finanzagentur: auctions read %d, last %s — inserted %d, updated %d, unchanged %d (%s)\n",
		res.Rows, res.Last.Format(time.DateOnly), res.Inserted, res.Updated, res.Unchanged, time.Since(start).Round(time.Second))
	return nil
}

// recordBundAuctions saves the results, the history, the issuance calendar
// page and the auction plan it links.
func recordBundAuctions(env *app.Env, dir string) error {
	c := collect.NewFinanzagenturClient(env.Config, env.Root)
	if c == nil {
		return fmt.Errorf("finanzagentur is not enabled in the config")
	}
	sec := env.Config.Finanzagentur
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ctx := context.Background()
	for _, u := range []string{sec.ResultsURL, sec.HistoryURL, sec.CalendarPage} {
		data, err := c.Get(ctx, u)
		if err != nil {
			return err
		}
		name := filepath.Base(u)
		if u == sec.CalendarPage {
			name = "issuance-calendar.html"
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", filepath.Join(dir, name))
		if u == sec.CalendarPage { // the newest issuance outlook it links
			links, err := finanzagentur.OutlookLinks(data, u)
			if err != nil {
				return err
			}
			plan, err := c.Get(ctx, links[0])
			if err != nil {
				return err
			}
			path := filepath.Join(dir, filepath.Base(links[0]))
			if err := os.WriteFile(path, plan, 0o644); err != nil {
				return err
			}
			fmt.Println("wrote", path)
		}
	}
	return nil
}
