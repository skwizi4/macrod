package fedtexts

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// The press conference transcript is published only as a PDF. The Fed's
// transcripts are simple PDFs: Flate-compressed page content streams
// drawing literal strings in single-byte (WinAnsi) TrueType fonts, page
// header and footer marked as /Artifact. pdfText reads exactly that with the
// standard library; a PDF it cannot read (hex-coded CID fonts, no text) is an
// error, so no garbage is stored.

var (
	streamStart = regexp.MustCompile(`stream\r?\n`)
	pageOf      = regexp.MustCompile(`Page (\d+) of \d+`)
)

// errPDFText: the PDF has no text pdfText can read.
var errPDFText = errors.New("pdf: no readable text (only single-byte fonts are supported)")

// pdfText returns the text of a PDF, one paragraph per speaker turn, pages
// in footer order ("Page N of M") or else in file order, headers and
// footers left out.
func pdfText(b []byte) (string, error) {
	type page struct {
		n    int
		text string
	}
	var pages []page
	for _, m := range streamStart.FindAllIndex(b, -1) {
		end := bytes.Index(b[m[1]:], []byte("endstream"))
		if end < 0 {
			continue
		}
		data, ok := inflate(b[m[1] : m[1]+end])
		if !ok || !isContent(data) {
			continue
		}
		body, artifacts, err := contentText(data)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(body) == "" {
			continue
		}
		n := 0
		if pm := pageOf.FindStringSubmatch(artifacts); pm != nil {
			n, _ = strconv.Atoi(pm[1])
		}
		pages = append(pages, page{n, body})
	}
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].n < pages[j].n })
	var parts []string
	for _, p := range pages {
		parts = append(parts, p.text)
	}
	text := paragraphs(strings.Join(parts, "\n"))
	letters := 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if len(text) < 200 || letters*2 < len([]rune(text)) {
		return "", errPDFText
	}
	return text, nil
}

func inflate(raw []byte) ([]byte, bool) {
	r, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, false
	}
	out, err := io.ReadAll(r)
	if err != nil && len(out) == 0 {
		return nil, false
	}
	return out, true
}

// isContent tells a page content stream with text from fonts, images and
// cross-reference data: mostly printable ASCII with text operators.
func isContent(d []byte) bool {
	if !bytes.Contains(d, []byte("BT")) || !(bytes.Contains(d, []byte("Tj")) || bytes.Contains(d, []byte("TJ"))) {
		return false
	}
	printable := 0
	for _, c := range d {
		if c == '\n' || c == '\r' || c == '\t' || (c >= 0x20 && c < 0x7f) {
			printable++
		}
	}
	return printable*100 >= len(d)*95
}

// contentText interprets the text operators of a content stream: body is
// the text outside /Artifact marked content, one line per text line;
// artifacts is the header and footer text.
func contentText(d []byte) (body, artifacts string, err error) {
	var out, art strings.Builder
	var operands []any // string, float64, []any, name (pdfName)
	var marks []bool   // marked-content stack: true for /Artifact
	inArtifact := func() bool {
		for _, a := range marks {
			if a {
				return true
			}
		}
		return false
	}
	w := func(s string) {
		if inArtifact() {
			art.WriteString(s)
		} else {
			out.WriteString(s)
		}
	}
	lex := &pdfLexer{b: d}
	for {
		tok, ok, lerr := lex.next()
		if lerr != nil {
			return "", "", lerr
		}
		if !ok {
			break
		}
		op, isOp := tok.(pdfOp)
		if !isOp {
			operands = append(operands, tok)
			continue
		}
		switch op {
		case "BDC", "BMC":
			isArt := false
			for _, o := range operands {
				if n, ok := o.(pdfName); ok && n == "Artifact" {
					isArt = true
				}
			}
			marks = append(marks, isArt)
		case "EMC":
			if len(marks) > 0 {
				marks = marks[:len(marks)-1]
			}
		case "Td", "TD":
			if len(operands) >= 2 {
				if ty, ok := operands[len(operands)-1].(float64); ok && ty != 0 {
					w("\n")
				}
			}
		case "T*":
			w("\n")
		case "Tm":
			w("\n")
		case "Tj", "'", "\"":
			if op != "Tj" {
				w("\n")
			}
			if len(operands) > 0 {
				if s, ok := operands[len(operands)-1].(string); ok {
					w(s)
				}
			}
		case "TJ":
			if len(operands) > 0 {
				if arr, ok := operands[len(operands)-1].([]any); ok {
					for _, e := range arr {
						switch v := e.(type) {
						case string:
							w(v)
						case float64:
							if v < -250 { // a gap wider than a quarter em reads as a space
								w(" ")
							}
						}
					}
				}
			}
		}
		operands = operands[:0]
	}
	return out.String(), art.String(), nil
}

