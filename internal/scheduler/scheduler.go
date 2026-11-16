package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"macro-collector/internal/calendar"
	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/export"
	"macro-collector/internal/guidance"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/sources/edgar"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/finanzagentur"
	"macro-collector/internal/sources/nasdaq"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
)

// Deps are what the scheduler runs on.
type Deps struct {
	Config      *config.Config
	Store       *store.Store
	Guidance    *guidance.Service // nil: no capex guidance candidates (phase 14b)
	FRED        FREDClient
	Fed         *fedtexts.Client
	ECB         *ecbtexts.Client         // nil: no ECB texts, meetings or decision day (ecb_texts disabled)
	Treasury    *treasury.Client         // nil: no Treasury calendar, events or jobs
	Earnings    collect.EarningsClient   // nil: no earnings calendar
	EDGAR       collect.EDGARClient      // nil: no earnings polling or EDGAR job
	Transcripts collect.TranscriptClient // nil: no call transcripts
	Yahoo       ingest.YahooClient       // nil: no Yahoo job
	CFTC        ingest.CFTCClient        // nil: no CFTC job
	Cboe        ingest.CboeClient        // nil: no put/call job
	Kalshi      ingest.KalshiClient      // nil with Polymarket: no prediction markets job
	Polymarket  ingest.PolymarketClient  // see Kalshi
	Intraday    ingest.IntradayClient    // nil: no event reaction bars
	Posts       *collect.Posts           // nil: no Telegram/RSS job
	Numeric     []collect.NumericRunner  // numeric sources of the config sections (ecb, bundesbank, …): a job each
	Bund        *finanzagentur.Client    // nil: no Bund auctions job, calendar or events
	Issuers     *collect.IssuerClients   // nil: no EU Micro issuers job
	Root        *slog.Logger
	FetchCron   string           // safety fetch of all FRED series (§8.3: 06:00)
	Clock       Clock            // nil: real time (tests: virtual)
	Sink        export.EventSink // nil: log only
	// Alert sends an urgent message (ECB decision day: nothing found in
	// time); nil: logged only.
	Alert func(ctx context.Context, text string) error
	Jobs  []Job // extra periodic jobs (phase 6: git push pending, source alerts, backup)
	// AfterWeekly runs once the weekly export of every region is done (the
	// public demo site); it must not block. nil: nothing.
	AfterWeekly func(isoWeek string)
}

// FREDClient is the FRED client the scheduler needs: series data for
// ingest and release dates for the calendar.
type FREDClient interface {
	ingest.FREDClient
	calendar.FREDClient
}

// DailyFallbackCron is §8.3: daily FRED series without a calendar event
// that day are fetched at 23:00.
const DailyFallbackCron = "0 23 * * *"

// TreasuryFetchCron is the daily safety fetch of every Treasury dataset and
// the refunding documents, after the FRED one (§8.3 06:00).
const TreasuryFetchCron = "15 6 * * *"

// RecheckCron re-decides the timed-out events of the last week hourly.
const RecheckCron = "@hourly"

// Job is a periodic task.
type Job struct {
	Name string
	Spec string // cron spec in the config timezone
	Run  func(ctx context.Context)
}

// Scheduler is the daemon's work: event pollers plus periodic jobs.
type Scheduler struct {
	Events *Events
	Jobs   []Job
	log    *slog.Logger
	loc    *time.Location
}

