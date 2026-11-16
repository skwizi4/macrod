package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"sync"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/guidance"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/edgar"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/irtranscript"
	"macro-collector/internal/sources/nasdaq"
	"macro-collector/internal/store"
	"macro-collector/internal/transform"
)

// EDGARClient is the part of edgar.Client the collector uses.
type EDGARClient interface {
	Submissions(ctx context.Context, cik string) (edgar.Submissions, error)
	Facts(ctx context.Context, cik string, concepts []string) ([]model.CompanyFact, error)
	PressRelease(ctx context.Context, cik, accession string) (edgar.PressRelease, error)
	InstanceFacts(ctx context.Context, cik string, f edgar.Filing, sel edgar.InstanceSelect) ([]model.CompanyFact, error)
}

// InstanceSelects are the instance selections of the companies with
// segments (edgar.segments, phase 5d).
func InstanceSelects(cfg *config.Config) map[string]edgar.InstanceSelect {
	out := map[string]edgar.InstanceSelect{}
	for t, sg := range cfg.EDGAR.Segments {
		var members []string
		for _, m := range sg.Members {
			members = append(members, m.Member)
		}
		out[t] = edgar.SegmentSelect(sg.Revenue, sg.Profit, members)
	}
	return out
}

// NewEDGARClient returns the EDGAR client of the config, or nil when EDGAR
// is disabled.
func NewEDGARClient(cfg *config.Config, root *slog.Logger) EDGARClient {
	if !cfg.EDGAR.Enabled {
		return nil
	}
	log := logging.For(root, edgar.Source)
	h := httpx.New(httpx.WithLogger(log), httpx.WithUserAgent(cfg.EDGAR.UserAgent),
		httpx.WithInterval(edgar.Interval(cfg.EDGAR.RequestsPerSecond)))
	return edgar.New(cfg.EDGAR.DataURL, cfg.EDGAR.ArchivesURL, h, log)
}

// TranscriptClient is the part of irtranscript.Client the collector uses.
type TranscriptClient interface {
	Fetch(ctx context.Context, page, feed string, fy, q int) (irtranscript.Transcript, bool, error)
}

// NewTranscriptClient returns the call transcript client of the config, or
// nil when transcripts are disabled.
func NewTranscriptClient(cfg *config.Config, root *slog.Logger) TranscriptClient {
	t := cfg.EDGAR.Transcripts
	if !cfg.EDGAR.Enabled || !t.Enabled {
		return nil
	}
	log := logging.For(root, irtranscript.Source)
	h := httpx.New(httpx.WithLogger(log), httpx.WithUserAgent(cfg.EDGAR.UserAgent), httpx.WithInterval(t.Interval))
	return irtranscript.New(h, log)
}

// Filing selection (LOOKUP E2).
const (
	// periodicLag is the longest gap between an earnings 8-K and the
	// 10-Q/10-K of the same quarter (AVGO files ~7 days later).
	periodicLag = 45 * 24 * time.Hour
	// calendarSlack is how far an 8-K may be from the calendar date.
	calendarSlack = 24 * time.Hour
)

// EarningsFiling is an earnings 8-K and the quarter it reports.
type EarningsFiling struct {
	Filing    edgar.Filing
	PeriodEnd time.Time // from the 10-Q/10-K that follows, else estimated
	Estimated bool      // no 10-Q/10-K yet
}