// speakerTurn starts a turn in a transcript: "CHAIRMAN WARSH. ",
// "EDWARD LAWRENCE. ".
var speakerTurn = regexp.MustCompile(`\s([A-Z][A-Z'-]+(?: [A-Z][A-Z.'-]*)+)\. `)

// paragraphs joins the text lines and starts a paragraph at each speaker
// turn (line breaks in the PDF are layout, not paragraphs).
func paragraphs(s string) string {
	text := strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(speakerTurn.ReplaceAllString(text, "\n\n$1. "))
}

type (
	pdfOp   string
	pdfName string
)

// pdfLexer reads the tokens of a content stream.
type pdfLexer struct {
	b []byte
	i int
}

// next returns a string, float64, []any, pdfName or pdfOp; ok is false at
// the end.
func (l *pdfLexer) next() (tok any, ok bool, err error) {
	for l.i < len(l.b) {
		c := l.b[l.i]
		switch {
		case c == '%':
			for l.i < len(l.b) && l.b[l.i] != '\n' && l.b[l.i] != '\r' {
				l.i++
			}
		case isPDFSpace(c):
			l.i++
		case c == '(':
			l.i++
			return l.literal(), true, nil
		case c == '<' && l.i+1 < len(l.b) && l.b[l.i+1] == '<', c == '>' && l.i+1 < len(l.b) && l.b[l.i+1] == '>':
			l.i += 2 // dictionaries (marked-content properties) are skipped
		case c == '<':
			return nil, false, errPDFText // hex string: a CID font
		case c == '[':
			l.i++
			var arr []any
			for {
				for l.i < len(l.b) && isPDFSpace(l.b[l.i]) {
					l.i++
				}
				if l.i >= len(l.b) {
					return arr, true, nil
				}
				if l.b[l.i] == ']' {
					l.i++
					return arr, true, nil
				}
				t, ok, err := l.next()
				if err != nil || !ok {
					return arr, ok, err
				}
				arr = append(arr, t)
			}
		case c == ']', c == '{', c == '}', c == ')', c == '>':
			l.i++
		case c == '/':
			start := l.i + 1
			l.i++
			for l.i < len(l.b) && !isPDFSpace(l.b[l.i]) && !isPDFDelim(l.b[l.i]) {
				l.i++
			}
			return pdfName(l.b[start:l.i]), true, nil
		default:
			start := l.i
			for l.i < len(l.b) && !isPDFSpace(l.b[l.i]) && !isPDFDelim(l.b[l.i]) {
				l.i++
			}
			if l.i == start { // a lone delimiter
				l.i++
				continue
			}
			w := string(l.b[start:l.i])
			if f, err := strconv.ParseFloat(w, 64); err == nil {
				return f, true, nil
			}
			return pdfOp(w), true, nil
		}
	}
	return nil, false, nil
}

// literal reads a (string) after its "(": escapes, nested parentheses,
// WinAnsi bytes.
func (l *pdfLexer) literal() string {
	var sb strings.Builder
	depth := 1
	for l.i < len(l.b) {
		c := l.b[l.i]
		l.i++
		switch c {
		case '\\':
			if l.i >= len(l.b) {
				return sb.String()
			}
			e := l.b[l.i]
			l.i++
			switch e {
			case 'n', 'r':
				sb.WriteByte(' ')
			case 't':
				sb.WriteByte(' ')
			case 'b', 'f':
			case '\r', '\n':
				// line continuation
			default:
				if e >= '0' && e <= '7' {
					v := int(e - '0')
					for k := 0; k < 2 && l.i < len(l.b) && l.b[l.i] >= '0' && l.b[l.i] <= '7'; k++ {
						v = v*8 + int(l.b[l.i]-'0')
						l.i++
					}
					sb.WriteRune(winAnsi(byte(v)))
				} else {
					sb.WriteRune(winAnsi(e))
				}
			}
		case '(':
			depth++
			sb.WriteByte('(')
		case ')':
			depth--
			if depth == 0 {
				return sb.String()
			}
			sb.WriteByte(')')
		default:
			sb.WriteRune(winAnsi(c))
		}
	}
	return sb.String()
}

func isPDFSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0
}

func isPDFDelim(c byte) bool { return strings.IndexByte("()<>[]{}/%", c) >= 0 }

// winAnsiHigh maps WinAnsiEncoding 0x80–0x9F (Windows-1252); other bytes
// are Latin-1.
var winAnsiHigh = [32]rune{
	'€', 0, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0, 'Ž', 0,
	0, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0, 'ž', 'Ÿ',
}

func winAnsi(c byte) rune {
	if c >= 0x80 && c <= 0x9f {
		if r := winAnsiHigh[c-0x80]; r != 0 {
			return r
		}
		return ' '
	}
	return rune(c)
}
