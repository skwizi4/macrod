package collect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"macro-collector/internal/calendar"
	"macro-collector/internal/config"
	"macro-collector/internal/logging"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/nasdaq"
	"macro-collector/internal/sources/treasury"
	"macro-collector/internal/store"
)

// CalendarSummary is the result of refreshing all calendars.
type CalendarSummary struct {
	FRED    calendar.FREDReport
	FREDErr error
	Fed     calendar.SourceReport
	FedErr  error
	// Meetings syncs FOMC decisions a year ahead (FED_PATH contract months).
	Meetings    calendar.SourceReport
	MeetingsErr error
	// Treasury is zero and TreasuryErr nil when no Treasury client is given.
	Treasury    calendar.SourceReport
	TreasuryErr error
	// Earnings dates (Nasdaq); zero when no client is given or the
	// earnings calendar is disabled. EarningsErr holds per-company errors;
	// the companies that answered are synced anyway.
	Earnings       calendar.SourceReport
	EarningsErr    error
	EarningsNoDate []string // tickers without an upcoming date (reported, next date not announced)
	// ECB Governing Council meetings a year ahead; zero when ecb_texts is
	// disabled.
	ECB    calendar.SourceReport
	ECBErr error
	// Release calendars of the config and the Bund auction plan; zero when
	// none is configured.
	Release ReleaseCalendarsSummary
}

// Err joins the calendar errors.
func (s CalendarSummary) Err() error {
	return errors.Join(s.FREDErr, s.FedErr, s.MeetingsErr, s.TreasuryErr, s.EarningsErr, s.ECBErr, s.Release.Err)
}

// EarningsClient is the part of nasdaq.Client the calendar uses.
type EarningsClient interface {
	EarningsDate(ctx context.Context, ticker string) (nasdaq.EarningsDate, error)
}

// fetchedCalendar serves events fetched beforehand.
type fetchedCalendar struct {
	name   string
	events []model.ReleaseEvent
}

func (f fetchedCalendar) Name() string { return f.name }

func (f fetchedCalendar) Upcoming(_ context.Context, from, to time.Time) ([]model.ReleaseEvent, error) {
	var out []model.ReleaseEvent
	for _, e := range f.events {
		if !e.ScheduledAt.Before(from) && e.ScheduledAt.Before(to) {
			out = append(out, e)
		}
	}
	return out, nil
}

// refreshEarnings asks Nasdaq for each company's next report and syncs the
// companies that gave a date. A company with no date or an error keeps its
// stored event: an empty answer would otherwise mark a known report
// rescheduled.
func refreshEarnings(ctx context.Context, cfg *config.Config, st *store.Store, client EarningsClient,
	log *slog.Logger) (calendar.SourceReport, []string, error) {
	var events []model.ReleaseEvent
	var keys [][2]string
	var noDate []string
	var errs []error
	for _, c := range cfg.EDGAR.Companies {
		d, err := client.EarningsDate(ctx, c.Ticker)
		if err != nil {
			if ctx.Err() != nil {
				return calendar.SourceReport{}, noDate, ctx.Err()
			}
			log.Warn("earnings date failed", "ticker", c.Ticker, "err", err)
			errs = append(errs, err)
			continue
		}
		if !d.OK {
			noDate = append(noDate, c.Ticker)
			continue
		}
		ev := nasdaq.Event(d, c, &cfg.Earnings, cfg.Calendar.ReleaseLocation)
		ev.Region, _, _ = cfg.RegionOfCompany(c.Ticker)
		events = append(events, ev)
		keys = append(keys, [2]string{nasdaq.Source, nasdaq.EventName(c.Ticker)})
	}
	if len(errs) == len(cfg.EDGAR.Companies) && len(errs) > 0 {
		return calendar.SourceReport{}, noDate, fmt.Errorf("calendar earnings: every company failed: %w", errors.Join(errs...))
	}
	long := cfg.Calendar
	long.HorizonDays = EarningsHorizonDays
	src := &calendar.Source{
		Source: fetchedCalendar{name: nasdaq.Source, events: events},
		Keys:   keys, Store: st, Calendar: &long, Log: log,
	}
	rep, err := src.Refresh(ctx)
	return rep, noDate, errors.Join(append(errs, err)...)
}

// EarningsHorizonDays keeps the whole reporting season in the calendar
// (USA Micro lists who is still to report: NVDA and AVGO come 4–7 weeks
// after the hyperscalers).
const EarningsHorizonDays = 100

// MeetingsHorizonDays covers the 12 months of ZQ contracts behind FED_PATH
// plus the current month.
const MeetingsHorizonDays = 400

