package ecbtexts

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/htmltext"
)

// Calendar events (releases.source, name) of Governing Council monetary
// policy meetings.
const (
	SourceMeeting       = "ecb_gc"
	NameDecision        = "ECB decision"
	NamePressConference = "ECB press conference"
)

// Keys are the event series the calendar owns.
var Keys = [][2]string{{SourceMeeting, NameDecision}, {SourceMeeting, NamePressConference}}

// ParseCalendar returns the days of monetary policy meetings followed by a
// press conference (the decision day) from the meeting schedule page. The
// place is not checked: once a year the meeting is held outside Frankfurt.
func ParseCalendar(b []byte) ([]time.Time, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("parse ecb calendar: %w", err)
	}
	var out []time.Time
	var perr error
	doc.Find("dt").Each(func(_ int, dt *goquery.Selection) {
		what := strings.ToLower(htmltext.Clean(dt.NextFiltered("dd").Text()))
		if !strings.Contains(what, "monetary policy meeting") || strings.Contains(what, "non-monetary") ||
			!strings.Contains(what, "press conference") {
			return
		}
		day, err := time.Parse("02/01/2006", htmltext.Clean(dt.Text()))
		if err != nil {
			perr = fmt.Errorf("parse ecb calendar date %q: %w", htmltext.Clean(dt.Text()), err)
			return
		}
		out = append(out, day)
	})
	if perr != nil {
		return nil, perr
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("parse ecb calendar: no monetary policy meetings (page layout changed?)")
	}
	return out, nil
}

// Calendar lists the decision and press conference of each meeting.
type Calendar struct {
	Client   *Client
	URL      string
	Location *time.Location // Europe/Berlin
	// Decision and PressConference are the times after midnight.
	Decision, PressConference time.Duration
}

var _ sources.CalendarSource = (*Calendar)(nil)

// Name implements sources.CalendarSource.
// CalendarName is the calendar's name (fetch_state "calendar:ecb_meetings").
const CalendarName = "ecb_meetings"

func (c *Calendar) Name() string { return CalendarName }

// Upcoming implements sources.CalendarSource.
func (c *Calendar) Upcoming(ctx context.Context, from, to time.Time) ([]model.ReleaseEvent, error) {
	b, err := c.Client.Get(ctx, c.URL)
	if err != nil {
		return nil, fmt.Errorf("ecb calendar: %w", err)
	}
	days, err := ParseCalendar(b)
	if err != nil {
		return nil, err
	}
	return Events(days, c.Location, c.Decision, c.PressConference, from, to), nil
}

// Events turns meeting days into events scheduled in [from, to).
func Events(days []time.Time, loc *time.Location, decision, conference time.Duration, from, to time.Time) []model.ReleaseEvent {
	var out []model.ReleaseEvent
	for _, d := range days {
		rel := model.Related{Meeting: d.Format(time.DateOnly)}
		for _, e := range []model.ReleaseEvent{
			{Source: SourceMeeting, Name: NameDecision, ScheduledAt: At(d, decision, loc), Importance: "high", Related: rel},
			{Source: SourceMeeting, Name: NamePressConference, ScheduledAt: At(d, conference, loc), Importance: "high", Related: rel},
		} {
			if !e.ScheduledAt.Before(from) && e.ScheduledAt.Before(to) {
				out = append(out, e)
			}
		}
	}
	return out
}

// At is the instant of the wall-clock time since midnight on day's
// calendar date in loc, in UTC.
func At(day time.Time, since time.Duration, loc *time.Location) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d, 0, int(since/time.Minute), 0, 0, loc).UTC()
}