// EarningsFilings picks the earnings 8-Ks among item 2.02 filings accepted
// from since. Tesla files deliveries under 2.02 too, so an 8-K counts only
// when the earnings calendar has a report within a day of it, or when it is
// the last 2.02 filing before a 10-Q/10-K that follows within 45 days.
// calendarDays are the New York dates of the company's earnings events.
func EarningsFilings(subs edgar.Submissions, since time.Time, calendarDays []time.Time, ny *time.Location) []EarningsFiling {
	var candidates, periodic []edgar.Filing
	for _, f := range subs.Filings {
		if f.AcceptedAt.Before(since) {
			continue
		}
		switch {
		case f.Form == "8-K" && f.HasItem(edgar.ItemEarnings):
			candidates = append(candidates, f)
		case f.Form == "10-Q" || f.Form == "10-K":
			periodic = append(periodic, f)
		}
	}
	byAccepted := func(a, b edgar.Filing) int { return a.AcceptedAt.Compare(b.AcceptedAt) }
	slices.SortFunc(candidates, byAccepted)
	slices.SortFunc(periodic, byAccepted)

	lastPeriodicBefore := func(t time.Time) (edgar.Filing, bool) {
		var out edgar.Filing
		ok := false
		for _, p := range periodic {
			if p.AcceptedAt.Before(t) {
				out, ok = p, true
			}
		}
		return out, ok
	}
	var out []EarningsFiling
	for i, c := range candidates {
		var next *edgar.Filing
		for j := range periodic {
			if p := periodic[j]; p.AcceptedAt.After(c.AcceptedAt) && p.AcceptedAt.Sub(c.AcceptedAt) <= periodicLag {
				next = &periodic[j]
				break
			}
		}
		lastBefore := next != nil && (i == len(candidates)-1 || !candidates[i+1].AcceptedAt.Before(next.AcceptedAt))
		day := model.DayIn(c.AcceptedAt, ny)
		onCalendar := slices.ContainsFunc(calendarDays, func(d time.Time) bool {
			diff := d.Sub(day)
			return diff >= -calendarSlack && diff <= calendarSlack
		})
		if !lastBefore && !onCalendar {
			continue
		}
		ef := EarningsFiling{Filing: c}
		switch {
		case next != nil && !next.ReportDate.IsZero():
			ef.PeriodEnd = next.ReportDate
		default:
			ef.Estimated = true
			if prev, ok := lastPeriodicBefore(c.AcceptedAt); ok {
				ef.PeriodEnd = transform.NextQuarterEnd(prev.ReportDate)
			} else {
				ef.PeriodEnd = previousMonthEnd(day)
			}
		}
		out = append(out, ef)
	}
	return out
}

func previousMonthEnd(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), 0, 0, 0, 0, 0, time.UTC)
}

// EDGARResult is the result of one company.
type EDGARResult struct {
	Ticker     string
	Reports    []model.CompanyReport // earnings reports known after the run, newest first
	NewReports int
	FactsRead  bool // company facts were fetched
	Facts      int  // fact rows inserted or changed
	// FactsCurrent: the facts include the latest 10-Q/10-K, whose
	// acceptance time is LatestPeriodic (zero if none since history).
	FactsCurrent   bool
	LatestPeriodic time.Time
	Err            error
}

// EDGARReport summarises a run.
type EDGARReport struct {
	Results     []EDGARResult
	Interrupted bool
}

// Failed returns the failed companies.
func (r EDGARReport) Failed() []EDGARResult {
	var out []EDGARResult
	for _, c := range r.Results {
		if c.Err != nil {
			out = append(out, c)
		}
	}
	return out
}

// EDGAR collects earnings reports and XBRL facts. Requests per company: one
// submissions read, the press release (index + Ex-99.1) of a new earnings
// 8-K, and company facts only when a 10-Q/10-K is not yet in the stored
// facts (or on backfill). 10-K/10-Q documents themselves are never read.
type EDGAR struct {
	Client      EDGARClient
	Transcripts TranscriptClient // nil: no call transcripts
	Store       *store.Store
	Config      *config.Config
	Log         *slog.Logger
	Now         func() time.Time
	// Guidance searches new transcripts and earnings 8-Ks of
	// edgar.guidance_tickers for capex guidance candidates (phase 14b);
	// nil: no search.
	Guidance *guidance.Service

	mu    sync.Mutex
	tried map[string]time.Time // transcript tries by 8-K accession
}

// region is the region of a company's documents.
func (e *EDGAR) region(ticker string) string {
	r, _, _ := e.Config.RegionOfCompany(ticker)
	return r
}