// New wires routes and jobs from the config.
func New(d Deps) *Scheduler {
	cfg := d.Config
	log := logging.For(d.Root, "scheduler")
	texts := collect.NewTexts(cfg, d.Store, d.Root)
	fedSrcs := collect.FedTextSources(cfg, d.Fed, d.Store, d.Root)
	ecbSrcs := collect.NewECBSources(cfg, d.ECB, d.Store, d.Root)
	var press, beige, meetingDocs sources.TextSource
	for _, s := range fedSrcs {
		switch s.Name() {
		case (&fedtexts.MeetingDocs{}).Name():
			meetingDocs = s
		case fedtexts.Source + ":" + fedtexts.FeedPressMonetary:
			press = s
		case fedtexts.Source + ":" + fedtexts.DocBeigeBook:
			beige = s
		}
	}
	et := cfg.Calendar.ReleaseLocation
	fedDoc := func(src sources.TextSource, docType string) Handler {
		return &FedDocument{Texts: texts, Source: src, Store: d.Store, DocType: docType, Location: et}
	}
	routes := []Route{
		{Source: calendar.SourceFRED, Handler: &FREDRelease{Config: cfg, Store: d.Store, Client: d.FRED, Root: d.Root}},
		{Source: fedtexts.SourceBeigeBook, Name: fedtexts.NameBeigeBook, Handler: fedDoc(beige, fedtexts.DocBeigeBook)},
	}
	sink := d.Sink
	if sink == nil {
		sink = export.LogSink{Log: logging.For(d.Root, "export")}
	}
	exp := &export.Events{Store: d.Store, Config: cfg, ExchangeDir: cfg.Export.ExchangeDir, Location: cfg.Location,
		ReleaseLoc: et, Sink: sink}
	if d.Clock != nil {
		exp.Now = d.Clock.Now
	}
	if press != nil {
		fomc := &FOMCDay{Client: d.Fed, BaseURL: cfg.FedTexts.BaseURL, Texts: texts, Press: press, MeetingDocs: meetingDocs, Store: d.Store,
			Export: exp, Location: et, Clock: d.Clock, Log: logging.For(d.Root, "fomc")}
		routes = append(routes,
			Route{Source: fedtexts.SourceFOMC, Name: fedtexts.NameDecision, Handler: fomc, Plan: FOMCPlan{}},
			Route{Source: fedtexts.SourceFOMC, Name: fedtexts.NameMinutes, Handler: fedDoc(press, fedtexts.DocMinutes)},
		)
	} else {
		log.Warn("no press_monetary feed in config: FOMC events are not polled")
	}
	if ecbSrcs != nil {
		ecbDay := &ECBDay{Texts: texts, Feed: ecbSrcs.Feed, Decisions: ecbSrcs.Decisions, Statements: ecbSrcs.Statements,
			Store: d.Store, Export: exp, Config: &cfg.ECBTexts, Alert: d.Alert, Clock: d.Clock, Log: logging.For(d.Root, "ecb_day")}
		routes = append(routes,
			Route{Source: ecbtexts.SourceMeeting, Name: ecbtexts.NameDecision, Handler: ecbDay, Plan: ECBPlan{Mode: cfg.ECBTexts.DayMode}},
			Route{Source: ecbtexts.SourceMeeting, Name: ecbtexts.NamePressConference,
				Handler: &ECBStatement{Texts: texts, Feed: ecbSrcs.Feed, Store: d.Store, Location: cfg.ECBTexts.Location}},
		)
	}
	var treasuryRunner *TreasuryRunner
	var refunding sources.TextSource
	if d.Treasury != nil {
		treasuryRunner = &TreasuryRunner{Ingest: &ingest.Treasury{Client: d.Treasury, Store: d.Store, Config: &cfg.Treasury,
			History: cfg.History, Log: logging.For(d.Root, treasury.Source)}}
		if d.Clock != nil {
			treasuryRunner.Ingest.Now = d.Clock.Now
		}
		refunding = &treasury.Refunding{Client: d.Treasury, ET: et}
		routes = append(routes, treasuryRoutes(cfg, d.Store, treasuryRunner, texts, refunding)...)
	}

	var edgarRun *collect.EDGAR
	if d.EDGAR != nil && cfg.EDGAR.Enabled {
		edgarRun = &collect.EDGAR{Client: d.EDGAR, Transcripts: d.Transcripts, Store: d.Store, Config: cfg, Log: logging.For(d.Root, edgar.Source),
			Guidance: d.Guidance}
		if d.Clock != nil {
			edgarRun.Now = d.Clock.Now
		}
		routes = append(routes, Route{Source: nasdaq.Source, Handler: &Earnings{EDGAR: edgarRun}})
	}

	if len(cfg.ReleaseCalendars) > 0 {
		series := &SeriesRelease{Config: cfg, Store: d.Store, Numeric: d.Numeric, Root: d.Root}
		for _, c := range cfg.ReleaseCalendars {
			routes = append(routes, Route{Source: c.Source(), Handler: series})
		}
	}
	if d.Bund != nil && cfg.Finanzagentur.Enabled {
		bund := &BundAuction{Config: cfg, Store: d.Store, Client: d.Bund, Root: d.Root}
		if d.Clock != nil {
			bund.Now = d.Clock.Now
		}
		routes = append(routes, Route{Source: finanzagentur.SourceAuction, Handler: bund})
	}

	jobLog := logging.For(d.Root, "job")
	s := &Scheduler{
		Events: &Events{Store: d.Store, Routes: routes, Log: log, Clock: d.Clock},
		log:    log,
		loc:    cfg.Location,
	}
	s.Jobs = []Job{
		{Name: "calendar refresh", Spec: cfg.Calendar.RefreshCron, Run: func(ctx context.Context) {
			sum := collect.RefreshCalendars(ctx, cfg, d.Store, d.FRED, d.Fed, d.Treasury, d.Earnings, d.Root)
			logResult(jobLog, "calendar refresh", sum.Err(), "fred_events", sum.FRED.Events, "fed_events", sum.Fed.Events,
				"treasury_events", sum.Treasury.Events, "earnings_events", sum.Earnings.Events, "ecb_events", sum.ECB.Events, "release_events", sum.Release.Events(),
				"rescheduled", sum.FRED.Rescheduled+sum.Fed.Rescheduled+sum.Treasury.Rescheduled+sum.Earnings.Rescheduled)
		}},
		{Name: "fred safety fetch", Spec: d.FetchCron, Run: func(ctx context.Context) {
			sum := collect.RunFRED(ctx, cfg, d.Store, d.FRED, d.Root, ingest.Incremental, time.Time{})
			logFRED(jobLog, "fred safety fetch", sum)
		}},
		{Name: "fred daily fallback", Spec: DailyFallbackCron, Run: func(ctx context.Context) {
			ids, err := uncoveredDaily(ctx, cfg, d.Store, time.Now())
			if err != nil || len(ids) == 0 {
				logResult(jobLog, "fred daily fallback", err, "series", 0)
				return
			}
			logFRED(jobLog, "fred daily fallback", collect.RunFREDSeries(ctx, cfg, d.Store, d.FRED, d.Root, ids))
		}},
		{Name: "fed texts", Spec: "@every " + cfg.FedTexts.PollInterval.String(), Run: func(ctx context.Context) {
			var failed, inserted int
			for _, r := range collect.RunFedTexts(ctx, texts, fedSrcs) {
				inserted += len(r.Inserted)
				if r.Err != nil {
					failed++
				}
			}
			var err error
			if failed > 0 {
				err = fmt.Errorf("%d sources failed", failed)
			}
			logResult(jobLog, "fed texts", err, "new_documents", inserted)
		}},
	}
	if ecbSrcs != nil {
		ecbLog := logging.For(d.Root, ecbtexts.Source)
		s.Jobs = append(s.Jobs, Job{Name: "ecb texts", Spec: "@every " + cfg.ECBTexts.PollInterval.String(), Run: func(ctx context.Context) {
			var failed, inserted int
			for _, r := range collect.RunFedTexts(ctx, texts, ecbSrcs.All()) {
				inserted += len(r.Inserted)
				if r.Err != nil {
					failed++
				}
			}
			var err error
			if failed > 0 {
				err = fmt.Errorf("%d sources failed", failed)
			}
			logResult(jobLog, "ecb texts", err, "new_documents", inserted)
			collect.CheckECBDecisions(ctx, cfg, d.Store, time.Now(), ecbLog)
		}})
	}
	// Data of a timed-out event may come later from the safety jobs.
	s.Jobs = append(s.Jobs, Job{Name: "timeout recheck", Spec: RecheckCron, Run: func(ctx context.Context) {
		s.Events.Recheck(ctx, recheckWindow)
	}})
	s.Jobs = append(s.Jobs, Job{Name: "weekly export", Spec: cfg.Export.WeeklyCron, Run: func(ctx context.Context) {
		// Fired at the period end (Sunday 16:00): the period that just closed.
		now := time.Now()
		if d.Clock != nil {
			now = d.Clock.Now()
		}
		end := export.LastPeriodEnd(now, cfg.Location)
		s.Events.Recheck(ctx, recheckWindow) // no false timeouts in section 5
		iso := ""
		for i := range cfg.Regions {
			weekly := &export.Weekly{Store: d.Store, Config: cfg, Region: &cfg.Regions[i], Sink: sink, Log: logging.For(d.Root, "export")}
			if d.Clock != nil {
				weekly.Now = d.Clock.Now
			}
			res, err := weekly.Run(ctx, end, false)
			logResult(jobLog, "weekly export", err, "region", cfg.Regions[i].ID, "iso_week", res.ISOWeek, "files", len(res.Files), "previous_reports", len(res.Previous), "prompts", len(res.Prompts), "no_prompt", res.NoPrompt, "forecast_err", res.ForecastErr)
			if res.ISOWeek != "" {
				iso = res.ISOWeek
			}
		}
		if d.AfterWeekly != nil && iso != "" && ctx.Err() == nil {
			d.AfterWeekly(iso)
		}
	}})
	if treasuryRunner != nil {
		s.Jobs = append(s.Jobs, Job{Name: "treasury safety fetch", Spec: TreasuryFetchCron, Run: func(ctx context.Context) {
			rep := treasuryRunner.All(ctx)
			docs := texts.Run(ctx, refunding)
			var errs []error
			for _, f := range rep.Failed() {
				errs = append(errs, fmt.Errorf("%s: %w", f.Dataset, f.Err))
			}
			if docs.Err != nil {
				errs = append(errs, fmt.Errorf("refunding: %w", docs.Err))
			}
			logResult(jobLog, "treasury safety fetch", errors.Join(errs...), "datasets", len(rep.Results),
				"new_documents", len(docs.Inserted))
		}})
	}
	if edgarRun != nil {
		s.Jobs = append(s.Jobs, Job{Name: "edgar safety fetch", Spec: EDGARFetchCron, Run: func(ctx context.Context) {
			rep := edgarRun.Run(ctx, ingest.Incremental)
			var err error
			if n := len(rep.Failed()); n > 0 {
				err = fmt.Errorf("%d companies failed, first %s: %w", n, rep.Failed()[0].Ticker, rep.Failed()[0].Err)
			}
			newReports, facts := 0, 0
			for _, r := range rep.Results {
				newReports += r.NewReports
				facts += r.Facts
			}
			logResult(jobLog, "edgar safety fetch", err, "companies", len(rep.Results), "new_reports", newReports, "facts_changed", facts)
		}})
	}
	if d.Yahoo != nil {
		weeklySched, _ := cron.ParseStandard(cfg.Export.WeeklyCron) // checked by config.Load
		for i := range cfg.Regions {
			region := &cfg.Regions[i]
			keeper := &YahooKeeper{
				Poll: func(ctx context.Context, only []string) ingest.YahooReport {
					rep, _ := collect.RunYahoo(ctx, cfg, d.Store, d.Yahoo, d.Root, ingest.Incremental, region.ID, only...)
					return rep
				},
				Symbols: func(now time.Time) []string {
					var out []string
					for _, y := range collect.YahooSymbols(cfg, region.ID, now) {
						out = append(out, y.Symbol)
					}
					return out
				},
				LastDate: func(ctx context.Context, symbol string) (time.Time, bool, error) {
					return d.Store.LastObsDate(ctx, yahoo.Source, symbol)
				},
				Weekly: weeklySched, Location: cfg.Location, Exchange: et, Clock: d.Clock, Log: jobLog,
				ExchangeOf: func(symbol string) *time.Location { return cfg.Yahoo.ExchangeOf(symbol, et) },
			}
			name := "yahoo " + region.ID
			retry := "yahoo retry and top-up"
			if len(cfg.Regions) > 1 {
				retry += " " + region.ID
			}
			s.Jobs = append(s.Jobs, Job{Name: name, Spec: fmt.Sprintf("%d %d * * *", region.YahooPoll.Minute, region.YahooPoll.Hour), Run: func(ctx context.Context) {
				// Daily, weekends included: futures sessions end at 23:59 ET in
				// Yahoo, so Friday closes arrive with the Saturday run.
				logResult(jobLog, name, keeper.Daily(ctx))
			}}, Job{Name: retry, Spec: "@every " + YahooRetryEvery.String(), Run: keeper.Tick})
		}
	}
	if d.Posts != nil && cfg.News.ViewsRefresh && cfg.Telegram.Enabled {
		weeklySched, _ := cron.ParseStandard(cfg.Export.WeeklyCron) // checked by config.Load
		views := &ViewsKeeper{Refresh: d.Posts.RefreshViews, Weekly: weeklySched, Location: cfg.Location, Clock: d.Clock, Log: jobLog}
		s.Jobs = append(s.Jobs, Job{Name: "telegram views", Spec: "@every " + YahooRetryEvery.String(), Run: views.Tick})
	}
	if d.Posts != nil {
		s.Jobs = append(s.Jobs, Job{Name: "posts", Spec: "@every " + cfg.Posts.PollInterval.String(), Run: func(ctx context.Context) {
			sum := d.Posts.Run(ctx)
			c := sum.Classify
			logResult(jobLog, "posts", sum.Error(), "sources", len(sum.Fetch), "new_posts", sum.New(),
				"classified", c.Posts, "gemini", c.Gemini, "keywords", c.Keywords, "gemini_requests", c.Requests,
				"duplicates_changed", sum.Dedup.Changed)
			if c.GeminiErr != nil {
				jobLog.Warn("posts: gemini fell back to keywords", "err", c.GeminiErr)
			}
		}})
	}
	if d.CFTC != nil && cfg.CFTC.Enabled {
		s.Jobs = append(s.Jobs, Job{Name: "cftc", Spec: cfg.CFTC.CheckCron, Run: func(ctx context.Context) {
			// Weekdays: the Friday report moves to Monday after holidays.
			for _, id := range cfg.RegionIDs() {
				if len(cfg.CFTC.ContractsFor(id)) == 0 {
					continue
				}
				res := collect.RunCFTC(ctx, cfg, d.Store, d.CFTC, d.Root, ingest.Incremental, id)
				logResult(jobLog, "cftc", res.Err, "region", id, "last_report", res.LastReport.Format(time.DateOnly), "inserted", res.Inserted)
			}
		}})
	}
	if d.Cboe != nil && cfg.Cboe.Enabled {
		s.Jobs = append(s.Jobs, Job{Name: "cboe", Spec: cfg.Cboe.PollCron, Run: func(ctx context.Context) {
			res := collect.RunCboe(ctx, cfg, d.Store, d.Cboe, d.Root, ingest.Incremental, time.Now())
			logResult(jobLog, "cboe", res.Err, "days", res.Days, "last", res.Last.Format(time.DateOnly))
		}})
	}
	if (d.Kalshi != nil || d.Polymarket != nil) && cfg.Prediction.Enabled {
		s.Jobs = append(s.Jobs, Job{Name: "prediction", Spec: cfg.Prediction.PollCron, Run: func(ctx context.Context) {
			res := collect.RunPredict(ctx, cfg, d.Store, d.Kalshi, d.Polymarket, d.Root, ingest.Incremental)
			logResult(jobLog, "prediction", res.Err, "markets", res.Markets)
		}})
	}
	if every := reactionEvery(cfg); d.Intraday != nil && every > 0 {
		s.Jobs = append(s.Jobs, Job{Name: "intraday", Spec: "@every " + every.String(), Run: func(ctx context.Context) {
			// Events of the last 30 days whose bars are due and not stored.
			now := time.Now()
			res := collect.RunIntraday(ctx, cfg, d.Store, d.Intraday, d.Root, now.Add(-yahoo.MinuteHistory), now)
			if res.Events > 0 || res.Err != nil {
				logResult(jobLog, "intraday", res.Err, "events", res.Events, "bars", res.Bars)
			}
		}})
	}
	if d.Bund != nil && cfg.Finanzagentur.Enabled {
		s.Jobs = append(s.Jobs, Job{Name: finanzagentur.Source, Spec: cfg.Finanzagentur.PollCron, Run: func(ctx context.Context) {
			res := collect.RunBundAuctions(ctx, cfg, d.Store, d.Bund, d.Root, ingest.Incremental, time.Now())
			logResult(jobLog, finanzagentur.Source, res.Err, "last", res.Last.Format(time.DateOnly), "inserted", res.Inserted, "updated", res.Updated)
		}})
	}
	if d.Issuers != nil && cfg.Issuers.Enabled {
		s.Jobs = append(s.Jobs, Job{Name: collect.IssuersSource, Spec: cfg.Issuers.PollCron, Run: func(ctx context.Context) {
			for _, r := range collect.RunIssuers(ctx, cfg, d.Store, d.Issuers, d.Root, time.Now()) {
				if r.Err != nil || r.Facts > 0 || r.Releases > 0 || r.Dates > 0 {
					logResult(jobLog, collect.IssuersSource, r.Err, "ticker", r.Ticker, "facts", r.Facts, "releases", r.Releases, "dates", r.Dates)
				}
			}
		}})
	}
	for _, r := range d.Numeric {
		name := r.Name()
		s.Jobs = append(s.Jobs, Job{Name: name, Spec: r.PollCron, Run: func(ctx context.Context) {
			// The daily safety poll (D8): releases without a calendar event
			// are caught here.
			logFRED(jobLog, name, collect.RunNumeric(ctx, cfg, d.Store, r, d.Root, ingest.Incremental))
		}})
	}
	s.Jobs = append(s.Jobs, d.Jobs...)
	return s
}

