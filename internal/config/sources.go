package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Numeric sources configured by a section of series (spec 6.1). The name is
// the section, observations.source and the fetch_state prefix.
const (
	SourceECB        = "ecb"
	SourceBundesbank = "bundesbank"
	SourceEurostat   = "eurostat"
	SourceSTOXX      = "stoxx"
	SourceZEW        = "zew"
	SourceManual     = "manual"
)

// Units a configured series may state: the stored series_meta.units, which
// decide how the reports write its values.
const (
	UnitsRate         = "Percent per Annum" // rates and yields: changes in basis points
	UnitsPercent      = "Percent"
	UnitsIndex        = "Index"
	UnitsBalance      = "Balance"          // survey balances, in points
	UnitsPoints       = "Index Points"     // a quoted index: VDAX-NEW
	UnitsMillionsEUR  = "Millions of Euro" // shown in billions
	UnitsPercentOfGDP = "Percent of GDP"
	UnitsThousands    = "Thousands of Persons" // employment
)

// SourceUnits lists the values of the units key.
var SourceUnits = []string{UnitsRate, UnitsPercent, UnitsIndex, UnitsBalance, UnitsPoints, UnitsMillionsEUR, UnitsPercentOfGDP, UnitsThousands, UnitsFX}

// sourceFrequencies are the values of the frequency key (the short FRED
// codes, lower case).
var sourceFrequencies = []string{"d", "w", "m", "q"}

// NumericSource is a section of series read from one source: ecb,
// bundesbank, eurostat, stoxx, zew.
type NumericSource struct {
	Enabled  bool           `yaml:"enabled"`
	Endpoint string         `yaml:"endpoint"`
	PollCron string         `yaml:"poll_cron"` // config timezone
	Sheet    string         `yaml:"sheet"`     // zew: the sheet of the workbook
	Series   []SourceSeries `yaml:"series"`
}

// SourceSeries is one series of a NumericSource or of manual input. ID is
// the name in the store and in the tables; Key is what the source calls it:
// a flow and series key (ecb, bundesbank), a dataset with its filters
// (eurostat), a file (stoxx), a column header (zew).
type SourceSeries struct {
	ID         string   `yaml:"id"`
	Key        string   `yaml:"key"`
	Agent      string   `yaml:"agent"`
	Label      string   `yaml:"label"`
	Frequency  string   `yaml:"frequency"`
	Units      string   `yaml:"units"`
	Transforms []string `yaml:"transforms"`
	FlagIfGt   *float64 `yaml:"flag_if_gt"`
	Quality    string   `yaml:"quality"`
	// Forecast: the source publishes values ahead of today (the ECB wage
	// tracker). Reports take the value at the period end as the fact and
	// name the last point as the forecast; dashboard charts split them.
	Forecast bool `yaml:"forecast"`
	// ReleaseDates are the publication days of a manual series (YYYY-MM-DD):
	// the source has no calendar to read, so staleness counts from them.
	ReleaseDates []string `yaml:"release_dates"`

	Releases []time.Time `yaml:"-"` // parsed ReleaseDates, ascending
}

// Manual is the section of series typed in with `macroctl input`.
type Manual struct {
	Series   []SourceSeries  `yaml:"series"`
	Reminder *ManualReminder `yaml:"reminder"` // nil: no Telegram reminder (phase 14c)
}

// ManualReminder is the bot's reminder to type in monthly manual series
// (Ifo; phase 14c, D29). On weekdays at Time it asks for the values of the
// current month while one of them is missing: from the first release day
// of the month in release_dates, or, when the month has none, from FromDay.
// The answer is the numbers in the order of Series, in reply to the
// reminder or as "<keyword> <numbers> [YYYY-MM]" (keyword: Name in lower case).
type ManualReminder struct {
	Name     string   `yaml:"name"`     // "Ifo": in the message and the keyword
	Series   []string `yaml:"series"`   // ids of manual.series, monthly
	FromDay  int      `yaml:"from_day"` // 1–28; default 20
	Time     string   `yaml:"time"`     // HH:MM; default 10:05
	Timezone string   `yaml:"timezone"` // default: the config timezone
	Hint     string   `yaml:"hint"`     // where the values are published, shown in the reminder

	Clock    ClockTime      `yaml:"-"`
	Location *time.Location `yaml:"-"`
}

// Keyword is the first word of a reply typed without the reminder.
func (r *ManualReminder) Keyword() string { return strings.ToLower(r.Name) }

// Cron is the job spec of the reminder: weekdays at Time in its timezone.
func (r *ManualReminder) Cron() string {
	return fmt.Sprintf("CRON_TZ=%s %d %d * * 1-5", r.Location.String(), r.Clock.Minute, r.Clock.Hour)
}