// Run processes the companies with the tickers (all when empty); a failing
// company is recorded in fetch_state["edgar:<ticker>"] and does not stop the
// others.
func (e *EDGAR) Run(ctx context.Context, mode ingest.Mode, tickers ...string) EDGARReport {
	var rep EDGARReport
	for _, c := range e.Config.EDGAR.Companies {
		if len(tickers) > 0 && !slices.Contains(tickers, c.Ticker) {
			continue
		}
		if ctx.Err() != nil {
			rep.Interrupted = true
			break
		}
		res := e.company(ctx, mode, c)
		if res.Err != nil && ctx.Err() != nil {
			rep.Interrupted = true
			break
		}
		if res.Err != nil {
			e.Log.Error("company failed", "ticker", c.Ticker, "err", res.Err)
			if err := e.Store.Write(ctx, func(tx *sql.Tx) error {
				return store.SetFetchError(tx, edgarKey(c.Ticker), res.Err, e.now())
			}); err != nil {
				e.Log.Error("record fetch error", "ticker", c.Ticker, "err", err)
			}
		} else {
			e.Log.Info("company done", "ticker", c.Ticker, "reports", len(res.Reports), "new_reports", res.NewReports,
				"facts_read", res.FactsRead, "facts_changed", res.Facts, "facts_current", res.FactsCurrent)
		}
		rep.Results = append(rep.Results, res)
	}
	return rep
}

func edgarKey(ticker string) string { return edgar.Source + ":" + ticker }

func (e *EDGAR) company(ctx context.Context, mode ingest.Mode, c config.Company) EDGARResult {
	res := EDGARResult{Ticker: c.Ticker}
	cfg := &e.Config.EDGAR
	ny := e.Config.Calendar.ReleaseLocation
	now := e.now()
	subs, err := e.Client.Submissions(ctx, c.CIK)
	if err != nil {
		res.Err = err
		return res
	}
	days, err := e.calendarDays(ctx, c.Ticker, now)
	if err != nil {
		res.Err = err
		return res
	}

	// Earnings 8-Ks: new ones get their press release; stored ones are
	// updated when the 10-Q fixes the estimated period or the release is
	// still missing.
	type pending struct {
		report model.CompanyReport
		doc    *model.Document
	}
	var writes []pending
	var errs []error
	for _, ef := range EarningsFilings(subs, cfg.History, days, ny) {
		f := ef.Filing
		r := model.CompanyReport{Ticker: c.Ticker, PeriodEnd: ef.PeriodEnd, Form: f.Form, ReportDate: model.DayIn(f.AcceptedAt, ny),
			Accession: f.Accession, AcceptedAt: f.AcceptedAt, Session: transform.Session(f.AcceptedAt, ny)}
		old, ok, err := e.Store.CompanyReport(ctx, f.Accession)
		if err != nil {
			res.Err = err
			return res
		}
		if ok && old.PeriodEnd.Equal(r.PeriodEnd) && old.DocumentID != 0 {
			continue
		}
		p := pending{report: r}
		if !ok || old.DocumentID == 0 {
			pr, err := e.Client.PressRelease(ctx, c.CIK, f.Accession)
			if err != nil {
				if ctx.Err() != nil {
					res.Err = err
					return res
				}
				errs = append(errs, err) // the report is stored; the release is retried next run
			} else {
				p.doc = pressDocument(c.Ticker, f, pr)
			}
		}
		if !ok {
			res.NewReports++
		}
		writes = append(writes, p)
	}

	// Facts: read when the newest 10-Q/10-K is not stored yet. XBRL lags
	// the filing by up to a minute; until the facts carry it, they are read
	// again on the next run.
	var facts []model.CompanyFact
	latest, hasPeriodic := latestPeriodic(subs, cfg.History)
	if hasPeriodic {
		res.LatestPeriodic = latest.AcceptedAt
		have, err := e.Store.HasFactsFrom(ctx, c.Ticker, latest.Accession)
		if err != nil {
			res.Err = err
			return res
		}
		res.FactsCurrent = have
		if !have || mode == ingest.Backfill {
			if facts, err = e.Client.Facts(ctx, c.CIK, c.Resolved.Tags()); err != nil {
				res.Err = err
				return res
			}
			res.FactsRead = true
			res.FactsCurrent = slices.ContainsFunc(facts, func(f model.CompanyFact) bool { return f.Accession == latest.Accession })
			if !res.FactsCurrent {
				e.Log.Info("xbrl facts of the latest filing not available yet", "ticker", c.Ticker, "accn", latest.Accession, "form", latest.Form)
			}
		}
	}

	cands, err := e.candidates(ctx, c.Ticker, func(yield func(*model.Document) bool) {
		for _, p := range writes {
			if p.doc != nil && !yield(p.doc) {
				return
			}
		}
	})
	if err != nil {
		res.Err = err
		return res
	}
	var announce []store.GuidanceCandidate

	cursor := ""
	if len(subs.Filings) > 0 {
		cursor = newestAccepted(subs).Format(time.RFC3339)
	}
	err = e.Store.Write(ctx, func(tx *sql.Tx) error {
		for _, p := range writes {
			if p.doc != nil {
				p.doc.Region = e.region(c.Ticker)
				id, _, err := store.UpsertDocument(tx, *p.doc, now)
				if err != nil {
					return err
				}
				p.report.DocumentID = id
			}
			if err := store.UpsertCompanyReport(tx, p.report); err != nil {
				return err
			}
		}
		n, err := store.UpsertCompanyFacts(tx, c.Ticker, facts, now)
		if err != nil {
			return err
		}
		res.Facts = n
		if announce, err = guidance.Insert(tx, cands, now); err != nil {
			return err
		}
		if len(errs) > 0 {
			return store.SetFetchError(tx, edgarKey(c.Ticker), errors.Join(errs...), now)
		}
		return store.SetFetchOK(tx, edgarKey(c.Ticker), cursor, now)
	})
	if err != nil {
		res.Err = fmt.Errorf("edgar store %s: %w", c.Ticker, err)
		return res
	}
	e.announce(ctx, announce)
	if res.Reports, err = e.Store.CompanyReportsOf(ctx, c.Ticker); err != nil {
		res.Err = err
		return res
	}
	e.transcripts(ctx, c, subs.FiscalYearEnd, res.Reports, now)
	e.instances(ctx, mode, c, subs, now)
	if len(errs) > 0 {
		res.Err = errors.Join(errs...)
	}
	return res
}

