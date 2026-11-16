package ircal

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"macro-collector/internal/config"
)

func cal(kind string, ms ...string) config.IssuerCalendar {
	c := config.IssuerCalendar{URL: "https://ir.example/cal", Kind: kind, Attr: "data-event-data", Slash: "dmy", Lines: 4}
	for i := 0; i+1 < len(ms); i += 2 {
		c.Events = append(c.Events, config.CalendarMatch{Match: ms[i], Period: ms[i+1], Re: regexp.MustCompile(ms[i])})
	}
	return c
}

func dump(es []Event) string {
	var b []string
	for _, e := range es {
		b = append(b, e.Date.Format("2006-01-02")+" "+e.Period)
	}
	return strings.Join(b, "; ")
}

// Recorded pages (plain GET, 2026-11-14); the matches are those of the
// preset eu-equities.yaml.
func TestParseRecordedPages(t *testing.T) {
	for _, tt := range []struct {
		file string
		cal  config.IssuerCalendar
		want string
	}{
		{"telekom", cal(config.CalendarJSONAttr,
			`(?i)^Q4 (?P<year>\d{4}) financial results$`, "FY {year}",
			`(?i)^(?:publication of )?Q(?P<q>[1-3]) (?P<year>\d{4}) financial results$`, "Q{q} {year}"),
			"2027-02-25 FY 2026; 2027-05-12 Q1 2027"},
		{"siemens", cal(config.CalendarText, `(?i)\b(?P<q>[1-4])(?:st|nd|rd|th) quarter results fiscal year (?P<year>\d{4})\b`, "Q{q} FY{year}"),
			"2026-11-12 Q4 FY2026; 2027-02-11 Q1 FY2027; 2027-05-13 Q2 FY2027; 2027-08-05 Q3 FY2027; 2027-11-11 Q4 FY2027"},
		// "Q4 FY2026 Pre-Close Call" of the publications list is not a report date: the match is the whole line.
		{"siemens-energy", cal(config.CalendarText, `^Q(?P<q>[1-4]) FY(?P<year>\d{4})$`, "Q{q} FY{year}"),
			"2027-02-11 Q1 FY2027; 2027-05-13 Q2 FY2027; 2027-08-05 Q3 FY2027; 2027-11-11 Q4 FY2027"},
		{"rheinmetall", cal(config.CalendarText,
			`Publication of quarterly results Q(?P<q>[1-3]) (?P<year>\d{4})\b`, "Q{q} {year}",
			`Publication of the Annual Report (?P<year>\d{4})\b`, "FY {year}"),
			"2026-03-11 FY 2025; 2026-05-07 Q1 2026; 2026-08-06 Q2 2026; 2026-11-05 Q3 2026; 2027-03-11 FY 2026; 2027-05-05 Q1 2027; 2027-08-05 Q2 2027; 2027-11-04 Q3 2027"},
	} {
		page, err := os.ReadFile("../../../testdata/ircal/" + tt.file + "-2026-11-14.html")
		if err != nil {
			t.Fatal(err)
		}
		got, err := Parse(page, tt.cal)
		if err != nil || dump(got) != tt.want {
			t.Errorf("%s:\n got %s (%v)\nwant %s", tt.file, dump(got), err, tt.want)
		}
	}
}

func TestParseLayoutChanged(t *testing.T) {
	page := []byte(`<table><tr><td>Feb. 11, 2027</td><td>Q1 results</td></tr></table>`)
	_, err := Parse(page, cal(config.CalendarText, `quarter results fiscal year (?P<year>\d{4})`, "FY {year}"))
	if err == nil || !strings.Contains(err.Error(), "no report event among 1 dated entries") {
		t.Fatalf("err %v", err)
	}
	if _, err := Parse([]byte(`<html></html>`), cal(config.CalendarJSONAttr, `x`, "x")); err == nil {
		t.Fatal("empty page parsed")
	}
}

func TestLineDate(t *testing.T) {
	for _, tt := range []struct{ line, slash, want string }{
		{"Nov. 12-13, 2026 London", "dmy", "2026-11-12 London"},
		{"Sept. 9, 2027", "dmy", "2027-09-09 "},
		{"February 11, 2027", "dmy", "2027-02-11 "},
		{"07/05/2026 | Event", "dmy", "2026-05-07 | Event"},
		{"07/05/2026", "mdy", "2026-07-05 "},
		{"2027-02-25T00:00", "dmy", "2027-02-25 00:00"},
		{"31/02/2026", "dmy", "no"},
		{"Fooday 3, 2026", "dmy", "no"},
		{"Q1 FY2027", "dmy", "no"},
	} {
		d, rest, ok := lineDate(tt.line, tt.slash)
		got := "no"
		if ok {
			got = fmt.Sprintf("%s %s", d.Format("2006-01-02"), rest)
		}
		if got != tt.want {
			t.Errorf("lineDate(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}
