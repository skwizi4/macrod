package fedtexts

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"macro-collector/internal/model"
	"macro-collector/internal/sources"
)

// Event series published by the Fed calendar (releases.source / name).
const (
	SourceFOMC      = "fomc"
	SourceBeigeBook = "beige_book"
	NameDecision    = "FOMC decision"
	NameMinutes     = "FOMC minutes"
	NameBeigeBook   = "Beige Book"
)

// Keys are the (source, name) pairs Calendar produces, for syncing.
var Keys = [][2]string{{SourceFOMC, NameDecision}, {SourceFOMC, NameMinutes}, {SourceBeigeBook, NameBeigeBook}}

// Release times in America/New_York (LOOKUP 2026-10-24: statement "For
// release at 2:00 p.m."; Fed calendar lists minutes and Beige Book at 2:00 p.m.).
const (
	releaseHour = 14
	// Minutes come out three weeks after the decision (all of 2021–2026);
	// the page shows the real date only once they are out.
	minutesLag = 21 // days
)

// Meeting is one FOMC meeting from the calendar page.
type Meeting struct {
	Start, End time.Time // calendar days, UTC midnight
	SEP        bool      // with a Summary of Economic Projections ("*")
	Minutes    time.Time // release date once published, else zero
}

var (
	meetingDays = regexp.MustCompile(`^(\d{1,2})(?:-(\d{1,2}))?(\*)?$`)
	released    = regexp.MustCompile(`Released ([A-Z][a-z]+ \d{1,2}, \d{4})`)
	yearHeading = regexp.MustCompile(`^(\d{4}) FOMC Meetings$`)
)

// ParseFOMCCalendar reads meetings from federalreserve.gov/monetarypolicy/fomccalendars.htm.
// Notation votes are not meetings and are skipped; any other unexpected
// row is an error, so a page redesign does not silently empty the calendar.
func ParseFOMCCalendar(page []byte) ([]Meeting, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("fomc calendar parse: %w", err)
	}
	var out []Meeting
	var perr error
	doc.Find("div.panel").EachWithBreak(func(_ int, panel *goquery.Selection) bool {
		m := yearHeading.FindStringSubmatch(strings.TrimSpace(panel.Find(".panel-heading").First().Text()))
		if m == nil {
			return true
		}
		year, _ := strconv.Atoi(m[1])
		panel.Find("div.fomc-meeting").EachWithBreak(func(_ int, row *goquery.Selection) bool {
			month := strings.TrimSpace(row.Find(".fomc-meeting__month").Text())
			days := strings.TrimSpace(row.Find(".fomc-meeting__date").Text())
			if strings.Contains(days, "notation vote") {
				return true
			}
			mt, err := parseMeeting(year, month, days)
			if err != nil {
				perr = err
				return false
			}
			if r := released.FindStringSubmatch(row.Find(".fomc-meeting__minutes").Text()); r != nil {
				if mt.Minutes, err = time.Parse("January 2, 2006", r[1]); err != nil {
					perr = fmt.Errorf("fomc calendar %d %s %s minutes %q: %w", year, month, days, r[1], err)
					return false
				}
			}
			out = append(out, mt)
			return true
		})
		return perr == nil
	})
	if perr != nil {
		return nil, perr
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("fomc calendar parse: no meetings found")
	}
	return out, nil
}

// parseMeeting reads "October" + "27-28", "Apr/May" + "30-1" or "December" + "8-9*".
func parseMeeting(year int, month, days string) (Meeting, error) {
	where := fmt.Sprintf("fomc calendar %d %q %q", year, month, days)
	d := meetingDays.FindStringSubmatch(days)
	if d == nil {
		return Meeting{}, fmt.Errorf("%s: unexpected day format", where)
	}
	first, last, _ := strings.Cut(month, "/")
	m1, err := parseMonth(first)
	if err != nil {
		return Meeting{}, fmt.Errorf("%s: %w", where, err)
	}
	m2 := m1
	if last != "" {
		if m2, err = parseMonth(last); err != nil {
			return Meeting{}, fmt.Errorf("%s: %w", where, err)
		}
	}
	startDay, _ := strconv.Atoi(d[1])
	endDay := startDay
	if d[2] != "" {
		endDay, _ = strconv.Atoi(d[2])
	}
	if endDay < startDay && m2 == m1 {
		m2 = m1 + 1 // "31-1" under a single month name
	}
	start := time.Date(year, m1, startDay, 0, 0, 0, 0, time.UTC)
	endYear := year
	if m2 < m1 {
		endYear++ // "Dec/Jan"
	}
	end := time.Date(endYear, m2, endDay, 0, 0, 0, 0, time.UTC)
	if start.Day() != startDay || end.Day() != endDay || end.Before(start) || end.Sub(start) > 72*time.Hour {
		return Meeting{}, fmt.Errorf("%s: invalid dates", where)
	}
	return Meeting{Start: start, End: end, SEP: d[3] == "*"}, nil
}

