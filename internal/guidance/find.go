// Package guidance finds full-year capex guidance in call transcripts and
// earnings 8-Ks and keeps it as candidates until the user approves them
// (phase 14b, ТЗ §21.6 п. 3, DECISIONS D15, D28). A candidate is never used
// as a number: only an approved range reaches the export (§19.3a).
package guidance

import (
	"regexp"
	"strconv"
	"strings"

	"macro-collector/internal/sources/edgar"
)

// Match is a sentence with a capex amount in billions of dollars and a year.
type Match struct {
	Year      int
	Low, High float64
	Quote     string
}

var (
	capexRe = regexp.MustCompile(`(?i)\b(capex|capital expenditures?)\b`)
	yearRe  = regexp.MustCompile(`\b20\d\d\b`)
	// outlookRe: the sentence speaks of a plan, not of money raised "to be
	// used for capital expenditures".
	outlookRe = regexp.MustCompile(`(?i)\b(expect\w*|anticipat\w*|guid\w*|outlook|forecast\w*|plan\w*|invest\w*|range|will)\b`)
	// quarterRe marks a sentence about a quarter: its single amount is the
	// quarter's spend, not the year's guidance.
	quarterRe = regexp.MustCompile(`(?i)\b(q[1-4]|(first|second|third|fourth) quarter|the quarter)\b`)
	num       = `(\d+(?:\.\d+)?)`
	unit      = `(?:\s*(billion|bn|b))`
	// rangeRe: "$195-$205 billion", "$195 billion to $205 billion",
	// "$195B to $205B", "between $195 and $205 billion".
	rangeRe = regexp.MustCompile(`(?i)(between\s+)?~?\$\s?` + num + unit + `?\s*(-|to|and)\s*~?\$?\s?` + num + unit + `\b`)
	// singleRe: "$200 billion", "~$200B".
	singleRe = regexp.MustCompile(`(?i)~?\$\s?` + num + unit + `\b`)
	// million amounts are not guidance in billions: such a sentence is left out.
	millionRe = regexp.MustCompile(`(?i)\$\s?\d+(?:\.\d+)?\s*(million|mn|m)\b`)
)

// normalize turns dashes, non-breaking hyphens and spaces into ASCII so
// "$195‑$205 billion" (U+2011) parses like "$195-$205 billion".
var normalize = strings.NewReplacer(
	"‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "−", "-",
	" ", " ", " ", " ", " ", " ",
)

// Find returns the sentences of text that give a capex amount or range in
// billions of dollars for exactly one year, with a word of outlook
// (expect, anticipate, guidance, outlook, forecast, plan, invest, range, will). A range wins over single
// amounts ("Q3 capex was $51B, bringing 2026 to $195B–$205B"); a sentence
// with two different ranges, two different single amounts, a single amount
// of a quarter, a million amount, no year or two years is not a match.
// The quote is the sentence as written.
func Find(text string) []Match {
	var out []Match
	seen := map[Match]bool{}
	for _, s := range edgar.Sentences(text) {
		m, ok := match(s)
		if !ok {
			continue
		}
		key := Match{Year: m.Year, Low: m.Low, High: m.High}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m)
	}
	return out
}

func match(sentence string) (Match, bool) {
	s := normalize.Replace(sentence)
	if !capexRe.MatchString(s) || !outlookRe.MatchString(s) || millionRe.MatchString(s) {
		return Match{}, false
	}
	years := distinct(yearRe.FindAllString(s, -1))
	if len(years) != 1 {
		return Match{}, false
	}
	year, _ := strconv.Atoi(years[0])
	m := Match{Year: year, Quote: sentence}

	type pair struct{ lo, hi float64 }
	var ranges []pair
	rest := s
	for _, r := range rangeRe.FindAllStringSubmatch(s, -1) {
		// r: 1 between, 2 low, 3 unit, 4 sep, 5 high, 6 unit
		if strings.EqualFold(r[4], "and") && r[1] == "" {
			continue // "$51B and $195 billion" is two amounts, not a range
		}
		lo, _ := strconv.ParseFloat(r[2], 64)
		hi, _ := strconv.ParseFloat(r[5], 64)
		if lo <= 0 || hi < lo {
			continue
		}
		p := pair{lo, hi}
		if len(ranges) == 0 || ranges[0] != p {
			ranges = append(ranges, p)
		}
		rest = strings.Replace(rest, r[0], " ", 1)
	}
	switch len(ranges) {
	case 1:
		m.Low, m.High = ranges[0].lo, ranges[0].hi
		return m, true
	case 0:
	default:
		return Match{}, false
	}
	if quarterRe.MatchString(s) {
		return Match{}, false
	}
	var singles []string
	for _, r := range singleRe.FindAllStringSubmatch(rest, -1) {
		singles = append(singles, r[1])
	}
	singles = distinct(singles)
	if len(singles) != 1 {
		return Match{}, false
	}
	v, _ := strconv.ParseFloat(singles[0], 64)
	if v <= 0 {
		return Match{}, false
	}
	m.Low, m.High = v, v
	return m, true
}

func distinct(xs []string) []string {
	var out []string
	for _, x := range xs {
		dup := false
		for _, o := range out {
			if o == x {
				dup = true
			}
		}
		if !dup {
			out = append(out, x)
		}
	}
	return out
}