// instances stores the segment facts and the RPO total of the company's
// XBRL instances (phase 5d, ТЗ §20): the newest 10-Q/10-K once, or the
// newest edgar.InstanceFixtures on backfill (the quarter, its YoY and a
// fiscal Q4 = year − 9 months). fetch_state["edgar-instance:<ticker>"]
// holds the last one read; a failure there does not fail the EDGAR run.
func (e *EDGAR) instances(ctx context.Context, mode ingest.Mode, c config.Company, subs edgar.Submissions, now time.Time) {
	sel, ok := InstanceSelects(e.Config)[c.Ticker]
	if !ok {
		return
	}
	key := "edgar-instance:" + c.Ticker
	n := 1
	if mode == ingest.Backfill {
		n = edgar.InstanceFixtures
	}
	filings := slices.DeleteFunc(edgar.LatestPeriodic(subs, n), func(f edgar.Filing) bool { return f.AcceptedAt.Before(e.Config.EDGAR.History) })
	if len(filings) == 0 {
		return
	}
	st, _, err := e.Store.FetchStateOf(ctx, key)
	if err != nil || (mode != ingest.Backfill && st.Cursor == filings[0].Accession) {
		return
	}
	var facts []model.CompanyFact
	var ferr error
	for _, f := range filings {
		fs, err := e.Client.InstanceFacts(ctx, c.CIK, f, sel)
		if err != nil {
			ferr = err
			break
		}
		facts = append(facts, fs...)
	}
	n = 0
	werr := e.Store.Write(ctx, func(tx *sql.Tx) error {
		var err error
		if n, err = store.UpsertInstanceFacts(tx, c.Ticker, facts, now); err != nil {
			return err
		}
		if ferr != nil {
			return store.SetFetchError(tx, key, ferr, now)
		}
		return store.SetFetchOK(tx, key, filings[0].Accession, now)
	})
	switch {
	case werr != nil:
		e.Log.Error("store instance facts", "ticker", c.Ticker, "err", werr)
	case ferr != nil:
		e.Log.Warn("instance facts failed", "ticker", c.Ticker, "err", ferr)
	default:
		e.Log.Info("instance facts stored", "ticker", c.Ticker, "filings", len(filings), "facts_changed", n)
	}
}

