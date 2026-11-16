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
	"macro-collector/internal/sources/cftc"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/store"
)

func newCFTCClient(env *app.Env) *cftc.Client {
	log := logging.For(env.Root, cftc.Source)
	return cftc.New(env.Config.CFTC.Endpoint, httpx.New(httpx.WithLogger(log)), log)
}

// runCFTC loads the contracts of every region.
func runCFTC(env *app.Env, mode ingest.Mode) error {
	if !env.Config.CFTC.Enabled {
		fmt.Println("cftc: disabled in config")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	start := time.Now()
	for _, region := range env.Config.RegionIDs() {
		if len(env.Config.CFTC.ContractsFor(region)) == 0 {
			continue
		}
		res := collect.RunCFTC(ctx, env.Config, st, newCFTCClient(env), env.Root, mode, region)
		if res.Err != nil {
			return fmt.Errorf("cftc %s: %w", region, res.Err)
		}
		fmt.Printf("cftc %s: since %s, last report %s — rows inserted %d, revised %d, unchanged %d (%s)\n", region,
			res.Since.Format(time.DateOnly), res.LastReport.Format(time.DateOnly), res.Inserted, res.Revised, res.Unchanged,
			time.Since(start).Round(time.Second))
	}
	return nil
}
