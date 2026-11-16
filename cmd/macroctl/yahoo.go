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
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
)

func newYahooClient(env *app.Env) *yahoo.Client {
	log := logging.For(env.Root, yahoo.Source)
	h := httpx.New(httpx.WithLogger(log), httpx.WithInterval(time.Second))
	c := yahoo.New(env.Config.Yahoo.Endpoint, h, log)
	c.SetHourly(env.Config.Yahoo.HourlySymbols())
	return c
}

// runYahoo loads the symbols of every region.
func runYahoo(env *app.Env, mode ingest.Mode) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	var rep ingest.YahooReport
	var took time.Duration
	for _, region := range env.Config.RegionIDs() {
		r, d := collect.RunYahoo(ctx, env.Config, st, newYahooClient(env), env.Root, mode, region)
		rep.Results, rep.Interrupted, took = append(rep.Results, r.Results...), rep.Interrupted || r.Interrupted, took+d
	}
	inserted, revised, dropped := 0, 0, 0
	for _, r := range rep.Results {
		inserted += r.Inserted
		revised += r.Revised
		dropped += r.Dropped
	}
	fmt.Printf("yahoo: %d symbols in %s — failed %d; rows inserted %d, revised %d; anomalous bars dropped %d\n",
		len(rep.Results), took.Round(time.Second), len(rep.Failed()), inserted, revised, dropped)
	for _, r := range rep.Failed() {
		fmt.Printf("  FAILED %s: %v\n", r.Symbol, r.Err)
	}
	if rep.Interrupted {
		return fmt.Errorf("interrupted; stored data is consistent, run again to continue")
	}
	if n := len(rep.Failed()); n > 0 {
		return fmt.Errorf("%d yahoo symbols failed (non-critical source: export falls back to FRED where configured)", n)
	}
	return nil
}