// fomcDecisions keeps only the FOMC decisions of the Fed calendar, under
// its own fetch_state key (calendar:fomc_meetings).
type fomcDecisions struct{ sources.CalendarSource }

func (fomcDecisions) Name() string { return "fomc_meetings" }

func (d fomcDecisions) Upcoming(ctx context.Context, from, to time.Time) ([]model.ReleaseEvent, error) {
	evs, err := d.CalendarSource.Upcoming(ctx, from, to)
	if err != nil {
		return nil, err
	}
	var out []model.ReleaseEvent
	for _, e := range evs {
		if e.Source == fedtexts.SourceFOMC && e.Name == fedtexts.NameDecision {
			out = append(out, e)
		}
	}
	return out, nil
}

// RefreshCalendars syncs the FRED release calendar, the Fed calendar (FOMC
// meetings, minutes, Beige Book), the Treasury calendar (auctions, DTS,
// MTS, refunding), company earnings dates and, with ecb_texts enabled, the
// ECB meetings and the release calendars of the config (Eurostat, ECB
// statistics, ZEW, EC survey, Bund auctions). One failing does not stop the
// others. A nil treasuryClient or earnings skips that calendar.
func RefreshCalendars(ctx context.Context, cfg *config.Config, st *store.Store, fredClient calendar.FREDClient,
	fedClient *fedtexts.Client, treasuryClient *treasury.Client, earnings EarningsClient, root *slog.Logger) CalendarSummary {
	log := logging.For(root, "calendar")
	var sum CalendarSummary
	f := &calendar.FRED{Client: fredClient, Store: st, Series: cfg.FRED.Series, Calendar: &cfg.Calendar, RegionOf: cfg.RegionOfTag, Log: log}
	sum.FRED, sum.FREDErr = f.Refresh(ctx)
	fed := &calendar.Source{
		Source: &fedtexts.Calendar{
			Client: fedClient, FOMCURL: cfg.FedTexts.FOMCCalendar, BeigeBookURL: cfg.FedTexts.BeigeBook,
			Location: cfg.Calendar.ReleaseLocation,
		},
		Keys: fedtexts.Keys, Region: cfg.RegionOfTag(cfg.FedTexts.Agent), Store: st, Calendar: &cfg.Calendar, Log: log,
	}
	sum.Fed, sum.FedErr = fed.Refresh(ctx)
	long := cfg.Calendar
	long.HorizonDays = MeetingsHorizonDays
	meetings := &calendar.Source{
		Source: fomcDecisions{fed.Source},
		Keys:   [][2]string{{fedtexts.SourceFOMC, fedtexts.NameDecision}}, Region: cfg.RegionOfTag(cfg.FedTexts.Agent), Store: st, Calendar: &long, Log: log,
	}
	sum.Meetings, sum.MeetingsErr = meetings.Refresh(ctx)
	if treasuryClient != nil {
		var types []string
		if d, ok := cfg.Treasury.Dataset(config.DatasetAuctions); ok {
			types = d.SecurityTypes
		}
		tc := &calendar.Source{
			Source: &treasury.Calendar{Client: treasuryClient, SecurityTypes: types, ET: cfg.Calendar.ReleaseLocation},
			Keys:   treasury.Keys, Dynamic: []string{treasury.SourceAuction},
			Region: cfg.TreasuryRegion(), Store: st, Calendar: &cfg.Calendar, Log: log,
		}
		sum.Treasury, sum.TreasuryErr = tc.Refresh(ctx)
	}
	if earnings != nil && cfg.Earnings.Enabled && cfg.EDGAR.Enabled {
		sum.Earnings, sum.EarningsNoDate, sum.EarningsErr = refreshEarnings(ctx, cfg, st, earnings, log)
	}
	if ecb := NewECBClient(cfg, root); ecb != nil {
		sum.ECB, sum.ECBErr = RefreshECBCalendar(ctx, cfg, st, ecb, root)
	}
	if len(cfg.ReleaseCalendars) > 0 || cfg.Finanzagentur.Enabled {
		sum.Release = RefreshReleaseCalendars(ctx, cfg, st, NewCalendarGetter(root), NewFinanzagenturClient(cfg, root), root)
	}
	return sum
}

// NewEarningsClient returns the Nasdaq earnings client, or nil when the
// earnings calendar or EDGAR is disabled.
func NewEarningsClient(cfg *config.Config, root *slog.Logger) EarningsClient {
	if !cfg.Earnings.Enabled || !cfg.EDGAR.Enabled {
		return nil
	}
	log := logging.For(root, nasdaq.Source)
	return nasdaq.New(cfg.Earnings.Endpoint, httpx.New(httpx.WithLogger(log), httpx.WithInterval(time.Second)), log)
}
