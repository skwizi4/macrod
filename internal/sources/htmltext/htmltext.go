// Package htmltext turns an HTML fragment into plain text: paragraphs
// separated by blank lines, table rows as "cell | cell", collapsed
// whitespace. Site-specific cleanup (share widgets, footnotes, boilerplate)
// belongs to the source, before or after this step.
package htmltext

import (
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

var (
	spaces     = regexp.MustCompile(`[ \t\x{a0}]+`)
	blankLines = regexp.MustCompile(`\n{3,}`)
)

var blocks = map[string]bool{
	"p": true, "div": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "table": true, "blockquote": true, "section": true,
	"thead": true, "tbody": true, "caption": true,
}

// Paragraphs renders sel (scripts and styles excluded) and returns its
// non-empty paragraphs, lines within a paragraph joined by "\n". sel is
// not modified.
func Paragraphs(sel *goquery.Selection) []string {
	sel = sel.Clone()
	sel.Find("script, style, noscript").Remove()
	var b strings.Builder
	for _, n := range sel.Nodes {
		render(&b, n)
	}
	var paras []string
	for _, para := range strings.Split(blankLines.ReplaceAllString(b.String(), "\n\n"), "\n\n") {
		var lines []string
		for _, l := range strings.Split(para, "\n") {
			if l = Clean(l); l != "" {
				lines = append(lines, l)
			}
		}
		if text := strings.Join(lines, "\n"); text != "" {
			paras = append(paras, text)
		}
	}
	return paras
}

// Text is Paragraphs joined by blank lines.
func Text(sel *goquery.Selection) string { return strings.Join(Paragraphs(sel), "\n\n") }

// Clean collapses whitespace (including &nbsp;) and trims.
func Clean(s string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(strings.ReplaceAll(s, "\n", " "), " "))
}

func render(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(strings.ReplaceAll(n.Data, "\n", " "))
		return
	case html.ElementNode:
	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			render(b, c)
		}
		return
	}
	switch n.Data {
	case "br":
		b.WriteString("\n")
		return
	case "tr":
		var cells []string
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") {
				var cb strings.Builder
				render(&cb, c)
				cells = append(cells, Clean(cb.String()))
			}
		}
		b.WriteString("\n" + strings.Join(cells, " | ") + "\n")
		return
	}
	if blocks[n.Data] {
		b.WriteString("\n\n")
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		render(b, c)
	}
	if blocks[n.Data] {
		b.WriteString("\n\n")
	}
}
