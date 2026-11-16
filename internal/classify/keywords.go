package classify

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"macro-collector/internal/config"
)

// Keywords is the fallback classifier (§11 points 1–5). A keyword matches
// whole words regardless of case; "word*" matches any ending (ставк* →
// ставка, ставки); a phrase matches consecutive words. A tag scores one
// point per distinct keyword found, the channel's default_tag one more; tags
// below min_score are dropped. drop_if_contains words match anywhere in the
// normalised text (промокод → промокоду), a trailing * is ignored.
type Keywords struct {
	minScore float64
	tags     []string
	rules    map[string][]Phrase
	drop     []string
}

// Phrase is a compiled keyword: consecutive word patterns.
type Phrase []wordPattern

type wordPattern struct {
	text   string
	prefix bool
}

// NewKeywords compiles classify.rules and drop_if_contains.
func NewKeywords(cfg config.Classify) *Keywords {
	k := &Keywords{minScore: float64(cfg.MinScore), tags: cfg.Tags, rules: make(map[string][]Phrase)}
	for tag, words := range cfg.Rules {
		for _, w := range words {
			if p := compile(w); len(p) > 0 {
				k.rules[tag] = append(k.rules[tag], p)
			}
		}
	}
	for _, w := range cfg.DropIfContains {
		if ws := words(strings.TrimSuffix(w, "*")); len(ws) > 0 {
			k.drop = append(k.drop, strings.Join(ws, " "))
		}
	}
	return k
}

// CompilePhrase compiles a keyword in the classify.rules syntax: whole
// words regardless of case, "word*" matches any ending.
func CompilePhrase(keyword string) Phrase { return compile(keyword) }

func compile(keyword string) Phrase {
	var p Phrase
	for _, f := range strings.Fields(keyword) {
		prefix := strings.HasSuffix(f, "*")
		for _, w := range words(strings.TrimSuffix(f, "*")) {
			p = append(p, wordPattern{text: w})
		}
		if prefix && len(p) > 0 {
			p[len(p)-1].prefix = true
		}
	}
	return p
}

// Classify never fails.
func (k *Keywords) Classify(_ context.Context, posts []Post) ([]Result, error) {
	out := make([]Result, len(posts))
	for i, p := range posts {
		out[i] = k.classify(p)
	}
	return out, nil
}

func (k *Keywords) classify(p Post) Result {
	ws := words(p.Text)
	res := Result{By: ByKeywords}
	joined := " " + strings.Join(ws, " ") + " "
	for _, d := range k.drop {
		if strings.Contains(joined, d) {
			res.IsJunk = true
			res.Reason = "drop_if_contains"
			return res
		}
	}
	for _, tag := range k.tags {
		score := 0.0
		for _, ph := range k.rules[tag] {
			if ph.In(ws) {
				score++
			}
		}
		if tag == p.DefaultTag {
			score++
		}
		if score >= k.minScore {
			res.Tags = append(res.Tags, Tag{Tag: tag, Score: score})
		}
	}
	res.setPrimary(k.tags)
	return res
}

// In reports whether the phrase occurs in normalised words (Normalize).
func (ph Phrase) In(ws []string) bool {
	for i := 0; i+len(ph) <= len(ws); i++ {
		ok := true
		for j, wp := range ph {
			w := ws[i+j]
			if wp.prefix && !strings.HasPrefix(w, wp.text) || !wp.prefix && w != wp.text {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

var links = regexp.MustCompile(`(?i)\b(?:https?://|t\.me/|www\.)\S+`)

// words normalises text (§11.1: lower case, no links or emoji, ё → е) and
// splits it into words of letters and digits; apostrophes inside a word
// (звʼязок, don't) are kept.
func words(text string) []string {
	text = strings.ToLower(links.ReplaceAllString(text, " "))
	text = strings.ReplaceAll(text, "ё", "е")
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, strings.Trim(b.String(), "'’ʼ"))
			b.Reset()
		}
	}
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case (r == '\'' || r == '’' || r == 'ʼ') && b.Len() > 0:
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// Normalize is the text as words, the form keyword matching and repost
// dedup compare: lower case, no links, emoji or punctuation.
func Normalize(text string) []string { return words(text) }