// transcripts stores the call transcripts of the company's recent reports
// (phase 5b, ТЗ §18.4): tried for window_days after the 8-K, at most once
// per retry_every while the transcript is not up. A failure is recorded in
// fetch_state["transcript:<ticker>"] and does not fail the EDGAR run.
func (e *EDGAR) transcripts(ctx context.Context, c config.Company, fyEnd string, reports []model.CompanyReport, now time.Time) {
	t := &e.Config.EDGAR.Transcripts
	src, ok := t.Transcript(c.Ticker)
	if e.Transcripts == nil || !ok || fyEnd == "" {
		return
	}
	key := irtranscript.Source + ":" + c.Ticker
	for _, r := range reports {
		if age := now.Sub(r.AcceptedAt); age < 0 || age > time.Duration(t.WindowDays)*24*time.Hour {
			continue
		}
		if _, ok, err := e.Store.Document(ctx, irtranscript.Source, r.Accession); err != nil || ok {
			continue
		}
		if !e.due(r.Accession, now, t.Retry) {
			continue
		}
		fy, q, err := transform.FiscalQuarter(r.PeriodEnd, fyEnd)
		if err != nil {
			continue
		}
		tr, ok, err := e.Transcripts.Fetch(ctx, src.Page, src.Feed, fy, q)
		if err == nil && !ok {
			e.Log.Info("call transcript not published yet", "ticker", c.Ticker, "accn", r.Accession, "fy", fy, "q", q)
			continue
		}
		doc := model.Document{Source: irtranscript.Source, ExternalID: r.Accession, DocType: "transcript", Region: e.region(c.Ticker),
			PublishedAt: r.AcceptedAt, Author: c.Ticker, Title: fmt.Sprintf("%s earnings call transcript, FY%d Q%d", c.Ticker, fy, q),
			Body: tr.Text, URL: tr.URL}
		var cands, announce []store.GuidanceCandidate
		if err == nil {
			var cerr error
			if cands, cerr = e.candidates(ctx, c.Ticker, func(yield func(*model.Document) bool) { yield(&doc) }); cerr != nil {
				e.Log.Error("guidance candidates", "ticker", c.Ticker, "err", cerr)
			}
		}
		werr := e.Store.Write(ctx, func(tx *sql.Tx) error {
			if err != nil {
				return store.SetFetchError(tx, key, err, now)
			}
			if _, _, err := store.UpsertDocument(tx, doc, now); err != nil {
				return err
			}
			var ierr error
			if announce, ierr = guidance.Insert(tx, cands, now); ierr != nil {
				return ierr
			}
			return store.SetFetchOK(tx, key, r.Accession, now)
		})
		if werr == nil {
			e.announce(ctx, announce)
		}
		switch {
		case werr != nil:
			e.Log.Error("store call transcript", "ticker", c.Ticker, "err", werr)
		case err != nil:
			e.Log.Warn("call transcript failed", "ticker", c.Ticker, "accn", r.Accession, "err", err)
		default:
			e.Log.Info("call transcript stored", "ticker", c.Ticker, "accn", r.Accession, "url", tr.URL)
		}
	}
}

