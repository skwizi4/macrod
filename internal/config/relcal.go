package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"macro-collector/internal/transform"
)

// Kinds of a release calendar.
const (
	RelCalICS      = "ics"          // iCalendar file (Eurostat): dates only
	RelCalECBStats = "ecb_statscal" // ECB statistical calendar page: date and time
	RelCalDates    = "dates"        // days listed in the config (ZEW, EC survey: PDFs)
)

// ReleaseCalendar is a calendar of one publisher whose releases are known
// as dates (spec 8.1, phase 11): its events are matched by title (or listed
// with their days) and carry the series they publish, so the release
// polls those series' sources and the staleness rule knows their days.
type ReleaseCalendar struct {
	Name     string `yaml:"name"` // releases.source "relcal:<name>", fetch_state "calendar:relcal:<name>"
	Kind     string `yaml:"kind"`
	URL      string `yaml:"url"`      // ics, ecb_statscal
	Agent    string `yaml:"agent"`    // series tag: the region of the events
	Timezone string `yaml:"timezone"` // of the days and times
	Time     string `yaml:"time"`     // HH:MM of a release that states none
	// HorizonDays stores this calendar's events further ahead than
	// calendar.horizon_days (0: the same): the next release of a quarterly
	// series must be known for its stale flag (batch 11f: the labour cost
	// index comes about 75 days after its quarter).
	HorizonDays int                    `yaml:"horizon_days"`
	Events      []ReleaseCalendarEvent `yaml:"events"`

	// Resolved by Load.
	Location *time.Location `yaml:"-"`
	Clock    ClockTime      `yaml:"-"`
}

// ReleaseCalendarEvent maps entries of the calendar to an event.
type ReleaseCalendarEvent struct {
	Match      string   `yaml:"match"`   // ics/ecb_statscal: the title, exactly
	Dataset    string   `yaml:"dataset"` // ecb_statscal: the dataset code, if set
	Name       string   `yaml:"name"`    // event name in the calendar files
	Importance string   `yaml:"importance"`
	Series     []string `yaml:"series"` // configured series the release publishes (source/id or id)
	Dates      []string `yaml:"dates"`  // dates: YYYY-MM-DD
	Time       string   `yaml:"time"`   // overrides the calendar's time
	// Previous is the value shown as «Прошлое значение» in the calendar
	// file (a configured series and transform), nil for none.
	Previous *ReleasePrevious `yaml:"previous"`

	Days  []time.Time `yaml:"-"` // parsed Dates
	Clock *ClockTime  `yaml:"-"` // parsed Time
}

// SourcePrefix is the releases.source prefix of these calendars.
const SourcePrefix = "relcal:"

// Source is the releases.source of the calendar's events.
func (r ReleaseCalendar) Source() string { return SourcePrefix + r.Name }

// At is the moment of a release on day (a calendar date) at the event's
// time, or the calendar's, or clock when the entry states one.
func (r ReleaseCalendar) At(e ReleaseCalendarEvent, day time.Time, clock string) (time.Time, error) {
	c := r.Clock
	if e.Clock != nil {
		c = *e.Clock
	}
	if clock != "" {
		var err error
		if c, err = parseClock(clock); err != nil {
			return time.Time{}, err
		}
	}
	return time.Date(day.Year(), day.Month(), day.Day(), c.Hour, c.Minute, 0, 0, r.Location).UTC(), nil
}

