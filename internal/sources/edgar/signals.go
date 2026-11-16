package edgar

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Macro signals (phase 5c, ТЗ §19.4): whole sentences on macro topics
// (demand, capacity, prices, hiring, tariffs, China, memory) from press
// releases and call transcripts. Text only: no numbers are taken from them.

// Topic is a macro topic: a sentence belongs to the first topic that
// matches it.
type Topic struct {
	Name  string
	Match *regexp.Regexp
}

// Signal is a sentence on a topic from a source document.
type Signal struct {
	Topic, Sentence, Source string
	Strong                  bool // has a strong phrase
}

// SignalRules select the sentences (ТЗ §19.4, rules of 2026-10-31).
type SignalRules struct {
	Topics             []Topic
	Exclude            *regexp.Regexp // nil: nothing excluded
	Strong             *regexp.Regexp // strong phrases: picked first; nil: none
	MinChars, MaxChars int            // sentence length in characters
	PerTopic           int            // per company and topic
	PerCompany         int            // per company
}

// SignalPicker picks one company's signals over its documents: candidates
// from every document are gathered first, then the strong ones win the
// limits.
type SignalPicker struct {
	rules      SignalRules
	seen       map[string]bool
	candidates []Signal
}

// NewSignalPicker returns a picker for one company.
func NewSignalPicker(r SignalRules) *SignalPicker {
	return &SignalPicker{rules: r, seen: map[string]bool{}}
}

var digitRe = regexp.MustCompile(`\d`)

// Add gathers the candidate sentences of a document. conversational marks
// the Q&A of a call: spoken answers lean on what was said before, so only
// a sentence with a number or a strong phrase is understood alone.
func (p *SignalPicker) Add(text, source string, conversational bool) {
	for _, s := range Sentences(text) {
		n := utf8.RuneCountInString(s)
		last, _ := utf8.DecodeLastRuneInString(s)
		// Only whole sentences: list fragments ("supply or other quality
		// problems;") and slide text have no final stop.
		if !strings.ContainsRune(".!?\"”'’", last) || n < p.rules.MinChars || n > p.rules.MaxChars || p.seen[s] ||
			(p.rules.Exclude != nil && p.rules.Exclude.MatchString(s)) {
			continue
		}
		strong := p.rules.Strong != nil && p.rules.Strong.MatchString(s)
		if conversational && !strong && !digitRe.MatchString(s) {
			continue
		}
		for _, t := range p.rules.Topics {
			if t.Match.MatchString(s) {
				p.seen[s] = true
				p.candidates = append(p.candidates, Signal{Topic: t.Name, Sentence: s, Source: source, Strong: strong})
				break
			}
		}
	}
}

// Signals returns the picked signals: strong sentences first, then the
// others; within each, topics in the order of the rules (their priority,
// FIXES_W44 D2), then in the order added; within the limits per topic and
// per company.
func (p *SignalPicker) Signals() []Signal {
	var out []Signal
	counts := map[string]int{}
	for _, strong := range []bool{true, false} {
		for _, t := range p.rules.Topics {
			for _, c := range p.candidates {
				if c.Strong != strong || c.Topic != t.Name || counts[c.Topic] >= p.rules.PerTopic || len(out) >= p.rules.PerCompany {
					continue
				}
				counts[c.Topic]++
				out = append(out, c)
			}
		}
	}
	return out
}

// Sentences splits text into sentences: paragraphs and lines first (list
// items "• " lose their marker), then at ". ", "! ", "? " before a capital
// letter, a digit or an opening quote, so "U.S. factory" and "$0.9
// billion" stay whole. Whitespace is collapsed.
func Sentences(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.Join(strings.Fields(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "•"))), " ")
		if line == "" {
			continue
		}
		rs := []rune(line)
		start := 0
		for i := 0; i < len(rs)-2; i++ {
			if (rs[i] != '.' && rs[i] != '!' && rs[i] != '?') || rs[i+1] != ' ' {
				continue
			}
			next := rs[i+2]
			if unicode.IsUpper(next) || unicode.IsDigit(next) || strings.ContainsRune("“\"‘'(", next) {
				out = append(out, string(rs[start:i+1]))
				start = i + 2
			}
		}
		out = append(out, string(rs[start:]))
	}
	return out
}
