package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/export"
	"macro-collector/internal/forecast"
	"macro-collector/internal/store"
)

const forecastUsage = "forecast import [--region usa|eu|all] [--week 2026-W46] | forecast score [--region usa|eu|all] [--dry-run]   (forecast log of the masters' reports, phase 13)"

// forecastCmd: import reads the forecast blocks of the masters' reports
// into the database; score imports all of them, scores what is due and
// writes forecast_scorecard.md to the master's folder (not to the archive,
// and without delivery: the next weekly export commits it).
func forecastCmd(env *app.Env, args []string) error {
	if len(args) == 0 || (args[0] != "import" && args[0] != "score") {
		return errors.New("usage: " + forecastUsage)
	}
	sub := args[0]
	fs := flag.NewFlagSet("forecast "+sub, flag.ExitOnError)
	region := fs.String("region", "all", "region id from the config, or all")
	week := fs.String("week", "", "import: only this report week, e.g. 2026-W46")
	dry := fs.Bool("dry-run", false, "score: print the scorecard instead of writing it")
	_ = fs.Parse(args[1:])
	cfg := env.Config
	if !cfg.Forecast.Enabled {
		return errors.New("forecast.enabled is false in the config")
	}
	if *week != "" {
		if _, err := export.PeriodEndOfWeek(*week, cfg.Location); err != nil {
			return err
		}
	}
	regions, err := cfg.SelectRegions(*region)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	now := time.Now()
	log := &forecast.Log{Store: st, Config: cfg}
	for _, r := range regions {
		if r.AgentByID(cfg.Forecast.Agent) == nil {
			fmt.Printf("forecast %s: no agent %q, skipped\n", r.ID, cfg.Forecast.Agent)
			continue
		}
		if sub == "import" {
			res, err := log.Import(ctx, r.ID, *week, now)
			if err != nil {
				return fmt.Errorf("region %s: %w", r.ID, err)
			}
			fmt.Printf("forecast import %s: %d reports, %d forecasts, %d rows changed\n", r.ID, res.Weeks, res.Items, res.Changed)
			printNotes(res.Notes)
			continue
		}
		fr, err := export.SyncForecasts(ctx, st, cfg, r, now)
		if err != nil {
			return fmt.Errorf("region %s: %w", r.ID, err)
		}
		fmt.Printf("forecast score %s: %d reports, %d scored now, %d pending\n", r.ID, fr.Import.Weeks, fr.Score.Scored, fr.Score.Pending)
		printNotes(fr.Import.Notes)
		for _, o := range fr.Score.Overdue {
			fmt.Printf("  ⚠ %s: the forecast week is over, but there are no closes to score it\n", o)
		}
		if *dry {
			fmt.Printf("dry run: %s not written\n\n%s\n", fr.Path, fr.Scorecard)
			continue
		}
		written, err := export.WriteScorecard(cfg, "", fr) // the archive keeps what the export wrote
		if err != nil {
			return fmt.Errorf("region %s: %w", r.ID, err)
		}
		for _, p := range written {
			fmt.Printf("  → %s\n", p)
		}
	}
	return nil
}

func printNotes(notes []forecast.Note) {
	for _, n := range notes {
		if errors.Is(n.Err, forecast.ErrNoBlock) {
			fmt.Printf("  %s: no forecast block\n", n.Week)
		} else {
			fmt.Printf("  ⚠ %s: %v\n", n.Week, n.Err)
		}
	}
}