func (c *Config) validateReleaseCalendars() error {
	var errs []error
	names := map[string]bool{}
	for i := range c.ReleaseCalendars {
		r := &c.ReleaseCalendars[i]
		where := fmt.Sprintf("release_calendars[%d] %s", i, r.Name)
		if r.Name == "" || strings.ContainsAny(r.Name, ": ") {
			errs = append(errs, fmt.Errorf("%s: name must be non-empty, without ':' or spaces", where))
		}
		if names[r.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name", where))
		}
		names[r.Name] = true
		if r.HorizonDays < 0 || r.HorizonDays > 400 {
			errs = append(errs, fmt.Errorf("%s: horizon_days %d: want 0–400", where, r.HorizonDays))
		}
		switch r.Kind {
		case RelCalICS, RelCalECBStats:
			if !strings.HasPrefix(r.URL, "https://") {
				errs = append(errs, fmt.Errorf("%s: url %q: want an https URL", where, r.URL))
			}
		case RelCalDates:
		default:
			errs = append(errs, fmt.Errorf("%s: kind %q: want ics, ecb_statscal or dates", where, r.Kind))
		}
		loc, err := time.LoadLocation(r.Timezone)
		if err != nil || r.Timezone == "" {
			errs = append(errs, fmt.Errorf("%s: timezone %q: invalid", where, r.Timezone))
		}
		r.Location = loc
		if r.Clock, err = parseClock(r.Time); err != nil {
			errs = append(errs, fmt.Errorf("%s: time: %w", where, err))
		}
		errs = append(errs, c.checkAgent(where, r.Agent))
		if len(r.Events) == 0 {
			errs = append(errs, fmt.Errorf("%s: no events", where))
		}
		seen := map[string]bool{}
		for j := range r.Events {
			e := &r.Events[j]
			ew := fmt.Sprintf("%s events[%d] %s", where, j, e.Name)
			if e.Name == "" {
				errs = append(errs, fmt.Errorf("%s: empty name", ew))
			}
			if seen[e.Name] {
				errs = append(errs, fmt.Errorf("%s: duplicate name", ew))
			}
			seen[e.Name] = true
			if !slices.Contains([]string{"high", "medium", "low"}, e.Importance) {
				errs = append(errs, fmt.Errorf("%s: importance %q: want high, medium or low", ew, e.Importance))
			}
			if r.Kind == RelCalDates {
				if e.Match != "" || e.Dataset != "" {
					errs = append(errs, fmt.Errorf("%s: match and dataset are for ics and ecb_statscal", ew))
				}
				if len(e.Dates) == 0 {
					errs = append(errs, fmt.Errorf("%s: no dates", ew))
				}
			} else {
				if e.Match == "" {
					errs = append(errs, fmt.Errorf("%s: empty match", ew))
				}
				if len(e.Dates) > 0 {
					errs = append(errs, fmt.Errorf("%s: dates are for kind dates", ew))
				}
			}
			if e.Dataset != "" && r.Kind != RelCalECBStats {
				errs = append(errs, fmt.Errorf("%s: dataset is for ecb_statscal", ew))
			}
			e.Days = e.Days[:0]
			for _, d := range e.Dates {
				day, err := time.Parse(time.DateOnly, d)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: date %q: want YYYY-MM-DD", ew, d))
					continue
				}
				e.Days = append(e.Days, day)
			}
			if e.Time != "" {
				ct, err := parseClock(e.Time)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: time: %w", ew, err))
				}
				e.Clock = &ct
			}
			if p := e.Previous; p != nil {
				if p.Name == "" {
					errs = append(errs, fmt.Errorf("%s: previous.name is required", ew))
				}
				if _, ok := c.ResolveSeries(p.Series); !ok {
					errs = append(errs, fmt.Errorf("%s: previous.series %q is not configured", ew, p.Series))
				}
				if !slices.Contains(transform.Codes, p.Code) {
					errs = append(errs, fmt.Errorf("%s: previous: unknown transform %q", ew, p.Code))
				}
			}
			for _, s := range e.Series {
				if _, ok := c.ResolveSeries(s); !ok {
					errs = append(errs, fmt.Errorf("%s: series %q is not configured", ew, s))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// ReleasePreviousOf is the «Прошлое значение» of the event name of the
// release calendar whose releases.source is source; nil without one.
func (c *Config) ReleasePreviousOf(source, name string) *ReleasePrevious {
	for _, r := range c.ReleaseCalendars {
		if r.Source() != source {
			continue
		}
		for _, e := range r.Events {
			if e.Name == name {
				return e.Previous
			}
		}
	}
	return nil
}
