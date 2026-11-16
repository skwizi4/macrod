// Package relcal reads release calendars that publish dates as pages or
// iCalendar files (spec 8.1, phase 11; LOOKUP 2026-11-11): the statistical
// calendar of the ECB and the Eurostat release calendar (.ics, dates only).
// The parsers return plain entries; which of them become events, with what
// name, time, importance and series, is the config's (internal/collect).
package relcal

import (
	"bufio"
	"bytes"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
)

// Entry is one dated release of a calendar.
type Entry struct {
	Title     string    // as the calendar names it
	Dataset   string    // ECB: the dataset code ("HICP")
	Period    string    // reference period, as written ("Oct-2026", "November 2026")
	Day       time.Time // calendar day, UTC midnight
	Clock     string    // "HH:MM" local to the calendar, "" if it states none
	Tentative bool      // ECB: "Tentative"
}

var (
	tagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// text strips tags and entities and folds white space.
func text(s string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(html.UnescapeString(tagRe.ReplaceAllString(s, " ")), " "))
}

var (
	ecbItemRe    = regexp.MustCompile(`(?s)<dt>(.*?)</dt>\s*<dd>(.*?)</dd>`)
	ecbWhenRe    = regexp.MustCompile(`^(\d{2})/(\d{2})/(\d{4})(?: (\d{2}:\d{2}))?`)
	ecbDatasetRe = regexp.MustCompile(`^(.*?)\s*\(Dataset:\s*([A-Z0-9_]+)\)`)
	ecbPeriodRe  = regexp.MustCompile(`Reference period:\s*([^<\n]+)`)
)

// ParseECBStats reads the ECB statistical calendar page: <dt>DD/MM/YYYY
// HH:MM [Tentative]</dt><dd>Title (Dataset: X)<br>Reference period: …</dd>,
// times local to Frankfurt. A page without entries is an error (layout
// changed).
func ParseECBStats(page []byte) ([]Entry, error) {
	var out []Entry
	for _, m := range ecbItemRe.FindAllSubmatch(page, -1) {
		when := text(string(m[1]))
		w := ecbWhenRe.FindStringSubmatch(when)
		if w == nil {
			continue // not a calendar entry
		}
		day, err := time.Parse("2006-01-02", w[3]+"-"+w[2]+"-"+w[1])
		if err != nil {
			return nil, fmt.Errorf("ecb statistical calendar: date %q: %w", when, err)
		}
		body := string(m[2])
		first, _, _ := strings.Cut(body, "<br")
		d := ecbDatasetRe.FindStringSubmatch(text(first))
		if d == nil {
			continue // an entry without a dataset (none seen)
		}
		e := Entry{Title: d[1], Dataset: d[2], Day: day, Clock: w[4], Tentative: strings.Contains(when, "Tentative")}
		if p := ecbPeriodRe.FindStringSubmatch(body); p != nil {
			e.Period = text(p[1])
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ecb statistical calendar: no entries (page layout changed?)")
	}
	return out, nil
}

// ParseICS reads the VEVENTs of an iCalendar file: SUMMARY and a DTSTART
// that is a date (VALUE=DATE) or a date-time (its day and clock are kept
// as written). Folded lines are unfolded; escaped commas are unescaped.
func ParseICS(data []byte) ([]Entry, error) {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), "\r")
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
			continue
		}
		lines = append(lines, l)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ics: %w", err)
	}
	var out []Entry
	var cur *Entry
	for _, l := range lines {
		name, value, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		prop, params, _ := strings.Cut(name, ";")
		switch prop {
		case "BEGIN":
			if value == "VEVENT" {
				cur = &Entry{}
			}
		case "END":
			if value == "VEVENT" && cur != nil {
				if cur.Title == "" || cur.Day.IsZero() {
					return nil, fmt.Errorf("ics: event without SUMMARY or DTSTART")
				}
				out = append(out, *cur)
				cur = nil
			}
		case "SUMMARY":
			if cur != nil {
				cur.Title = strings.NewReplacer(`\,`, ",", `\;`, ";", `\n`, " ", `\\`, `\`).Replace(value)
			}
		case "DTSTART":
			if cur == nil {
				continue
			}
			if len(value) < 8 {
				return nil, fmt.Errorf("ics: DTSTART %q", value)
			}
			day, err := time.Parse("20060102", value[:8])
			if err != nil {
				return nil, fmt.Errorf("ics: DTSTART %q: %w", value, err)
			}
			cur.Day = day
			if !strings.Contains(params, "VALUE=DATE") && len(value) >= 13 && value[8] == 'T' {
				cur.Clock = value[9:11] + ":" + value[11:13]
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ics: no events")
	}
	return out, nil
}