// candidates are the guidance candidates of new documents of ticker (none
// when the ticker is not in edgar.guidance_tickers).
func (e *EDGAR) candidates(ctx context.Context, ticker string, docs iter.Seq[*model.Document]) ([]store.GuidanceCandidate, error) {
	if e.Guidance == nil || !e.Guidance.Enabled(ticker) {
		return nil, nil
	}
	var current []guidance.Value
	var out []store.GuidanceCandidate
	for d := range docs {
		if current == nil {
			var err error
			if current, err = e.Guidance.Current(ctx); err != nil {
				return nil, fmt.Errorf("guidance %s: %w", ticker, err)
			}
		}
		out = append(out, guidance.Candidates(ticker, e.Guidance.DocOf(*d), current)...)
	}
	return out, nil
}

// announce logs new candidates and sends them to Telegram with buttons.
func (e *EDGAR) announce(ctx context.Context, cands []store.GuidanceCandidate) {
	for _, c := range cands {
		e.Log.Info("guidance candidate found", "id", c.ID, "ticker", c.Ticker, "year", c.Year, "doc", c.DocSource+":"+c.DocRef)
	}
	if e.Guidance != nil {
		e.Guidance.Announce(ctx, cands)
	}
}

// due reports whether the transcript of the 8-K may be tried now, and
// records the try.
func (e *EDGAR) due(accession string, now time.Time, every time.Duration) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if last, ok := e.tried[accession]; ok && now.Sub(last) < every {
		return false
	}
	if e.tried == nil {
		e.tried = map[string]time.Time{}
	}
	e.tried[accession] = now
	return true
}

// calendarDays are the New York dates of ticker's earnings events since
// history start, including past ones (rescheduled events are left out).
func (e *EDGAR) calendarDays(ctx context.Context, ticker string, now time.Time) ([]time.Time, error) {
	evs, err := e.Store.Releases(ctx, e.Config.EDGAR.History, now.AddDate(0, 0, e.Config.Calendar.HorizonDays+1))
	if err != nil {
		return nil, err
	}
	var out []time.Time
	for _, ev := range evs {
		if ev.Source == nasdaq.Source && ev.Related.Ticker == ticker && ev.Status != model.StatusRescheduled {
			out = append(out, model.DayIn(ev.ScheduledAt, e.Config.Calendar.ReleaseLocation))
		}
	}
	return out, nil
}

func pressDocument(ticker string, f edgar.Filing, pr edgar.PressRelease) *model.Document {
	title := edgar.Title(pr.Text)
	if title == "" {
		title = ticker + " earnings release"
	}
	return &model.Document{Source: edgar.Source, ExternalID: f.Accession, DocType: f.Form,
		PublishedAt: f.AcceptedAt, Author: ticker, Title: title, Body: pr.Text, URL: pr.URL}
}

func latestPeriodic(subs edgar.Submissions, since time.Time) (edgar.Filing, bool) {
	var out edgar.Filing
	ok := false
	for _, f := range subs.Filings {
		if (f.Form == "10-Q" || f.Form == "10-K") && !f.AcceptedAt.Before(since) && (!ok || f.AcceptedAt.After(out.AcceptedAt)) {
			out, ok = f, true
		}
	}
	return out, ok
}

func newestAccepted(subs edgar.Submissions) time.Time {
	var t time.Time
	for _, f := range subs.Filings {
		if f.AcceptedAt.After(t) {
			t = f.AcceptedAt
		}
	}
	return t
}

func (e *EDGAR) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// RunEDGAR collects the companies (all when tickers is empty).
// g searches new documents for capex guidance candidates (nil: none).
func RunEDGAR(ctx context.Context, cfg *config.Config, st *store.Store, client EDGARClient, g *guidance.Service, root *slog.Logger,
	mode ingest.Mode, tickers ...string) (EDGARReport, time.Duration) {
	start := time.Now()
	e := &EDGAR{Client: client, Transcripts: NewTranscriptClient(cfg, root), Store: st, Config: cfg, Log: logging.For(root, edgar.Source),
		Guidance: g}
	return e.Run(ctx, mode, tickers...), time.Since(start)
}
