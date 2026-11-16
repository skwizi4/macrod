package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/store"
)

func newTreasuryClient(env *app.Env) *treasury.Client {
	log := logging.For(env.Root, treasury.Source)
	return treasury.New(env.Config.Treasury, httpx.New(httpx.WithLogger(log)), treasury.WithLogger(log))
}

func runTreasury(env *app.Env, mode ingest.Mode) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	sum := collect.RunTreasury(ctx, env.Config, st, newTreasuryClient(env), env.Root, mode)
	for _, r := range sum.Datasets.Results {
		if r.Err != nil {
			fmt.Printf("treasury %-20s FAILED: %v\n", r.Dataset, r.Err)
			continue
		}
		switch r.Dataset {
		case config.DatasetAuctions:
			a := r.Auctions
			fmt.Printf("treasury %-20s since %s: new %d, updated %d (results %d), unchanged %d\n",
				r.Dataset, r.Since.Format(time.DateOnly), a.Inserted, a.Updated, a.Resulted, a.Unchanged)
		case config.DatasetBuybacks:
			fmt.Printf("treasury %-20s since %s: %d new or changed\n", r.Dataset, r.Since.Format(time.DateOnly), r.Buybacks)
		default:
			fmt.Printf("treasury %-20s since %s: rows inserted %d, revised %d, unchanged %d\n",
				r.Dataset, r.Since.Format(time.DateOnly), r.Inserted, r.Revised, r.Unchanged)
		}
	}
	if r := sum.Refunding; r.Err != nil {
		fmt.Printf("treasury %-20s FAILED: %v\n", "refunding", r.Err)
	} else if r.Source != "" {
		fmt.Printf("treasury %-20s new %d, updated %d, unchanged %d\n", "refunding", len(r.Inserted), r.Updated, r.Unchanged)
		for _, d := range r.Inserted {
			fmt.Printf("  + %s %-20s %s  %s\n", d.PublishedAt.In(env.Config.Calendar.ReleaseLocation).Format("2006-01-02 15:04 ET"),
				d.DocType, d.ExternalID, d.Title)
		}
	}
	fmt.Printf("treasury: done in %s\n", sum.Duration.Round(time.Second))
	if sum.Interrupted {
		return fmt.Errorf("interrupted; stored data is consistent, run again to continue")
	}
	if n := sum.Failures(); n > 0 {
		return fmt.Errorf("%d treasury datasets failed", n)
	}
	return nil
}
