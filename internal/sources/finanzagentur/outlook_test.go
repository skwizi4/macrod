package finanzagentur

import (
	"testing"
	"time"

	"macro-collector/internal/xlsx"
)

func TestParseOutlookRecorded(t *testing.T) {
	ps, err := ParseOutlook(read(t, "Issuance_outlook_2026_Update_Q4_en.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) < 30 {
		t.Fatalf("planned = %d", len(ps))
	}
	at := func(day, name string) (Planned, bool) {
		for _, p := range ps {
			if p.Day.Format(time.DateOnly) == day && p.Name() == name {
				return p, true
			}
		}
		return Planned{}, false
	}
	// Bund 10Y of 2026-10-28, as auctioned (results file, LOOKUP 2026-11-11).
	if p, ok := at("2026-10-28", "Bund 10 years"); !ok || p.ISIN != "DE000BU2Z072" || p.Volume != 5.5e9 || p.New || p.Segment() != "10 Y" {
		t.Errorf("Bund 10Y = %+v, %v", p, ok)
	}
	if p, ok := at("2026-10-06", "Schatz 2 years"); !ok || !p.New || p.Volume != 6e9 {
		t.Errorf("Schatz = %+v, %v", p, ok)
	}
	// A range of terms has no ISIN and no single segment; a green issue has no term.
	if p, ok := at("2026-10-14", "Bund 15/20/30 years"); !ok || p.ISIN != "" || p.Segment() != "" {
		t.Errorf("range = %+v, %v", p, ok)
	}
	if p, ok := at("2026-10-20", "Green issue"); !ok || p.Volume != 0 || p.Term != "" {
		t.Errorf("green = %+v, %v", p, ok)
	}
	if p, ok := at("2026-11-16", "Bubill 12 months"); !ok || p.Segment() != "12 M" {
		t.Errorf("Bubill = %+v, %v", p, ok)
	}
}

func TestParseOutlookRejects(t *testing.T) {
	hdr := []string{"Serial number", "Date", "Security", "Term to maturity/ Remaining term*", "Type", "Volume in € mn", "Maturity", "ISIN"}
	good := []string{"1", "46323", "Bund", "10 years", "Reopening", "5500", "49902", "DE000BU2Z072"}
	if ps, err := ParseOutlook(xlsx.Build("Tabelle1", [][]string{{"title"}, hdr, good})); err != nil || len(ps) != 1 {
		t.Fatalf("good = %+v, %v", ps, err)
	}
	multi := append([]string(nil), good...)
	multi[4], multi[7] = "Reopenings (multi-ISIN)", ""
	if ps, err := ParseOutlook(xlsx.Build("Tabelle1", [][]string{hdr, multi})); err != nil || len(ps) != 1 || ps[0].New || ps[0].ISIN != "" {
		t.Errorf("multi-ISIN reopening = %+v, %v", ps, err)
	}
	bad := func(i int, v string) []string { r := append([]string(nil), good...); r[i] = v; return r }
	for name, rows := range map[string][][]string{
		"no header": {good},
		"no volume": {hdr[:5], good[:5]},
		"type":      {hdr, bad(4, "Swap")},
		"volume":    {hdr, bad(5, "lots")},
		"isin":      {hdr, bad(7, "US123")},
		"empty":     {hdr},
	} {
		if _, err := ParseOutlook(xlsx.Build("Tabelle1", rows)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestOutlookLinks(t *testing.T) {
	page := read(t, "issuance-calendar.html")
	links, err := OutlookLinks(page, "https://www.deutsche-finanzagentur.de/en/federal-securities/issuances/issuance-calendar")
	if err != nil {
		t.Fatal(err)
	}
	const dir = "https://www.deutsche-finanzagentur.de/fileadmin/user_upload/Institutionelle-investoren/auktionen/"
	if len(links) != 2 || links[0] != dir+"Issuance_outlook_2026_Update_Q4_en.xlsx" || links[1] != dir+"Issuance_outlook_2026_Update_Q3_en.xlsx" {
		t.Errorf("links = %v", links)
	}
	if _, err := OutlookLinks([]byte("<html></html>"), "https://x.test/"); err == nil {
		t.Error("page without links: no error")
	}
}

func TestMergeOutlooks(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	q4 := []Planned{{Day: d("2026-10-05"), Security: "Bubill", Term: "3 months"}, {Day: d("2026-10-14"), Security: "Bund", Term: "30 years", ISIN: "DE000BU2D012"}}
	q3 := []Planned{{Day: d("2026-09-30"), Security: "Schatz", Term: "2 years"}, {Day: d("2026-10-14"), Security: "Bund", Term: "30 years"}}
	got := MergeOutlooks(q4, q3)
	if len(got) != 3 || got[0].Security != "Schatz" || got[2].ISIN != "DE000BU2D012" {
		t.Errorf("merged = %+v", got)
	}
}
