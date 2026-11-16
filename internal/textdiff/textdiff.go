// Package textdiff compares two versions of a statement sentence by
// sentence (ТЗ §10): added, removed and changed sentences, with changed
// words marked as ~~old~~ **new**.
package textdiff

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind of an entry.
type Kind int

const (
	Equal Kind = iota
	Added
	Removed
	Changed
)

// Entry is one sentence of the comparison, in reading order of the new
// text (removed sentences sit where they used to be).
type Entry struct {
	Kind Kind
	Old  string // Equal, Removed, Changed
	New  string // Equal, Added, Changed
	Para int    // paragraph of the new text; a removed paragraph gets its own negative id
}

// Diff is the result of Compare.
type Diff struct {
	Entries []Entry
}

// Stats counts changed, added and removed sentences.
func (d Diff) Stats() (changed, added, removed int) {
	for _, e := range d.Entries {
		switch e.Kind {
		case Changed:
			changed++
		case Added:
			added++
		case Removed:
			removed++
		}
	}
	return changed, added, removed
}

// Identical reports whether the texts have the same sentences.
func (d Diff) Identical() bool {
	c, a, r := d.Stats()
	return c+a+r == 0
}

// similarity threshold for pairing a removed and an added sentence as a change.
const minSimilarity = 0.5

type sentence struct {
	text string
	key  string // normalized for comparison
	para int
}

// Compare diffs old and new text.
func Compare(oldText, newText string) Diff {
	a, b := split(oldText), split(newText)
	ops := lcs(len(a), len(b), func(i, j int) bool { return a[i].key == b[j].key })

	var d Diff
	var dels, ins []int // pending hunk
	prev := anchor{-1, -1}
	own := 0 // unique paragraph ids (negative) for removed paragraphs
	flush := func(next anchor) {
		d.Entries = append(d.Entries, hunk(a, b, dels, ins, prev, next, &own)...)
		dels, ins = nil, nil
	}
	for _, op := range ops {
		switch op.kind {
		case Equal:
			next := anchor{a[op.i].para, b[op.j].para}
			flush(next)
			prev = next
			d.Entries = append(d.Entries, Entry{Kind: Equal, Old: a[op.i].text, New: b[op.j].text, Para: b[op.j].para})
		case Removed:
			dels = append(dels, op.i)
		case Added:
			ins = append(ins, op.j)
		}
	}
	flush(anchor{-1, -1})
	return d
}

// anchor is the paragraph (old, new) of an unchanged sentence around a hunk.
type anchor struct{ old, new int }

// hunk turns a run of removed and added sentences into entries: each
// removed sentence is paired with the most similar unpaired added one
// (≥ minSimilarity) as Changed; the rest stay Removed / Added. A removed
// sentence stays in the paragraph of its unchanged neighbour, if it shared
// one with it in the old text, else forms a paragraph of its own.
func hunk(a, b []sentence, dels, ins []int, prev, next anchor, own *int) []Entry {
	pairOf := make(map[int]int) // added index → removed index
	paired := make(map[int]bool)
	for _, di := range dels {
		best, bestSim := -1, minSimilarity
		for _, ij := range ins {
			if _, used := pairOf[ij]; used {
				continue
			}
			if s := similarity(a[di].key, b[ij].key); s >= bestSim {
				best, bestSim = ij, s
			}
		}
		if best >= 0 {
			pairOf[best] = di
			paired[di] = true
		}
	}
	var out []Entry
	lastOld, lastPara := -2, 0
	for _, di := range dels {
		if paired[di] {
			continue
		}
		para := a[di].para
		switch {
		case para == prev.old && prev.new >= 0:
			para = prev.new
		case para == next.old && next.new >= 0:
			para = next.new
		case para == lastOld:
			para = lastPara
		default:
			*own--
			lastOld, lastPara = para, *own
			para = *own
		}
		out = append(out, Entry{Kind: Removed, Old: a[di].text, Para: para})
	}
	for _, ij := range ins {
		if di, ok := pairOf[ij]; ok {
			out = append(out, Entry{Kind: Changed, Old: a[di].text, New: b[ij].text, Para: b[ij].para})
		} else {
			out = append(out, Entry{Kind: Added, New: b[ij].text, Para: b[ij].para})
		}
	}
	return out
}

