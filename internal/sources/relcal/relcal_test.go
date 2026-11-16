package relcal

import (
	"os"
	"slices"
	"testing"
	"time"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../testdata/relcal/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func has(es []Entry, day, title string) (Entry, bool) {
	i := slices.IndexFunc(es, func(e Entry) bool { return e.Day.Format(time.DateOnly) == day && e.Title == title })
	if i < 0 {
		return Entry{}, false
	}
	return es[i], true
}

// Recorded 2026-11-11 (LOOKUP in PROGRESS, batch 11d).
func TestParseECBStatsRecorded(t *testing.T) {
	es, err := ParseECBStats(read(t, "ecb_statscal.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 280 {
		t.Errorf("entries = %d, want 280", len(es))
	}
	e, ok := has(es, "2026-11-26", "Monetary developments in the euro area")
	if !ok || e.Dataset != "BSI" || e.Clock != "10:00" || e.Period != "Oct-2026" || e.Tentative {
		t.Errorf("M3 = %+v, %v", e, ok)
	}
	if e, ok := has(es, "2026-12-01", "Euro area seasonally adjusted HICP flash estimate"); !ok || e.Dataset != "HICP" || e.Clock != "15:00" {
		t.Errorf("HICP flash = %+v, %v", e, ok)
	}
}

func TestParseECBStatsLayout(t *testing.T) {
	page := []byte(`<dl><dt>05/01/2027 10:00 Tentative</dt><dd>Monetary developments in the euro area (Dataset: BSI)<br>Reference period: Nov-2026</dd>
<dt>Legend</dt><dd>no date</dd></dl>`)
	es, err := ParseECBStats(page)
	if err != nil || len(es) != 1 || !es[0].Tentative || es[0].Day.Format(time.DateOnly) != "2027-01-05" || es[0].Period != "Nov-2026" {
		t.Errorf("= %+v, %v", es, err)
	}
	if _, err := ParseECBStats([]byte("<html>changed</html>")); err == nil {
		t.Error("page without entries: no error")
	}
}

func TestParseICSRecorded(t *testing.T) {
	es, err := ParseICS(read(t, "eurostat.ics"))
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 199 { // trimmed to 2026-11…2027-01 when recorded
		t.Errorf("events = %d, want 199", len(es))
	}
	for day, title := range map[string]string{
		"2026-11-13": "Flash estimate GDP and employment - EU and euro area",
		"2026-12-01": "Flash estimate inflation euro area",
		"2026-12-17": "Inflation (HICP)",
		"2027-01-22": "Quarterly government debt",
	} {
		if e, ok := has(es, day, title); !ok || e.Clock != "" {
			t.Errorf("%s %q = %+v, %v", day, title, e, ok)
		}
	}
}

func TestParseICSForms(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:20261201T100000\r\nSUMMARY:Flash\\, euro\r\n  area\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	es, err := ParseICS([]byte(ics))
	if err != nil || len(es) != 1 || es[0].Title != "Flash, euro area" || es[0].Clock != "10:00" {
		t.Errorf("= %+v, %v", es, err)
	}
	for name, s := range map[string]string{
		"no events":   "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n",
		"no summary":  "BEGIN:VEVENT\r\nDTSTART;VALUE=DATE:20261201\r\nEND:VEVENT\r\n",
		"bad dtstart": "BEGIN:VEVENT\r\nDTSTART:2026\r\nSUMMARY:x\r\nEND:VEVENT\r\n",
	} {
		if _, err := ParseICS([]byte(s)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
