package fedtexts

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var fixtureDir = filepath.Join("..", "..", "..", "testdata", "fed")

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseFOMCCalendar(t *testing.T) {
	ms, err := ParseFOMCCalendar(readFixture(t, "fomccalendars.htm"))
	if err != nil {
		t.Fatal(err)
	}
	// 2021–2027, 8 meetings a year; the 2025-08-22 notation vote is skipped.
	if len(ms) != 56 {
		t.Fatalf("meetings = %d, want 56", len(ms))
	}
	byStart := make(map[string]Meeting)
	for _, m := range ms {
		byStart[m.Start.Format(time.DateOnly)] = m
	}
	if _, ok := byStart["2025-08-22"]; ok {
		t.Error("notation vote parsed as a meeting")
	}

	var y2026 []string
	var sep []string
	for _, m := range ms {
		if m.Start.Year() == 2026 {
			y2026 = append(y2026, meetingID(m))
			if m.SEP {
				sep = append(sep, m.End.Format("01-02"))
			}
		}
	}
	want := []string{"2026-01-27/28", "2026-03-17/18", "2026-04-28/29", "2026-06-16/17",
		"2026-07-28/29", "2026-09-15/16", "2026-10-27/28", "2026-12-08/09"}
	if !slices.Equal(y2026, want) {
		t.Errorf("2026 meetings = %v", y2026)
	}
	if !slices.Equal(sep, []string{"03-18", "06-17", "09-16", "12-09"}) {
		t.Errorf("2026 SEP = %v", sep)
	}

	// Minutes: released dates for past meetings, all 21 days after the decision in 2026.
	if got := byStart["2026-09-15"].Minutes; !got.Equal(day("2026-10-07")) {
		t.Errorf("September minutes = %v", got)
	}
	for _, m := range ms {
		if m.Start.Year() == 2026 && !m.Minutes.IsZero() && m.Minutes.Sub(m.End) != minutesLag*24*time.Hour {
			t.Errorf("%s: minutes %s, not %d days later", meetingID(m), m.Minutes.Format(time.DateOnly), minutesLag)
		}
	}
	if !byStart["2026-10-27"].Minutes.IsZero() {
		t.Error("October 2026 minutes are not out yet")
	}

	// Meetings across a month end.
	for start, id := range map[string]string{"2024-04-30": "2024-04-30/05-01", "2023-01-31": "2023-01-31/02-01", "2023-10-31": "2023-10-31/11-01"} {
		if got := meetingID(byStart[start]); got != id {
			t.Errorf("%s: %s, want %s", start, got, id)
		}
	}
}

func TestParseMeetingRejectsUnknownFormat(t *testing.T) {
	for _, c := range [][2]string{{"October", "27 to 28"}, {"Octember", "27-28"}, {"February", "30-31"}} {
		if _, err := parseMeeting(2026, c[0], c[1]); err == nil {
			t.Errorf("%v: want error", c)
		}
	}
	page := []byte(`<div class="panel"><div class="panel-heading"><h4>2026 FOMC Meetings</h4></div>
		<div class="row fomc-meeting"><div class="fomc-meeting__month">Oct</div><div class="fomc-meeting__date">TBD</div></div></div>`)
	if _, err := ParseFOMCCalendar(page); err == nil || !strings.Contains(err.Error(), "unexpected day format") {
		t.Errorf("err = %v", err)
	}
	if _, err := ParseFOMCCalendar([]byte(`<html><body>redesigned</body></html>`)); err == nil {
		t.Error("empty page: want error")
	}
}

func TestParseBeigeBook(t *testing.T) {
	dates, err := ParseBeigeBook(readFixture(t, "beige-book-default.htm"))
	if err != nil {
		t.Fatal(err)
	}
	var y2026 []string
	for _, d := range dates {
		if d.Year() == 2026 {
			y2026 = append(y2026, d.Format("01-02"))
		}
	}
	want := []string{"01-14", "03-04", "04-15", "06-03", "07-15", "09-02", "10-14", "11-25"}
	if !slices.Equal(y2026, want) {
		t.Errorf("2026 = %v, want %v", y2026, want)
	}
}

func TestEvents(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := ParseFOMCCalendar(readFixture(t, "fomccalendars.htm"))
	if err != nil {
		t.Fatal(err)
	}
	bb, err := ParseBeigeBook(readFixture(t, "beige-book-default.htm"))
	if err != nil {
		t.Fatal(err)
	}
	evs := Events(ms, bb, ny, day("2026-10-24"), day("2027-01-01"))
	var got []string
	for _, e := range evs {
		got = append(got, e.ScheduledAt.Format(time.RFC3339)+" "+e.Name+" "+e.Related.Meeting)
		if e.ScheduledAt.Location() != time.UTC {
			t.Errorf("%+v", e)
		}
	}
	slices.Sort(got)
	// 14:00 ET: 18:00 UTC before 2026-11-01, 19:00 UTC after.
	want := []string{
		"2026-10-28T18:00:00Z FOMC decision 2026-10-27/28",
		"2026-11-18T19:00:00Z FOMC minutes 2026-10-27/28",
		"2026-11-25T19:00:00Z Beige Book ",
		"2026-12-09T19:00:00Z FOMC decision 2026-12-08/09",
		"2026-12-30T19:00:00Z FOMC minutes 2026-12-08/09",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, e := range evs {
		if e.Name == NameDecision && e.Related.Meeting == "2026-12-08/09" && (!e.Related.SEP || e.Importance != "high") {
			t.Errorf("December decision = %+v", e)
		}
	}
}