// similarity is the Dice coefficient of the word sets, 2·|A∩B| / (|A|+|B|).
func similarity(x, y string) float64 {
	wx, wy := wordSet(x), wordSet(y)
	if len(wx) == 0 && len(wy) == 0 {
		return 1
	}
	inter := 0
	for w := range wx {
		if wy[w] {
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(wx)+len(wy))
}

func wordSet(s string) map[string]bool {
	out := make(map[string]bool)
	for _, w := range strings.Fields(strings.ToLower(s)) {
		if w = strings.TrimFunc(w, unicode.IsPunct); w != "" {
			out[w] = true
		}
	}
	return out
}

// normalize makes typographic variants compare equal: non-breaking
// hyphens and spaces, repeated whitespace.
func normalize(s string) string {
	s = strings.NewReplacer("‑", "-", " ", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// abbreviations end with a dot that does not end a sentence.
var abbreviations = map[string]bool{
	"u.s": true, "mr": true, "ms": true, "mrs": true, "dr": true, "a.m": true, "p.m": true,
	"e.g": true, "i.e": true, "no": true, "st": true, "vs": true, "inc": true, "co": true,
	"corp": true, "jr": true, "sr": true, "jan": true, "feb": true, "mar": true, "apr": true,
	"jun": true, "jul": true, "aug": true, "sep": true, "sept": true, "oct": true, "nov": true, "dec": true,
}

// split breaks text into sentences. Paragraphs are separated by blank
// lines; within one, a sentence ends at . ! or ? (plus closing quotes or
// brackets) followed by a space and an upper-case letter or digit, unless
// the dot ends a known abbreviation ("U.S.", "p.m.").
func split(text string) []sentence {
	var out []sentence
	for p, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		para = strings.Join(strings.Fields(para), " ")
		start := 0
		for i := 0; i < len(para); i++ {
			c := para[i]
			if c != '.' && c != '!' && c != '?' {
				continue
			}
			end := i + 1
			for end < len(para) && strings.ContainsRune(`"')]`, rune(para[end])) {
				end++
			}
			// Closing curly quotes are multi-byte.
			for end < len(para) {
				r, size := utf8.DecodeRuneInString(para[end:])
				if r != '”' && r != '’' {
					break
				}
				end += size
			}
			if end >= len(para) || para[end] != ' ' {
				continue
			}
			next, _ := utf8.DecodeRuneInString(para[end+1:])
			if !unicode.IsUpper(next) && !unicode.IsDigit(next) {
				continue
			}
			if c == '.' && isAbbreviation(lastWord(para[start:i])) {
				continue
			}
			out = appendSentence(out, para[start:end], p)
			start = end + 1
			i = end
		}
		out = appendSentence(out, para[start:], p)
	}
	return out
}

func appendSentence(out []sentence, s string, para int) []sentence {
	if s = strings.TrimSpace(s); s == "" {
		return out
	}
	return append(out, sentence{text: s, key: normalize(s), para: para})
}

// isAbbreviation: a known abbreviation or an initial ("Jerome H. Powell").
func isAbbreviation(w string) bool {
	if r, size := utf8.DecodeRuneInString(w); size == len(w) && unicode.IsUpper(r) {
		return true
	}
	return abbreviations[strings.ToLower(w)]
}

// lastWord is the word before a dot, with inner dots kept ("U.S").
func lastWord(s string) string {
	if i := strings.LastIndexAny(s, " (\"“"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

type op struct {
	kind Kind
	i, j int
}

// lcs aligns two sequences of lengths n and m by their longest common
// subsequence and returns the edit script in order.
func lcs(n, m int, eq func(i, j int) bool) []op {
	// t[i][j] = LCS length of suffixes a[i:], b[j:].
	t := make([][]int, n+1)
	for i := range t {
		t[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if eq(i, j) {
				t[i][j] = t[i+1][j+1] + 1
			} else {
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case eq(i, j):
			ops = append(ops, op{Equal, i, j})
			i++
			j++
		case t[i+1][j] >= t[i][j+1]:
			ops = append(ops, op{Removed, i, -1})
			i++
		default:
			ops = append(ops, op{Added, -1, j})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{Removed, i, -1})
	}
	for ; j < m; j++ {
		ops = append(ops, op{Added, -1, j})
	}
	return ops
}

// Words marks the word-level changes between two sentences:
// "~~old words~~ **new words**", unchanged words as they are.
func Words(oldS, newS string) string {
	a, b := strings.Fields(normalize(oldS)), strings.Fields(normalize(newS))
	ops := lcs(len(a), len(b), func(i, j int) bool { return a[i] == b[j] })
	var out, del, ins []string
	flush := func() {
		if len(del) > 0 {
			out = append(out, "~~"+escape(strings.Join(del, " "))+"~~")
		}
		if len(ins) > 0 {
			out = append(out, "**"+escape(strings.Join(ins, " "))+"**")
		}
		del, ins = nil, nil
	}
	for _, o := range ops {
		switch o.kind {
		case Equal:
			flush()
			out = append(out, escape(a[o.i]))
		case Removed:
			del = append(del, a[o.i])
		case Added:
			ins = append(ins, b[o.j])
		}
	}
	flush()
	return strings.Join(out, " ")
}

// escape protects Markdown emphasis characters in source text.
func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `~`, `\~`, `_`, `\_`).Replace(s)
}
