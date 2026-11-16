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
	"macro-collector/internal/sources/finanzagentur"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/relcal"
	"macro-collector/internal/store"
)

// Getter downloads a calendar file.
type Getter interface {
	Get(ctx context.Context, url string) ([]byte, error)
}

// relcalSource lists the events of one configured release calendar.
type relcalSource struct {
	cal config.ReleaseCalendar
	get Getter
	cfg *config.Config
}

func (s relcalSource) Name() string { return s.cal.Source() }

// Upcoming fetches the calendar (kinds ics and ecb_statscal) and returns
// the configured events of [from, to).
func (s relcalSource) Upcoming(ctx context.Context, from, to time.Time) ([]model.ReleaseEvent, error) {
	var entries []relcal.Entry
	switch s.cal.Kind {
	case config.RelCalICS, config.RelCalECBStats:
		data, err := s.get.Get(ctx, s.cal.URL)
		if err != nil {
			return nil, err
		}
		if s.cal.Kind == config.RelCalICS {
			entries, err = relcal.ParseICS(data)
		} else {
			entries, err = relcal.ParseECBStats(data)
		}
		if err != nil {
			return nil, err
		}
	}
	return ReleaseCalendarEvents(s.cfg, s.cal, entries, from, to)
}

// ReleaseCalendarEvents turns the entries of a calendar (none for kind
// dates: the config lists the days) into its configured events within
// [from, to). Two entries of one event on a day (a page listing a release
// twice) make one event.
func ReleaseCalendarEvents(cfg *config.Config, cal config.ReleaseCalendar, entries []relcal.Entry, from, to time.Time) ([]model.ReleaseEvent, error) {
	region := cfg.RegionOfTag(cal.Agent)
	var out []model.ReleaseEvent
	seen := map[string]bool{}
	add := func(e config.ReleaseCalendarEvent, day time.Time, clock string) error {
		at, err := cal.At(e, day, clock)
		if err != nil {
			return fmt.Errorf("calendar %s %q: %w", cal.Name, e.Name, err)
		}
		if at.Before(from) || !at.Before(to) {
			return nil
		}
		k := e.Name + "|" + at.Format(time.RFC3339)
		if seen[k] {
			return nil
		}
		seen[k] = true
		ev := model.ReleaseEvent{Source: cal.Source(), Name: e.Name, ScheduledAt: at, Region: region, Importance: e.Importance}
		for _, s := range e.Series {
			if def, ok := cfg.ResolveSeries(s); ok {
				ev.Related.Series = append(ev.Related.Series, def.ID)
			}
		}
		out = append(out, ev)
		return nil
	}
	for _, e := range cal.Events {
		if cal.Kind == config.RelCalDates {
			for _, d := range e.Days {
				if err := add(e, d, ""); err != nil {
					return nil, err
				}
			}
			continue
		}
		for _, en := range entries {
			if en.Title != e.Match || (e.Dataset != "" && en.Dataset != e.Dataset) {
				continue
			}
			if err := add(e, en.Day, en.Clock); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// ReleaseCalendarKeys are the (releases.source, name) series of a calendar:
// each is synced even when the window lists none, so a moved release is
// marked rescheduled.
func ReleaseCalendarKeys(cal config.ReleaseCalendar) [][2]string {
	keys := make([][2]string, 0, len(cal.Events))
	for _, e := range cal.Events {
		keys = append(keys, [2]string{cal.Source(), e.Name})
	}
	return keys
}

// ReleaseCalendarsSummary is one refresh of the configured release
// calendars and the Bund auction plan.
type ReleaseCalendarsSummary struct {
	Calendars map[string]calendar.SourceReport // by releases.source
	Errors    map[string]error                 // by releases.source, the calendars that failed
	Err       error                            // Errors joined
}

// Events is the number of events in the window, all calendars together.
func (s ReleaseCalendarsSummary) Events() int {
	n := 0
	for _, r := range s.Calendars {
		n += r.Events
	}
	return n
}

// RefreshReleaseCalendars syncs each configured release calendar and, with
// the finanzagentur section enabled, the Bund auctions of the issuance
// outlook. One failing does not stop the others; each records its error in
// fetch_state["calendar:<source>"].
func RefreshReleaseCalendars(ctx context.Context, cfg *config.Config, st *store.Store, get Getter, bund *finanzagentur.Client, root *slog.Logger) ReleaseCalendarsSummary {
	log := logging.For(root, "calendar")
	sum := ReleaseCalendarsSummary{Calendars: map[string]calendar.SourceReport{}, Errors: map[string]error{}}
	var errs []error
	note := func(name string, rep calendar.SourceReport, err error) {
		sum.Calendars[name] = rep
		if err != nil {
			sum.Errors[name] = err
			errs = append(errs, err)
		}
	}
	for _, cal := range cfg.ReleaseCalendars {
		window := &cfg.Calendar
		if cal.HorizonDays > cfg.Calendar.HorizonDays {
			c := cfg.Calendar
			c.HorizonDays = cal.HorizonDays
			window = &c
		}
		src := &calendar.Source{
			Source: relcalSource{cal: cal, get: get, cfg: cfg},
			Keys:   ReleaseCalendarKeys(cal), Region: cfg.RegionOfTag(cal.Agent), Store: st, Calendar: window, Log: log,
		}
		rep, err := src.Refresh(ctx)
		note(cal.Source(), rep, err)
	}
	if bund != nil && cfg.Finanzagentur.Enabled {
		src := &calendar.Source{
			Source:  bundCalendar{client: bund, cfg: cfg},
			Dynamic: []string{finanzagentur.SourceAuction}, Region: cfg.RegionOfTag(cfg.Finanzagentur.Agent),
			Store: st, Calendar: &cfg.Calendar, Log: log,
		}
		rep, err := src.Refresh(ctx)
		note(finanzagentur.SourceAuction, rep, err)
	}
	sum.Err = errors.Join(errs...)
	return sum
}

// NewCalendarGetter returns the HTTP client of the release calendars.
func NewCalendarGetter(root *slog.Logger) Getter {
	log := logging.For(root, "calendar")
	return httpx.New(httpx.WithLogger(log), httpx.WithInterval(numericInterval))
}

// bundCalendar lists the planned Bund auctions of the securities the
// config puts in the calendar.
type bundCalendar struct {
	client *finanzagentur.Client
	cfg    *config.Config
}

func (bundCalendar) Name() string { return finanzagentur.SourceAuction }

func (b bundCalendar) Upcoming(ctx context.Context, from, to time.Time) ([]model.ReleaseEvent, error) {
	ps, err := b.client.Outlook(ctx, b.cfg.Finanzagentur.CalendarPage)
	if err != nil {
		return nil, err
	}
	return BundAuctionEvents(b.cfg, ps, from, to), nil
}

// BundAuctionEvents are the calendar events of the planned auctions within
// [from, to): the close of bidding (11:30 Berlin), named "Bund 10 years".
func BundAuctionEvents(cfg *config.Config, ps []finanzagentur.Planned, from, to time.Time) []model.ReleaseEvent {
	sec := cfg.Finanzagentur
	region := cfg.RegionOfTag(sec.Agent)
	var out []model.ReleaseEvent
	for _, p := range ps {
		imp, ok := sec.CalendarImportance[p.Security]
		if !ok {
			continue
		}
		at := finanzagentur.CloseAt(p.Day, sec.Location)
		if at.Before(from) || !at.Before(to) {
			continue
		}
		out = append(out, model.ReleaseEvent{
			Source: finanzagentur.SourceAuction, Name: p.Name(), ScheduledAt: at, Region: region, Importance: imp,
			Related: model.Related{CUSIP: p.ISIN, Security: p.Security, Tenor: p.Tenor()},
		})
	}
	return out
}
