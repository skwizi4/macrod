// Command macrod is the collector daemon.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/delivery"
	"macro-collector/internal/export"
	"macro-collector/internal/logging"
	"macro-collector/internal/scheduler"
	"macro-collector/internal/sources/cboe"
	"macro-collector/internal/sources/cftc"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/fred"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"

	// Embedded zone database: the Docker image has no /usr/share/zoneinfo.
	_ "time/tzdata"
)

// defaultFetchCron is the daily safety poll of ТЗ §8.3 (06:00 in the
// config timezone); release-driven polls come from the calendar.
const defaultFetchCron = "0 6 * * *"

func main() {
	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	envPath := flag.String("env", ".env", "path to .env (missing file is fine)")
	dataDir := flag.String("data-dir", "", "data directory; overrides env "+config.EnvDataDir+" and data_dir in the config")
	fetchCron := flag.String("fetch-cron", defaultFetchCron, "cron spec of the safety fetch of all FRED series, in the config timezone")
	flag.Parse()

	if err := run(*configPath, *envPath, *dataDir, *fetchCron); err != nil {
		fmt.Fprintln(os.Stderr, "macrod:", err)
		os.Exit(1)
	}
}

func run(configPath, envPath, dataDir, fetchCron string) error {
	env, err := app.Setup("macrod", configPath, envPath, dataDir)
	if err != nil {
		return err
	}
	defer env.Close()
	log := logging.For(env.Root, "macrod")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := fred.New(os.Getenv(fred.EnvAPIKey), fred.WithLogger(logging.For(env.Root, "fred")))
	if err != nil {
		return err
	}
	if _, err := export.LoadTemplates(env.Config); err != nil {
		return err
	}
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	release, err := store.ClaimOwner(ctx, env.Config.DBPath)
	if err != nil {
		st.Close()
		return err
	}
	defer release()
	version, err := st.SchemaVersion(ctx)
	if err != nil {
		st.Close()
		return fmt.Errorf("macrod schema version: %w", err)
	}

	fedClient := fedtexts.New(httpx.WithLogger(logging.For(env.Root, "fed")))
	tlog := logging.For(env.Root, treasury.Source)
	treasuryClient := treasury.New(env.Config.Treasury, httpx.New(httpx.WithLogger(tlog)), treasury.WithLogger(tlog))
	earnings := collect.NewEarningsClient(env.Config, env.Root)
	refreshCalendarIfOld(ctx, env, st, client, fedClient, treasuryClient, earnings, log)

	ylog := logging.For(env.Root, yahoo.Source)
	yahooClient := yahoo.New(env.Config.Yahoo.Endpoint, httpx.New(httpx.WithLogger(ylog), httpx.WithInterval(time.Second)), ylog)
	yahooClient.SetHourly(env.Config.Yahoo.HourlySymbols())
	clog := logging.For(env.Root, cftc.Source)
	cftcClient := cftc.New(env.Config.CFTC.Endpoint, httpx.New(httpx.WithLogger(clog)), clog)
	cblog := logging.For(env.Root, cboe.Source)
	kalshi, polymarket := collect.PredictClients(env.Config, env.Root)
	cboeClient := cboe.New(env.Config.Cboe.Endpoint, httpx.New(httpx.WithLogger(cblog), httpx.WithInterval(time.Second)), cblog)

	posts, err := collect.NewPosts(env.Config, st, env.Root, os.Getenv(config.EnvGeminiKey))
	if err != nil {
		st.Close()
		return fmt.Errorf("macrod posts: %w", err)
	}

	sink, jobs, afterWeekly, waitDelivery := deliverySetup(ctx, env.Config, env.Root, st)
	var alert func(context.Context, string) error
	if tg := delivery.NewTelegram(env.Config, env.Root); tg != nil {
		alert = tg.Send
	}

	// One Telegram poller (phase 14c, D29): ✅/❌ of capex guidance
	// candidates (14b), Ifo values, quarterly files of issuers.
	gsvc := delivery.NewGuidance(env.Config, st, env.Root)
	botDone := make(chan struct{})
	b, botJobs := newBot(env, st, gsvc)
	jobs = append(jobs, botJobs...)
	if b != nil {
		go func() { b.Run(ctx); close(botDone) }()
	} else {
		close(botDone)
	}

	sched := scheduler.New(scheduler.Deps{
		Config: env.Config, Store: st, Guidance: gsvc, FRED: client, Fed: fedClient, ECB: collect.NewECBClient(env.Config, env.Root), Treasury: treasuryClient, Earnings: earnings,
		EDGAR: collect.NewEDGARClient(env.Config, env.Root), Transcripts: collect.NewTranscriptClient(env.Config, env.Root),
		Yahoo: yahooClient, CFTC: cftcClient, Cboe: cboeClient, Kalshi: kalshi, Polymarket: polymarket, Intraday: yahooClient, Posts: posts, Root: env.Root, FetchCron: fetchCron,
		Sink: sink, Jobs: jobs, Alert: alert, AfterWeekly: afterWeekly,
		Numeric: collect.NumericRunners(env.Config, st, env.Root),
		Bund:    collect.NewFinanzagenturClient(env.Config, env.Root),
		Issuers: collect.NewIssuerClients(env.Config, env.Root),
	})
	log.Info("started", "data_dir", env.Config.DataDir, "db_path", env.Config.DBPath,
		"schema_version", version, "fred_series", len(env.Config.FRED.Series), "post_sources", len(posts.Sources),
		"gemini", posts.Classify.Gemini != nil, "delivery", sink != nil,
		"timezone", env.Config.Location.String())
	runErr := sched.Run(ctx) // until SIGINT/SIGTERM; waits for running jobs and pollers
	log.Info("shutting down")
	waitDelivery() // deliveries stop with ctx; a pending push goes out after the next start
	<-botDone      // the long poll ends with ctx

	// Let the store finish queued writes.
	if err := st.Close(); err != nil {
		return fmt.Errorf("macrod close store: %w", err)
	}
	if runErr != nil {
		return fmt.Errorf("macrod scheduler: %w", runErr)
	}
	log.Info("stopped")
	return nil
}

// calendarMaxAge: an older calendar is refreshed at start, so a freshly
// deployed or long-stopped daemon does not wait for the 05:00 job.
const calendarMaxAge = 24 * time.Hour

func refreshCalendarIfOld(ctx context.Context, env *app.Env, st *store.Store, fredClient *fred.Client,
	fedClient *fedtexts.Client, treasuryClient *treasury.Client, earnings collect.EarningsClient, log *slog.Logger) {
	fs, ok, err := st.FetchStateOf(ctx, "calendar:fred")
	if err == nil && ok && fs.Cursor != "" && time.Since(fs.UpdatedAt) < calendarMaxAge {
		return
	}
	log.Info("calendar is missing or older than a day, refreshing at start")
	sum := collect.RefreshCalendars(ctx, env.Config, st, fredClient, fedClient, treasuryClient, earnings, env.Root)
	if err := sum.Err(); err != nil {
		log.Error("calendar refresh at start failed", "err", err)
		return
	}
	log.Info("calendar refreshed", "fred_events", sum.FRED.Events, "fed_events", sum.Fed.Events,
		"treasury_events", sum.Treasury.Events, "earnings_events", sum.Earnings.Events, "ecb_events", sum.ECB.Events, "release_events", sum.Release.Events())
}
