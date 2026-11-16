package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Kinds of an issuer's financial calendar page (phase 14c, D29; LOOKUP
// 2026-11-13: no issuer of EU Micro publishes an .ics/RSS/JSON feed).
const (
	// CalendarJSONAttr: JSON objects {title, startDate} in an HTML
	// attribute (Deutsche Telekom: data-event-data).
	CalendarJSONAttr = "json_attr"
	// CalendarText: the page as text lines; an event is a line starting
	// with a date and the next lines (Siemens, Siemens Energy tables,
	// Rheinmetall "dd/mm/yyyy | Event" cards).
	CalendarText = "text"
)

// IssuerCalendar is an issuer's official financial calendar page read for
// report dates. Only events matching one of Events are kept; zero kept
// events is an error (the layout changed), recorded in fetch_state.
type IssuerCalendar struct {
	URL    string          `yaml:"url"`
	Kind   string          `yaml:"kind"`
	Attr   string          `yaml:"attr"`  // json_attr: default data-event-data
	Slash  string          `yaml:"slash"` // text: numeric dates are dmy (default) or mdy
	Lines  int             `yaml:"lines"` // text: lines of an event after its date line; default 4
	Events []CalendarMatch `yaml:"events"`
}

// CalendarMatch maps an event title (RE2, named groups q and year) to the
// period name of report_dates: "Q{q} FY{year}", "FY {year}".
type CalendarMatch struct {
	Match  string `yaml:"match"`
	Period string `yaml:"period"`

	Re *regexp.Regexp `yaml:"-"`
}

var periodGroupRe = regexp.MustCompile(`\{(q|year)\}`)

func (c *IssuerCalendar) validate(where string) error {
	var errs []error
	if err := checkURL(where+".url", c.URL); err != nil {
		errs = append(errs, err)
	}
	switch c.Kind {
	case CalendarJSONAttr:
		if c.Attr == "" {
			c.Attr = "data-event-data"
		}
	case CalendarText:
		if c.Slash == "" {
			c.Slash = "dmy"
		}
		if c.Slash != "dmy" && c.Slash != "mdy" {
			errs = append(errs, fmt.Errorf("%s.slash %q: want dmy or mdy", where, c.Slash))
		}
		if c.Lines == 0 {
			c.Lines = 4
		}
		if c.Lines < 1 || c.Lines > 20 {
			errs = append(errs, fmt.Errorf("%s.lines %d: want 1–20", where, c.Lines))
		}
	default:
		errs = append(errs, fmt.Errorf("%s.kind %q: want %s or %s", where, c.Kind, CalendarJSONAttr, CalendarText))
	}
	if len(c.Events) == 0 {
		errs = append(errs, errors.New(where+".events is empty"))
	}
	for i := range c.Events {
		e := &c.Events[i]
		w := fmt.Sprintf("%s.events[%d]", where, i)
		re, err := regexp.Compile(e.Match)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s.match: %w", w, err))
			continue
		}
		e.Re = re
		if e.Period == "" {
			errs = append(errs, fmt.Errorf("%s.period is empty", w))
		}
		for _, g := range periodGroupRe.FindAllStringSubmatch(e.Period, -1) {
			if re.SubexpIndex(g[1]) < 0 {
				errs = append(errs, fmt.Errorf("%s: period %q uses {%s}, match has no group (?P<%s>...)", w, e.Period, g[1], g[1]))
			}
		}
		if strings.Contains(periodGroupRe.ReplaceAllString(e.Period, ""), "{") {
			errs = append(errs, fmt.Errorf("%s: period %q: only {q} and {year}", w, e.Period))
		}
	}
	return errors.Join(errs...)
}