// Run starts the jobs and the event pollers and blocks until ctx is done;
// then it waits for running jobs and pollers.
func (s *Scheduler) Run(ctx context.Context) error {
	cl := cronLogger{s.log}
	c := cron.New(cron.WithLocation(s.loc), cron.WithLogger(cl),
		// A slow run (retries) must not overlap the next one; a panic must
		// not take the daemon down.
		cron.WithChain(cron.Recover(cl), cron.SkipIfStillRunning(cl)))
	ids := make([]cron.EntryID, len(s.Jobs))
	for i, j := range s.Jobs {
		id, err := c.AddFunc(j.Spec, func() { j.Run(ctx) })
		if err != nil {
			return fmt.Errorf("scheduler job %q spec %q: %w", j.Name, j.Spec, err)
		}
		ids[i] = id
	}
	c.Start()
	for i, j := range s.Jobs {
		// Entries() is sorted by next run, so look each job up by its id.
		s.log.Info("job scheduled", "job", j.Name, "spec", j.Spec, "next", c.Entry(ids[i]).Next.Format(time.RFC3339))
	}
	s.Events.Run(ctx) // returns once ctx is done and pollers have stopped
	<-c.Stop().Done()
	return nil
}

// uncoveredDaily lists configured daily FRED series whose release has no
// event on today's date (release timezone): those are fetched by the
// 23:00 fallback instead of a calendar poller.
func uncoveredDaily(ctx context.Context, cfg *config.Config, st *store.Store, now time.Time) ([]string, error) {
	loc := cfg.Calendar.ReleaseLocation
	y, m, d := now.In(loc).Date()
	from := time.Date(y, m, d, 0, 0, 0, 0, loc)
	evs, err := st.Releases(ctx, from, from.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	covered := make(map[string]bool)
	for _, e := range evs {
		if e.Source == calendar.SourceFRED && e.Status != model.StatusRescheduled {
			for _, id := range e.Related.Series {
				covered[id] = true
			}
		}
	}
	freq, err := st.SeriesFrequencies(ctx, "fred")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range cfg.FRED.Series {
		if freq[s.ID] == "d" && !covered[s.ID] {
			out = append(out, s.ID)
		}
	}
	return out, nil
}

func logResult(l *slog.Logger, job string, err error, attrs ...any) {
	if err != nil {
		l.Error(job+" failed", append(attrs, "err", err)...)
		return
	}
	l.Info(job+" finished", attrs...)
}

func logFRED(l *slog.Logger, job string, sum collect.Summary) {
	inserted, revised := sum.Rows()
	attrs := []any{"duration", sum.Duration.Round(time.Second),
		"updated", sum.FRED.Count(ingest.OutcomeUpdated), "skipped", sum.FRED.Count(ingest.OutcomeSkipped),
		"inserted", inserted, "revised", revised, "failed", sum.Failures()}
	switch {
	case sum.Interrupted:
		l.Warn(job+" interrupted by shutdown", attrs...)
	case sum.Failures() > 0:
		l.Error(job+" finished with failures", attrs...)
	default:
		l.Info(job+" finished", attrs...)
	}
}

// cronLogger adapts slog to cron.Logger.
type cronLogger struct{ l *slog.Logger }

func (c cronLogger) Info(msg string, kv ...any) { c.l.Debug(msg, kv...) }

func (c cronLogger) Error(err error, msg string, kv ...any) {
	c.l.Error(msg, append(kv, "err", err)...)
}

// reactionEvery is how often the daemon looks for reaction events with bars
// due: the shortest check_every of the regions, 0 without any reaction.
func reactionEvery(cfg *config.Config) time.Duration {
	var every time.Duration
	for _, s := range cfg.Sentiments() {
		if r := s.Reaction; len(r.BarSymbols()) > 0 && (every == 0 || r.CheckEvery < every) {
			every = r.CheckEvery
		}
	}
	return every
}
