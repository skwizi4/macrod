package edgar

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Extraction from press release text (ReleaseText output). The export uses
// it; numbers are never read from the text.

const (
	maxOutlookParas = 12
	maxQuotes       = 4
	maxQuoteParas   = 4 // a quote running over several paragraphs
	maxHeadingRunes = 80
)

var (
	outlookWord = regexp.MustCompile(`(?i)\b(outlook|guidance)\b`)
	// Disclaimers inside a guidance section carry no numbers worth keeping.
	// A pointer to the call ("Microsoft will provide forward-looking
	// guidance … on its earnings conference call") is not guidance either.
	outlookDisclaimer = regexp.MustCompile(`(?i)^(the following forward-looking statements|the guidance provided above is only an estimate)|will provide .*guidance.*(conference call|webcast)`)
	// Tesla's update is a slide deck: each slide is one paragraph opening with
	// a letter-spaced title, "O U T L O O K Volume We are focused…".
	spacedTitle = regexp.MustCompile(`^((?:[A-Z&] ){2,}[A-Z&])\s`)
	slidePage   = regexp.MustCompile(`\s+\d{1,2}$`) // slide number at the end

	saidVerb  = regexp.MustCompile(`(?i)\b(said|says|added|commented)\b`)
	execTitle = regexp.MustCompile(`(?i)\b(CEO|CFO|chief executive|chief financial|chief operating|president|founder|chairman|chairwoman|chair)\b`)
)

// Outlook returns the guidance section of a press release: the paragraphs
// under a heading that mentions outlook or guidance ("Outlook", "Financial
// Guidance", "CFO Outlook Commentary", "Fourth Quarter … Business Outlook"),
// up to the next heading that does not. Sub-headings that mention guidance
// ("Third Quarter 2026 Guidance") are kept. Without such a heading — MSFT,
// AAPL, GOOGL give guidance on the call — it returns nil; there is no
// keyword search over the whole text, which would pick up sentences like
// "compared to our guidance provided on April 29".
func Outlook(text string) []string {
	paras := paragraphs(text)
	for i, p := range paras {
		if t, rest, ok := slideTitle(p); ok && outlookWord.MatchString(t) {
			return []string{rest}
		}
		if !heading(p) || !outlookWord.MatchString(p) {
			continue
		}
		var out []string
		for _, q := range paras[i+1:] {
			if heading(q) && !outlookWord.MatchString(q) {
				break
			}
			if outlookDisclaimer.MatchString(q) {
				continue
			}
			if out = append(out, q); len(out) == maxOutlookParas {
				break
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// Quotes returns management quotes: paragraphs with a quotation and
// "said <name>, <CEO|CFO|…>" or "<name>, CEO …, said: “…". A quote whose
// closing mark is in a later paragraph is joined with it.
func Quotes(text string) []string {
	paras := paragraphs(text)
	var out []string
	for i := 0; i < len(paras) && len(out) < maxQuotes; i++ {
		p := paras[i]
		if !strings.ContainsAny(p, "“\"") || !saidVerb.MatchString(p) || !execTitle.MatchString(p) {
			continue
		}
		q := p
		for j := i + 1; j < len(paras) && j <= i+maxQuoteParas && openQuote(q); j++ {
			q += "\n" + paras[j]
			i = j
		}
		out = append(out, q)
	}
	return out
}

// Title is the first heading of the release ("NVIDIA Announces Financial
// Results for Second Quarter Fiscal 2027"), or "".
func Title(text string) string {
	for _, p := range paragraphs(text) {
		if heading(p) && utf8.RuneCountInString(p) > 15 {
			return p
		}
	}
	return ""
}

func paragraphs(text string) []string {
	var out []string
	for _, p := range strings.Split(text, "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// heading is a short line that does not end a sentence or introduce a list.
func heading(p string) bool {
	if utf8.RuneCountInString(p) > maxHeadingRunes || strings.HasPrefix(p, "•") || strings.ContainsAny(p, "“\"") {
		return false
	}
	return !strings.HasSuffix(p, ".") && !strings.HasSuffix(p, ":") && !strings.HasSuffix(p, ";")
}

// slideTitle splits "O U T L O O K Volume We…" into "OUTLOOK" and the rest.
func slideTitle(p string) (title, rest string, ok bool) {
	m := spacedTitle.FindStringSubmatch(p)
	if m == nil {
		return "", "", false
	}
	rest = slidePage.ReplaceAllString(strings.TrimSpace(p[len(m[0]):]), "")
	return strings.ReplaceAll(m[1], " ", ""), rest, true
}

// openQuote reports a paragraph with more opening than closing quotes.
func openQuote(s string) bool {
	return strings.Count(s, "“") > strings.Count(s, "”")
}