func parseMonth(s string) (time.Month, error) {
	for _, layout := range []string{"January", "Jan"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Month(), nil
		}
	}
	return 0, fmt.Errorf("unknown month %q", s)
}

// ParseBeigeBook reads release dates from
// federalreserve.gov/monetarypolicy/publications/beige-book-default.htm:
// per-year tables whose cells start with "January 14" (past ones add links).
func ParseBeigeBook(page []byte) ([]time.Time, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("beige book parse: %w", err)
	}
	var out []time.Time
	var perr error
	doc.Find("table").EachWithBreak(func(_ int, table *goquery.Selection) bool {
		year, err := strconv.Atoi(strings.TrimSpace(table.Find("th#year").Text()))
		if err != nil {
			return true // not a schedule table
		}
		table.Find("tbody td").EachWithBreak(func(_ int, td *goquery.Selection) bool {
			text, _, _ := strings.Cut(strings.TrimSpace(td.Text()), ":")
			d, err := time.Parse("January 2 2006", strings.TrimSpace(text)+" "+strconv.Itoa(year))
			if err != nil {
				perr = fmt.Errorf("beige book %d %q: %w", year, td.Text(), err)
				return false
			}
			out = append(out, d)
			return true
		})
		return perr == nil
	})
	if perr != nil {
		return nil, perr
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("beige book parse: no dates found")
	}
	return out, nil
}

// Calendar lists FOMC decisions, minutes and Beige Books.
type Calendar struct {
	Client       *Client
	FOMCURL      string
	BeigeBookURL string
	Location     *time.Location // America/New_York
}

var _ sources.CalendarSource = (*Calendar)(nil)

// Name implements sources.CalendarSource.
func (c *Calendar) Name() string { return "fed" }

// Upcoming implements sources.CalendarSource.
func (c *Calendar) Upcoming(ctx context.Context, from, to time.Time) ([]model.ReleaseEvent, error) {
	page, err := c.Client.Get(ctx, c.FOMCURL)
	if err != nil {
		return nil, fmt.Errorf("fed calendar: %w", err)
	}
	meetings, err := ParseFOMCCalendar(page)
	if err != nil {
		return nil, err
	}
	page, err = c.Client.Get(ctx, c.BeigeBookURL)
	if err != nil {
		return nil, fmt.Errorf("fed calendar: %w", err)
	}
	books, err := ParseBeigeBook(page)
	if err != nil {
		return nil, err
	}
	return Events(meetings, books, c.Location, from, to), nil
}

// Events turns meetings and Beige Book dates into events scheduled in [from, to).
func Events(meetings []Meeting, beigeBooks []time.Time, loc *time.Location, from, to time.Time) []model.ReleaseEvent {
	at := func(day time.Time) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), releaseHour, 0, 0, 0, loc).UTC()
	}
	var out []model.ReleaseEvent
	add := func(e model.ReleaseEvent) {
		if !e.ScheduledAt.Before(from) && e.ScheduledAt.Before(to) {
			out = append(out, e)
		}
	}
	for _, m := range meetings {
		rel := model.Related{Meeting: meetingID(m), SEP: m.SEP}
		add(model.ReleaseEvent{Source: SourceFOMC, Name: NameDecision, ScheduledAt: at(m.End), Importance: "high", Related: rel})
		minutes := m.Minutes
		if minutes.IsZero() {
			minutes = m.End.AddDate(0, 0, minutesLag)
		}
		add(model.ReleaseEvent{Source: SourceFOMC, Name: NameMinutes, ScheduledAt: at(minutes), Importance: "high",
			Related: model.Related{Meeting: rel.Meeting}})
	}
	for _, d := range beigeBooks {
		add(model.ReleaseEvent{Source: SourceBeigeBook, Name: NameBeigeBook, ScheduledAt: at(d), Importance: "medium"})
	}
	return out
}

// meetingID is "2026-10-27/28", or "2026-04-30/05-01" across months.
func meetingID(m Meeting) string {
	if m.Start.Month() == m.End.Month() {
		return m.Start.Format(time.DateOnly) + "/" + m.End.Format("02")
	}
	return m.Start.Format(time.DateOnly) + "/" + m.End.Format("01-02")
}
