// Package ircal reads report dates from an issuer's official financial
// calendar page (phase 14c, D29). No EU Micro issuer publishes a feed
// (LOOKUP 2026-11-13), so the page is read as data: JSON objects in an
// HTML attribute (Deutsche Telekom) or text lines where an event starts
// with a date (Siemens, Siemens Energy, Rheinmetall). Only events whose
// title matches the config are kept, named by its period; none kept is
// an error, so a changed layout is reported, never guessed around.
package ircal

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/config"
)

// Event is a report date of the calendar.
type Event struct {
	Date   time.Time // UTC midnight of the day
	Period string    // "Q1 FY2027", "FY 2026"
	Title  string    // the event text it was matched in

	lines []string // text kind: the event's lines, each tried alone too
}

// maxTitle caps the stored event text (a line may run into page code).
const maxTitle = 120

// Parse returns the matched events of the page, by date, one per period
// (the earliest date when a period is listed twice).
func Parse(page []byte, cal config.IssuerCalendar) ([]Event, error) {
	var raw []Event // Title only
	switch cal.Kind {
	case config.CalendarJSONAttr:
		raw = jsonAttr(string(page), cal.Attr)
	case config.CalendarText:
		raw = textEvents(Lines(page), cal.Slash, cal.Lines)
	default:
		return nil, fmt.Errorf("calendar kind %q", cal.Kind)
	}
	seen := map[string]bool{}
	var out []Event
	for _, e := range raw {
		// A line alone first (its text is the title), then the joined text.
		p, ok := "", false
		for _, l := range e.lines {
			if p, ok = period(l, cal.Events); ok {
				e.Title = l
				break
			}
		}
		if !ok {
			p, ok = period(e.Title, cal.Events)
		}
		if !ok {
			continue
		}
		if r := []rune(e.Title); len(r) > maxTitle {
			e.Title = string(r[:maxTitle-1]) + "…"
		}
		e.lines = nil
		e.Period = p
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(a, b Event) int { return a.Date.Compare(b.Date) })
	out = slices.DeleteFunc(out, func(e Event) bool {
		if seen[e.Period] {
			return true
		}
		seen[e.Period] = true
		return false
	})
	if len(out) == 0 {
		return nil, fmt.Errorf("calendar %s: no report event among %d dated entries (layout changed?)", cal.URL, len(raw))
	}
	return out, nil
}

func period(title string, ms []config.CalendarMatch) (string, bool) {
	for _, m := range ms {
		sub := m.Re.FindStringSubmatch(title)
		if sub == nil {
			continue
		}
		p := m.Period
		for _, g := range []string{"q", "year"} {
			if i := m.Re.SubexpIndex(g); i >= 0 {
				p = strings.ReplaceAll(p, "{"+g+"}", sub[i])
			}
		}
		return p, true
	}
	return "", false
}

func jsonAttr(page, attr string) []Event {
	re := regexp.MustCompile(regexp.QuoteMeta(attr) + `\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	var out []Event
	for _, m := range re.FindAllStringSubmatch(page, -1) {
		v := m[1] + m[2]
		var o struct {
			Title     string `json:"title"`
			StartDate string `json:"startDate"`
		}
		if json.Unmarshal([]byte(html.UnescapeString(v)), &o) != nil || len(o.StartDate) < 10 {
			continue
		}
		d, err := time.Parse(time.DateOnly, o.StartDate[:10])
		if err != nil {
			continue
		}
		out = append(out, Event{Date: d, Title: clean(o.Title)})
	}
	return out
}

var (
	blockTagRe = regexp.MustCompile(`(?i)<\s*/?\s*(tr|td|th|p|div|br|li|ul|ol|h[1-6]|table|tbody|thead|section|article|a|header|footer|time)\b[^>]*>`)
	tagRe      = regexp.MustCompile(`<[^>]*>`)
	spaceRe    = regexp.MustCompile(`[\s\x{00a0}]+`)
)

// Lines is the page as text: entities unescaped (tables embedded escaped
// in an attribute or a script are unescaped too), a line per block element.
func Lines(page []byte) []string {
	s := string(page)
	for range 3 {
		u := html.UnescapeString(s)
		u = strings.NewReplacer(`\"`, `"`, `\r\n`, "\n", `\n`, "\n", `\/`, "/").Replace(u)
		if u == s {
			break
		}
		s = u
	}
	s = blockTagRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, "")
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if l = clean(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func clean(s string) string { return strings.TrimSpace(spaceRe.ReplaceAllString(s, " ")) }

var (
	months = map[string]time.Month{"jan": 1, "january": 1, "feb": 2, "february": 2, "mar": 3, "march": 3, "apr": 4, "april": 4, "may": 5,
		"jun": 6, "june": 6, "jul": 7, "july": 7, "aug": 8, "august": 8, "sep": 9, "sept": 9, "september": 9, "oct": 10, "october": 10,
		"nov": 11, "november": 11, "dec": 12, "december": 12}
	monthDateRe = regexp.MustCompile(`(?i)^([a-z]{3,9})\.? (\d{1,2})(?:\s*[-–]\s*\d{1,2})?,? (\d{4})\b`)
	numDateRe   = regexp.MustCompile(`^(\d{1,2})[/.](\d{1,2})[/.](\d{4})\b`)
	isoDateRe   = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:\b|T)`)
)

// lineDate reads a date at the start of a line; rest is the line after it.
func lineDate(l, slash string) (time.Time, string, bool) {
	at := func(y, m, d int, n int) (time.Time, string, bool) {
		t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
		if m < 1 || m > 12 || d < 1 || t.Day() != d {
			return time.Time{}, "", false
		}
		return t, strings.TrimSpace(l[n:]), true
	}
	if m := monthDateRe.FindStringSubmatch(l); m != nil {
		mo, ok := months[strings.ToLower(m[1])]
		if !ok {
			return time.Time{}, "", false
		}
		d, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		return at(y, int(mo), d, len(m[0]))
	}
	if m := numDateRe.FindStringSubmatch(l); m != nil {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		if slash == "mdy" {
			a, b = b, a
		}
		return at(y, b, a, len(m[0]))
	}
	if m := isoDateRe.FindStringSubmatch(l); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		return at(y, mo, d, len(m[0]))
	}
	return time.Time{}, "", false
}

// textEvents: an event is a line starting with a date, its text the rest
// of that line and up to n following lines before the next date line.
func textEvents(lines []string, slash string, n int) []Event {
	var out []Event
	for i, l := range lines {
		d, rest, ok := lineDate(l, slash)
		if !ok {
			continue
		}
		parts := []string{rest}
		for j := i + 1; j < len(lines) && j <= i+n; j++ {
			if _, _, isDate := lineDate(lines[j], slash); isDate {
				break
			}
			parts = append(parts, lines[j])
		}
		out = append(out, Event{Date: d, Title: clean(strings.Join(parts, " ")), lines: parts})
	}
	return out
}
