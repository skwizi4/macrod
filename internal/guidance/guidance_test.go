package guidance

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"macro-collector/internal/config"
	"macro-collector/internal/notify"
	"macro-collector/internal/store"
)

func d(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestEffectiveNewerGivenWins(t *testing.T) {
	cfg := []config.CapexGuidance{
		{Ticker: "GOOGL", Year: 2026, Low: 175, High: 185, Source: "cfg", GivenAt: d("2026-07-23")},
		{Ticker: "GOOGL", Year: 2027, Low: 250, High: 260, Source: "cfg27", GivenAt: d("2026-10-28")},
	}
	db := []store.ConfirmedGuidance{
		{Ticker: "GOOGL", Year: 2026, Low: 195, High: 205, Given: d("2026-10-28"), Source: "call", CandidateID: 3},
		{Ticker: "GOOGL", Year: 2027, Low: 240, High: 240, Given: d("2026-07-23"), Source: "old", CandidateID: 1},
	}
	got := Effective(cfg, db)
	if len(got) != 2 {
		t.Fatalf("values = %+v", got)
	}
	if v := got[0]; v.Year != 2026 || v.Low != 195 || v.CandidateID != 3 || v.FromConfig() {
		t.Errorf("2026 = %+v, want the newer approval", v)
	}
	if v := got[1]; v.Year != 2027 || v.Low != 250 || !v.FromConfig() {
		t.Errorf("2027 = %+v, want the newer config", v)
	}
	// On a tie the approval wins.
	tie := Effective(cfg[:1], []store.ConfirmedGuidance{{Ticker: "GOOGL", Year: 2026, Low: 180, High: 190, Given: d("2026-07-23"), CandidateID: 9}})
	if tie[0].CandidateID != 9 {
		t.Errorf("tie = %+v", tie[0])
	}
}

func TestCandidatesRule2A(t *testing.T) {
	text := "Our full-year 2026 CapEx guidance range of $195-$205 billion remains unchanged."
	doc := func(given string) Doc {
		return Doc{Source: "transcript", Ref: "acc", Title: "GOOGL call", URL: "u", Given: d(given), Text: text}
	}
	cur := func(lo, hi float64, given string) []Value {
		return []Value{{Ticker: "GOOGL", Year: 2026, Low: lo, High: hi, Given: d(given)}}
	}
	for _, tc := range []struct {
		name    string
		doc     Doc
		current []Value
		want    int
		note    string
	}{
		{"no value", doc("2026-10-28"), nil, 1, ""},
		{"other year only", doc("2026-10-28"), []Value{{Ticker: "GOOGL", Year: 2027, Low: 1, High: 2, Given: d("2026-10-28")}}, 1, ""},
		{"same range same day", doc("2026-10-28"), cur(195, 205, "2026-10-28"), 0, ""},
		{"same range older doc", doc("2026-07-23"), cur(195, 205, "2026-10-28"), 0, ""},
		{"same range newer doc", doc("2026-10-28"), cur(195, 205, "2026-07-23"), 1, "подтверждает текущий диапазон 195–205 млрд $ от 2026-07-23 (approve обновит дату)"},
		{"other range newer doc", doc("2026-10-28"), cur(175, 185, "2026-07-23"), 1, "⚠ отличается от подтверждённого 175–185 млрд $ от 2026-07-23"},
		{"other range same day", doc("2026-10-28"), cur(175, 185, "2026-10-28"), 1, "⚠ отличается от подтверждённого 175–185 млрд $ от 2026-10-28"},
		{"other range older doc", doc("2026-04-24"), cur(175, 185, "2026-07-23"), 0, ""},
	} {
		got := Candidates("GOOGL", tc.doc, tc.current)
		if len(got) != tc.want {
			t.Errorf("%s: %d candidates, want %d", tc.name, len(got), tc.want)
			continue
		}
		if tc.want == 1 {
			c := got[0]
			if c.Note != tc.note || c.Low != 195 || c.High != 205 || c.Year != 2026 || c.DocRef != "acc" || !c.Given.Equal(tc.doc.Given) {
				t.Errorf("%s: candidate %+v", tc.name, c)
			}
		}
	}
}

func TestMessage(t *testing.T) {
	c := store.GuidanceCandidate{ID: 7, Ticker: "GOOGL", Year: 2026, Low: 210, High: 220, Quote: "Our 2026 capex <b>&</b> is $210-$220 billion.",
		URL: "https://abc.xyz/q3", DocTitle: "GOOGL earnings call transcript, FY2026 Q3", Given: d("2026-10-28"), Note: "⚠ отличается от подтверждённого 195–205 млрд $ от 2026-07-23"}
	cur := []Value{{Ticker: "GOOGL", Year: 2026, Low: 195, High: 205, Given: d("2026-07-23")}}
	want := "🔎 Кандидат guidance #7 — GOOGL\nCapex 2026: 210–220 млрд $\n«Our 2026 capex <b>&</b> is $210-$220 billion.»\n" +
		"Источник: GOOGL earnings call transcript, FY2026 Q3, 2026-10-28\nhttps://abc.xyz/q3\n" +
		"Сейчас в расчёте: 195–205 млрд $ от 2026-07-23 (config.yaml)\n⚠ отличается от подтверждённого 195–205 млрд $ от 2026-07-23\n" +
		"До подтверждения число нигде не используется."
	if got := Message(c, cur); got != want {
		t.Errorf("message:\n%s\nwant:\n%s", got, want)
	}
	c.Quote = strings.Repeat("capex ", 2000)
	got := Message(c, nil)
	if n := utf8.RuneCountInString(got); n > notify.MaxText || !strings.Contains(got, "…»") || !strings.HasSuffix(got, "используется.") {
		t.Errorf("long message: %d runes, tail %q", n, got[len(got)-40:])
	}
	c.Status, c.DecidedVia, c.DecidedAt = store.GuidanceApproved, "telegram", time.Date(2026, 11, 13, 13, 5, 0, 0, time.UTC)
	loc, _ := time.LoadLocation("Europe/Moscow")
	if got := Decided(c, loc); !strings.HasPrefix(got, "✅ Подтверждён 2026-11-13 16:05 кнопкой: guidance #7 — GOOGL 2026, 210–220 млрд $") {
		t.Errorf("decided %q", got)
	}
}

func TestNewestPerYear(t *testing.T) {
	c := func(year int, given string, low float64) store.GuidanceCandidate {
		return store.GuidanceCandidate{Ticker: "GOOGL", Year: year, Given: d(given), Low: low, High: low}
	}
	got := newestPerYear([]store.GuidanceCandidate{c(2026, "2026-10-28", 195), c(2026, "2026-02-04", 175), c(2025, "2025-07-23", 85), c(2025, "2025-10-29", 91)})
	if len(got) != 2 || got[0].Low != 195 || got[1].Low != 91 {
		t.Fatalf("newest = %+v", got)
	}
}