func (c *Config) validateReminder() error {
	r := c.Manual.Reminder
	if r == nil {
		return nil
	}
	var errs []error
	if r.Name == "" || strings.ContainsAny(r.Name, " \t") {
		errs = append(errs, fmt.Errorf("manual.reminder.name %q: want one word", r.Name))
	}
	if len(r.Series) == 0 {
		errs = append(errs, errors.New("manual.reminder.series is empty"))
	}
	for _, id := range r.Series {
		s, ok := c.ManualSeries(id)
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("manual.reminder.series %s: not in manual.series", id))
		case s.Frequency != "m":
			errs = append(errs, fmt.Errorf("manual.reminder.series %s: frequency %s, want m", id, s.Frequency))
		}
	}
	if r.FromDay == 0 {
		r.FromDay = 20
	}
	if r.FromDay < 1 || r.FromDay > 28 {
		errs = append(errs, fmt.Errorf("manual.reminder.from_day %d: want 1–28", r.FromDay))
	}
	if r.Time == "" {
		r.Time = "10:05"
	}
	var err error
	if r.Clock, err = parseClock(r.Time); err != nil {
		errs = append(errs, fmt.Errorf("manual.reminder.%w", err))
	}
	r.Location = c.Location
	if r.Timezone != "" {
		if r.Location, err = time.LoadLocation(r.Timezone); err != nil {
			errs = append(errs, fmt.Errorf("manual.reminder.timezone %q: %w", r.Timezone, err))
		}
	}
	if r.Location == nil {
		r.Location = time.UTC
	}
	if _, err := cron.ParseStandard(r.Cron()); err != nil {
		errs = append(errs, fmt.Errorf("manual.reminder: %w", err))
	}
	return errors.Join(errs...)
}

// NamedSource is a numeric source section with its name.
type NamedSource struct {
	Name string
	*NumericSource
}

// NumericSources are the sections of series, enabled or not, in a fixed
// order.
func (c *Config) NumericSources() []NamedSource {
	return []NamedSource{
		{SourceECB, &c.ECB}, {SourceBundesbank, &c.Bundesbank}, {SourceEurostat, &c.Eurostat},
		{SourceSTOXX, &c.STOXX}, {SourceZEW, &c.ZEW},
	}
}

// NumericSourceByName finds an enabled section.
func (c *Config) NumericSourceByName(name string) (*NumericSource, bool) {
	for _, s := range c.NumericSources() {
		if s.Name == name && s.Enabled {
			return s.NumericSource, true
		}
	}
	return nil, false
}

// ManualSeries finds a manual series by id.
func (c *Config) ManualSeries(id string) (SourceSeries, bool) {
	for _, s := range c.Manual.Series {
		if s.ID == id {
			return s, true
		}
	}
	return SourceSeries{}, false
}

// ConfigReleaseDays are the publication days set in the config, by series
// id; the calendar of the store knows nothing of them.
func (c *Config) ConfigReleaseDays() map[string][]time.Time {
	out := map[string][]time.Time{}
	for _, s := range c.Manual.Series {
		if len(s.Releases) > 0 {
			out[s.ID] = s.Releases
		}
	}
	return out
}

func (c *Config) validateSources() error {
	var errs []error
	for _, src := range c.NumericSources() {
		if !src.Enabled {
			continue
		}
		if !strings.HasPrefix(src.Endpoint, "https://") {
			errs = append(errs, fmt.Errorf("%s.endpoint %q: want an https URL", src.Name, src.Endpoint))
		}
		if _, err := cron.ParseStandard(src.PollCron); err != nil {
			errs = append(errs, fmt.Errorf("%s.poll_cron %q: %w", src.Name, src.PollCron, err))
		}
		if src.Name == SourceZEW && src.Sheet == "" {
			errs = append(errs, errors.New("zew.sheet is empty"))
		}
		if len(src.Series) == 0 {
			errs = append(errs, fmt.Errorf("%s.series is empty", src.Name))
		}
		errs = append(errs, c.validateSourceSeries(src.Name, src.Series, true))
	}
	errs = append(errs, c.validateSourceSeries(SourceManual, c.Manual.Series, false), c.validateReminder())
	return errors.Join(errs...)
}

func (c *Config) validateSourceSeries(name string, series []SourceSeries, needKey bool) error {
	var errs []error
	seen := map[string]bool{}
	for i := range series {
		s := &series[i]
		where := fmt.Sprintf("%s.series[%d] %s", name, i, s.ID)
		if s.ID == "" {
			errs = append(errs, fmt.Errorf("%s.series[%d]: empty id", name, i))
		}
		if strings.Contains(s.ID, "/") {
			errs = append(errs, fmt.Errorf("%s: id must not contain /", where))
		}
		if seen[s.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id", where))
		}
		seen[s.ID] = true
		if needKey && s.Key == "" {
			errs = append(errs, fmt.Errorf("%s: key is required", where))
		}
		if !slices.Contains(sourceFrequencies, s.Frequency) {
			errs = append(errs, fmt.Errorf("%s: frequency %q: want one of %s", where, s.Frequency, strings.Join(sourceFrequencies, ", ")))
		}
		if !slices.Contains(SourceUnits, s.Units) {
			errs = append(errs, fmt.Errorf("%s: units %q: want one of %s", where, s.Units, strings.Join(SourceUnits, ", ")))
		}
		s.Releases = nil
		for _, d := range s.ReleaseDates {
			t, err := time.Parse(time.DateOnly, d)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: release_dates: %w", where, err))
				continue
			}
			s.Releases = append(s.Releases, t)
		}
		slices.SortFunc(s.Releases, func(a, b time.Time) int { return a.Compare(b) })
		errs = append(errs, c.checkAgent(where, s.Agent), checkTransforms(where, s.Transforms))
	}
	return errors.Join(errs...)
}
