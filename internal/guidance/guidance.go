package guidance

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"macro-collector/internal/config"
	"macro-collector/internal/notify"
	"macro-collector/internal/store"
)

// Value is the guidance in force for a ticker and year: from
// edgar.capex_guidance of config.yaml (CandidateID 0) or an approved
// candidate. The newer given wins.
type Value struct {
	Ticker      string
	Year        int
	Low, High   float64
	Given       time.Time
	Source      string
	CandidateID int64 // 0: config.yaml
}

// FromConfig reports whether the value is the hand-entered one.
func (v Value) FromConfig() bool { return v.CandidateID == 0 }

// Effective merges the config and the approved ranges: one value per
// ticker and year, the newest given (on a tie the approval wins, the
// config being the fallback). Order: as first seen, config first.
func Effective(cfg []config.CapexGuidance, approved []store.ConfirmedGuidance) []Value {
	var out []Value
	put := func(v Value) {
		for i := range out {
			if out[i].Ticker == v.Ticker && out[i].Year == v.Year {
				if !v.Given.Before(out[i].Given) {
					out[i] = v
				}
				return
			}
		}
		out = append(out, v)
	}
	for _, g := range cfg {
		put(Value{Ticker: g.Ticker, Year: g.Year, Low: g.Low, High: g.High, Given: g.GivenAt, Source: g.Source})
	}
	for _, g := range approved { // oldest approval first
		put(Value{Ticker: g.Ticker, Year: g.Year, Low: g.Low, High: g.High, Given: g.Given, Source: g.Source, CandidateID: g.CandidateID})
	}
	return out
}

// Doc is a stored document searched for guidance.
type Doc struct {
	Source string // transcript | edgar (documents.source)
	Ref    string // accession of the 8-K
	Title  string
	URL    string
	Given  time.Time // New York date
	Text   string
}

// Candidates are the guidance matches of doc for ticker, measured against
// the values in force (DECISIONS D28, rule 2A):
//   - no value for the year: a candidate;
//   - the same range: a candidate only when doc is newer ("confirms", its
//     approval moves given forward so the export is no longer stale);
//   - another range: a candidate ("differs") unless doc is older.
func Candidates(ticker string, doc Doc, current []Value) []store.GuidanceCandidate {
	var out []store.GuidanceCandidate
	for _, m := range Find(doc.Text) {
		c := store.GuidanceCandidate{Ticker: ticker, Year: m.Year, Low: m.Low, High: m.High, Quote: m.Quote, URL: doc.URL,
			DocSource: doc.Source, DocRef: doc.Ref, DocTitle: doc.Title, Given: doc.Given}
		if v, ok := find(current, ticker, m.Year); ok {
			switch same := v.Low == m.Low && v.High == m.High; {
			case same && !doc.Given.After(v.Given):
				continue
			case same:
				c.Note = fmt.Sprintf("подтверждает текущий диапазон %s от %s (approve обновит дату)", Range(v.Low, v.High), day(v.Given))
			case doc.Given.Before(v.Given):
				continue // older than the value in force
			default:
				c.Note = fmt.Sprintf("⚠ отличается от подтверждённого %s от %s", Range(v.Low, v.High), day(v.Given))
			}
		}
		out = append(out, c)
	}
	return out
}

func find(vs []Value, ticker string, year int) (Value, bool) {
	for _, v := range vs {
		if v.Ticker == ticker && v.Year == year {
			return v, true
		}
	}
	return Value{}, false
}

// Range is "195–205 млрд $" ("250 млрд $" for a single amount).
func Range(low, high float64) string {
	if low == high {
		return amount(low) + " млрд $"
	}
	return amount(low) + "–" + amount(high) + " млрд $"
}

func amount(v float64) string {
	return strings.Replace(strconv.FormatFloat(v, 'f', -1, 64), ".", ",", 1)
}

func day(t time.Time) string { return t.Format(time.DateOnly) }

// Message is the Telegram text of a candidate (decision 1A): plain text,
// no parse_mode, so quotes need no escaping; the quote is cut first when
// the text is over the limit.
func Message(c store.GuidanceCandidate, current []Value) string {
	head := fmt.Sprintf("🔎 Кандидат guidance #%d — %s\nCapex %d: %s\n", c.ID, c.Ticker, c.Year, Range(c.Low, c.High))
	tail := "\nИсточник: " + c.Source() + "\n" + c.URL + "\n"
	if v, ok := find(current, c.Ticker, c.Year); ok {
		tail += fmt.Sprintf("Сейчас в расчёте: %s от %s (%s)\n", Range(v.Low, v.High), day(v.Given), origin(v))
	} else {
		tail += "Сейчас в расчёте: нет guidance на этот год\n"
	}
	if c.Note != "" {
		tail += c.Note + "\n"
	}
	tail += "До подтверждения число нигде не используется."
	quote := "«" + c.Quote + "»"
	if room := notify.MaxText - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail); utf8.RuneCountInString(quote) > room {
		quote = string([]rune(quote)[:max(room-2, 0)]) + "…»"
	}
	return head + quote + tail
}

// Decided is the text a candidate's message is edited to after a decision.
func Decided(c store.GuidanceCandidate, loc *time.Location) string {
	mark, what := "✅ Подтверждён", "implied Q4 — в следующем экспорте"
	if c.Status == store.GuidanceRejected {
		mark, what = "❌ Отклонён", "в расчёт не идёт"
	}
	via := "кнопкой"
	if c.DecidedVia == "cli" {
		via = "через macroctl"
	}
	return fmt.Sprintf("%s %s %s: guidance #%d — %s %d, %s (%s).\n%s\n%s",
		mark, c.DecidedAt.In(loc).Format("2006-01-02 15:04"), via, c.ID, c.Ticker, c.Year, Range(c.Low, c.High), what, c.Source(), c.URL)
}

func origin(v Value) string {
	if v.FromConfig() {
		return "config.yaml"
	}
	return fmt.Sprintf("подтверждён #%d", v.CandidateID)
}
