package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/collect"
	"macro-collector/internal/derived"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/scheduler"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/fred"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/store"
)

func backfill(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	source := fs.String("source", "", "source to backfill: fred | treasury | yahoo | cftc | cboe | prediction | telegram | rss | edgar | finanzagentur | ecb | bundesbank | eurostat | stoxx | zew")
	sinceStr := fs.String("since", "", "start date YYYY-MM-DD (default: history_start)")
	_ = fs.Parse(args)
	if isNumericSource(env.Config, *source) {
		if *sinceStr != "" {
			return fmt.Errorf("--since is not supported for %s (history_start is used)", *source)
		}
		return runNumeric(env, ingest.Backfill, false, *source)
	}
	switch *source {
	case "fred":
	case "treasury":
		if *sinceStr != "" {
			return fmt.Errorf("--since is not supported for treasury (history_start is used)")
		}
		return runTreasury(env, ingest.Backfill)
	case "yahoo":
		if *sinceStr != "" {
			return fmt.Errorf("--since is not supported for yahoo (history_start is used)")
		}
		return runYahoo(env, ingest.Backfill)
	case "cftc":
		if *sinceStr != "" {
			return fmt.Errorf("--since is not supported for cftc (history_start is used)")
		}
		return runCFTC(env, ingest.Backfill)
	case "cboe":
		if *sinceStr != "" {
			return fmt.Errorf("--since is not supported for cboe (cboe.history_days is used)")
		}
		return runCboe(env, ingest.Backfill)
	case "prediction":
		if *sinceStr != "" {
			return fmt.Errorf("--since is not supported for prediction (prediction_markets.history_days is used)")
		}
		return runPredict(env, ingest.Backfill)
	case "telegram", "rss":
		if *sinceStr != "" {
			return fmt.Errorf("--since is not supported for %s (posts.history_days and history_days of a source are used)", *source)
		}
		return runPosts(env, *source, true)
	case "edgar":
		if *sinceStr != "" {
			return fmt.Errorf("--since is not supported for edgar (edgar.history_start is used)")
		}
		return runEDGAR(env, ingest.Backfill)
	case "finanzagentur":
		if *sinceStr != "" {
			return fmt.Errorf("--since is not supported for finanzagentur (the history file starts in 1999)")
		}
		return runBundAuctions(env, ingest.Backfill)
	default:
		return fmt.Errorf("--source must be fred, treasury, yahoo, cftc, cboe, prediction, telegram, rss, edgar, finanzagentur or %s, got %q",
			strings.Join(numericSourceNames(env.Config), ", "), *source)
	}
	var since time.Time
	if *sinceStr != "" {
		var err error
		if since, err = time.Parse(time.DateOnly, *sinceStr); err != nil {
			return fmt.Errorf("--since: %w", err)
		}
	}
	return runFRED(env, ingest.Backfill, since)
}

func fetch(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	source := fs.String("source", "", "source to fetch: fred | fed | ecb_texts | treasury | yahoo | cftc | cboe | prediction | intraday | telegram | rss | posts | views | edgar | finanzagentur | issuers | ecb | bundesbank | eurostat | stoxx | zew | all")
	days := fs.Int("days", scheduler.ViewsDays, "views: re-read views and photo flags of posts of the last N days")
	_ = fs.Parse(args)
	if *days != scheduler.ViewsDays && *source != "views" {
		return fmt.Errorf("--days is only for --source views")
	}
	if *days <= 0 {
		return fmt.Errorf("--days must be positive")
	}
	if isNumericSource(env.Config, *source) {
		return runNumeric(env, ingest.Incremental, false, *source)
	}
	switch *source {
	case "fred":
		return runFRED(env, ingest.Incremental, time.Time{})
	case "fed":
		return runFed(env)
	case "ecb_texts":
		return runECBTexts(env)
	case "treasury":
		return runTreasury(env, ingest.Incremental)
	case "yahoo":
		return runYahoo(env, ingest.Incremental)
	case "cftc":
		return runCFTC(env, ingest.Incremental)
	case "cboe":
		return runCboe(env, ingest.Incremental)
	case "prediction":
		return runPredict(env, ingest.Incremental)
	case "intraday":
		return runIntraday(env)
	case "telegram", "rss", "posts":
		return runPosts(env, *source, false)
	case "views":
		return runViews(env, *days)
	case "edgar":
		return runEDGAR(env, ingest.Incremental)
	case "finanzagentur":
		return runBundAuctions(env, ingest.Incremental)
	case "issuers":
		return runIssuers(env)
	case "all":
		var ecbErr, faErr, isErr error
		if env.Config.Issuers.Enabled {
			isErr = runIssuers(env)
		}
		if env.Config.ECBTexts.Enabled {
			ecbErr = runECBTexts(env)
		}
		if env.Config.Finanzagentur.Enabled {
			faErr = runBundAuctions(env, ingest.Incremental)
		}
		return errors.Join(runFRED(env, ingest.Incremental, time.Time{}), runFed(env), ecbErr, faErr, isErr,
			runTreasury(env, ingest.Incremental), runYahoo(env, ingest.Incremental), runCFTC(env, ingest.Incremental),
			runCboe(env, ingest.Incremental), runPredict(env, ingest.Incremental), runPosts(env, "posts", false), runEDGAR(env, ingest.Incremental),
			runNumeric(env, ingest.Incremental, true, numericSourceNames(env.Config)...))
	default:
		return fmt.Errorf("--source must be fred, fed, ecb_texts, treasury, yahoo, cftc, cboe, prediction, intraday, telegram, rss, posts, views, edgar, finanzagentur, issuers, %s or all, got %q",
			strings.Join(numericSourceNames(env.Config), ", "), *source)
	}
}

