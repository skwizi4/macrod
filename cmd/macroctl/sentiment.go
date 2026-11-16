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
	"macro-collector/internal/sources/cboe"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
)

// Sentiment sources (FIXES_W44 batch F).

func newCboeClient(env *app.Env) *cboe.Client {
	log := logging.For(env.Root, cboe.Source)
	return cboe.New(env.Config.Cboe.Endpoint, httpx.New(httpx.WithLogger(log), httpx.WithInterval(time.Second)), log)
}

// openForRun opens the store under a context cancelled by SIGINT/SIGTERM.
func openForRun(env *app.Env) (context.Context, func(), *store.Store, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		stop()
		return nil, nil, nil, err
	}
	return ctx, func() { st.Close(); stop() }, st, nil
}

// runCboe loads the put/call ratios: the missing days, or history_days
// back on a backfill.
func runCboe(env *app.Env, mode ingest.Mode) error {
	if !env.Config.Cboe.Enabled {
		fmt.Println("cboe: disabled in config")
		return nil
	}
	ctx, done, st, err := openForRun(env)
	if err != nil {
		return err
	}
	defer done()
	start := time.Now()
	res := collect.RunCboe(ctx, env.Config, st, newCboeClient(env), env.Root, mode, start)
	if res.Err != nil {
		return fmt.Errorf("cboe: %w", res.Err)
	}
	fmt.Printf("cboe: since %s — days stored %d, last %s (%s)\n", res.Since.Format(time.DateOnly), res.Days,
		res.Last.Format(time.DateOnly), time.Since(start).Round(time.Second))
	return nil
}

// runPredict stores the prediction market prices; a backfill reloads
// history_days of history of every open market.
func runPredict(env *app.Env, mode ingest.Mode) error {
	if !env.Config.Prediction.Enabled {
		fmt.Println("prediction: disabled in config")
		return nil
	}
	ctx, done, st, err := openForRun(env)
	if err != nil {
		return err
	}
	defer done()
	start := time.Now()
	k, pm := collect.PredictClients(env.Config, env.Root)
	res := collect.RunPredict(ctx, env.Config, st, k, pm, env.Root, mode)
	fmt.Printf("prediction: markets stored %d (%s)\n", res.Markets, time.Since(start).Round(time.Second))
	if res.Err != nil {
		return fmt.Errorf("prediction: %w", res.Err)
	}
	return nil
}

// runIntraday loads the one-minute bars of the reaction events of the last
// 30 days that are not stored yet (Yahoo keeps no older ones).
func runIntraday(env *app.Env) error {
	ctx, done, st, err := openForRun(env)
	if err != nil {
		return err
	}
	defer done()
	start := time.Now()
	ylog := logging.For(env.Root, yahoo.Source)
	client := yahoo.New(env.Config.Yahoo.Endpoint, httpx.New(httpx.WithLogger(ylog), httpx.WithInterval(time.Second)), ylog)
	res := collect.RunIntraday(ctx, env.Config, st, client, env.Root, start.Add(-yahoo.MinuteHistory), start)
	fmt.Printf("intraday: events %d, bars %d (%s)\n", res.Events, res.Bars, time.Since(start).Round(time.Second))
	if res.Err != nil {
		return fmt.Errorf("intraday: %w", res.Err)
	}
	return nil
}