// runFed polls federalreserve.gov texts (RSS feeds and the Beige Book).
func runFed(env *app.Env) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	client := fedtexts.New(httpx.WithLogger(logging.For(env.Root, "fed")))
	return printTexts(ctx, env, st, "fed", collect.FedTextSources(env.Config, client, st, env.Root))
}

// runECBTexts polls ecb.europa.eu texts (RSS, yearly lists, Q&A of recent
// statements) and checks recent decisions against the DFR series.
func runECBTexts(env *app.Env) error {
	if !env.Config.ECBTexts.Enabled {
		return fmt.Errorf("ecb_texts is not enabled in the config")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	srcs := collect.NewECBSources(env.Config, collect.NewECBClient(env.Config, env.Root), st, env.Root)
	err = printTexts(ctx, env, st, "ecb_texts", srcs.All())
	collect.CheckECBDecisions(ctx, env.Config, st, time.Now(), logging.For(env.Root, ecbtexts.Source))
	return err
}

// printTexts runs text sources and prints what each stored.
func printTexts(ctx context.Context, env *app.Env, st *store.Store, label string, srcs []sources.TextSource) error {
	failed := 0
	for _, r := range collect.RunFedTexts(ctx, collect.NewTexts(env.Config, st, env.Root), srcs) {
		if r.Err != nil {
			failed++
			fmt.Printf("%-20s FAILED: %v\n", r.Source, r.Err)
			continue
		}
		fmt.Printf("%-20s new %d, updated %d, unchanged %d\n", r.Source, len(r.Inserted), r.Updated, r.Unchanged)
		for _, d := range r.Inserted {
			fmt.Printf("  + %s %-15s %s  %s\n", d.PublishedAt.In(env.Config.Location).Format("2006-01-02 15:04"), d.DocType, d.ExternalID, d.Title)
		}
	}
	if ctx.Err() != nil {
		return fmt.Errorf("interrupted; run again to continue")
	}
	if failed > 0 {
		return fmt.Errorf("%d %s sources failed", failed, label)
	}
	return nil
}

func runFRED(env *app.Env, mode ingest.Mode, since time.Time) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := fred.New(os.Getenv(fred.EnvAPIKey), fred.WithLogger(logging.For(env.Root, "fred")))
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	sum := collect.RunFRED(ctx, env.Config, st, client, env.Root, mode, since)
	printSummary(sum)
	if sum.Interrupted {
		return fmt.Errorf("interrupted; stored data is consistent, run again to continue")
	}
	if n := sum.Failures(); n > 0 {
		return fmt.Errorf("%d series failed", n)
	}
	return nil
}

func printSummary(sum collect.Summary) {
	rep := sum.FRED
	inserted, revised := sum.Rows()
	fmt.Printf("fred: %d series in %s — updated %d, skipped %d, failed %d; rows inserted %d, revised %d\n",
		len(rep.Results), sum.Duration.Round(time.Second),
		rep.Count(ingest.OutcomeUpdated), rep.Count(ingest.OutcomeSkipped), rep.Count(ingest.OutcomeFailed),
		inserted, revised)
	for _, r := range rep.Failed() {
		fmt.Printf("  FAILED %s: %v\n", r.ID, r.Err)
	}
	printDerived(sum)
}

// printDerived prints the derived series recomputed by a pass.
func printDerived(sum collect.Summary) {
	if len(sum.Derived) == 0 {
		return
	}
	var dUpdated, dFailed int
	var dLines []string
	for _, r := range sum.Derived {
		switch r.Outcome {
		case derived.OutcomeUpdated:
			dUpdated++
			dLines = append(dLines, fmt.Sprintf("  %-14s %6d rows, +%d new, %d revised", r.Name, r.Rows, r.Inserted, r.Revised))
		case derived.OutcomeFailed:
			dFailed++
			dLines = append(dLines, fmt.Sprintf("  FAILED %s: %v", r.Name, r.Err))
		}
	}
	fmt.Printf("derived: updated %d, failed %d (others in config are for later phases)\n", dUpdated, dFailed)
	for _, l := range dLines {
		fmt.Println(l)
	}
}
