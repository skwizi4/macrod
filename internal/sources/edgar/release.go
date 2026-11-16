package edgar

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	nethtml "golang.org/x/net/html"
)

// PressRelease is the Ex-99.1 exhibit of an earnings 8-K, as plain text.
type PressRelease struct {
	Accession string
	Filename  string
	URL       string
	Text      string // paragraphs separated by blank lines; tables and images left out
}

// FilingURL is the folder of a filing in the archive.
func (c *Client) FilingURL(cik, accession string) string {
	n, _ := strconv.ParseInt(cik, 10, 64)
	return fmt.Sprintf("%s/%d/%s", c.archivesURL, n, strings.ReplaceAll(accession, "-", ""))
}

// PressRelease fetches the filing's Ex-99.1. Exhibit file names are free
// form (msft-ex99_1.htm, exhibit9914.htm, q2fy27pr.htm), so the document
// type comes from the filing's index-headers page.
func (c *Client) PressRelease(ctx context.Context, cik, accession string) (PressRelease, error) {
	base := c.FilingURL(cik, accession)
	hdr, err := c.get(ctx, base+"/"+accession+"-index-headers.html")
	if err != nil {
		return PressRelease{}, fmt.Errorf("edgar %s index: %w", accession, err)
	}
	name, ok := ExhibitFile(hdr, "EX-99.1")
	if !ok {
		return PressRelease{}, fmt.Errorf("edgar %s: no EX-99.1 in the filing", accession)
	}
	body, err := c.get(ctx, base+"/"+name)
	if err != nil {
		return PressRelease{}, fmt.Errorf("edgar %s %s: %w", accession, name, err)
	}
	text, err := ReleaseText(body)
	if err != nil {
		return PressRelease{}, fmt.Errorf("edgar %s %s: %w", accession, name, err)
	}
	return PressRelease{Accession: accession, Filename: name, URL: base + "/" + name, Text: text}, nil
}

// headerDoc matches a document entry of the SGML header shown escaped on
// the index-headers page: <TYPE>EX-99.1 <SEQUENCE>2 <FILENAME>msft-ex99_1.htm.
var headerDoc = regexp.MustCompile(`<TYPE>([^\s<]+)\s*<SEQUENCE>\d+\s*<FILENAME>([^\s<]+)`)

// ExhibitFile returns the file name of the first document of the type.
func ExhibitFile(indexHeaders []byte, docType string) (string, bool) {
	for _, m := range headerDoc.FindAllStringSubmatch(html.UnescapeString(string(indexHeaders)), -1) {
		if m[1] == docType {
			return m[2], true
		}
	}
	return "", false
}

var (
	skipTags = map[string]bool{"script": true, "style": true, "head": true, "table": true, "img": true, "svg": true}
	// Block elements end a paragraph. EDGAR exhibits are mostly <div>/<p>
	// with inline <span>/<font>.
	blockTags = map[string]bool{
		"p": true, "div": true, "li": true, "ul": true, "ol": true, "tr": true, "center": true, "blockquote": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "section": true, "body": true,
	}
	spaceRun = regexp.MustCompile(`[\s\x{00a0}\x{200b}]+`)
	// Lines without content: page numbers, rules of underscores, "Exhibit
	// 99.1", and the SGML document line EDGAR puts first ("EX-99.1 2
	// msft-ex99_1.htm EX-99.1 Document").
	noiseLine = regexp.MustCompile(`(?i)^(\d{1,3}|[_\-–—=*\s]+|exhibit\s+99\.\d|EX-\d+(\.\d+)?\s+\d+\s+\S+\.html?\b.*)$`)
	bullet    = regexp.MustCompile(`^•\s*`)
)

// ReleaseText renders an exhibit as plain text: one paragraph per block,
// list items with "• ", tables (financial statements) and images dropped.
// Unlike fedtexts.ArticleText nothing is cut at <hr>: in EDGAR documents
// <hr> is a page break.
func ReleaseText(page []byte) (string, error) {
	doc, err := nethtml.Parse(bytes.NewReader(page))
	if err != nil {
		return "", fmt.Errorf("parse html: %w", err)
	}
	var b strings.Builder
	renderRelease(&b, doc)
	var paras []string
	for _, p := range strings.Split(b.String(), "\n") {
		p = strings.TrimSpace(spaceRun.ReplaceAllString(p, " "))
		if p == "" || p == "•" || noiseLine.MatchString(p) {
			continue
		}
		paras = append(paras, bullet.ReplaceAllString(p, "• "))
	}
	return strings.Join(paras, "\n\n"), nil
}

func renderRelease(b *strings.Builder, n *nethtml.Node) {
	switch n.Type {
	case nethtml.TextNode:
		b.WriteString(strings.ReplaceAll(n.Data, "\n", " "))
		return
	case nethtml.ElementNode:
		if skipTags[n.Data] || hidden(n) {
			return
		}
		switch n.Data {
		case "br", "hr":
			b.WriteString("\n")
			return
		case "li":
			b.WriteString("\n• ")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderRelease(b, c)
			}
			b.WriteString("\n")
			return
		}
	}
	block := n.Type == nethtml.ElementNode && blockTags[n.Data]
	if block {
		b.WriteString("\n")
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderRelease(b, c)
	}
	if block {
		b.WriteString("\n")
	}
}

// hidden reports display:none (inline XBRL headers, hidden facts).
func hidden(n *nethtml.Node) bool {
	for _, a := range n.Attr {
		if a.Key == "style" && strings.Contains(strings.ReplaceAll(strings.ToLower(a.Val), " ", ""), "display:none") {
			return true
		}
	}
	return n.Data == "ix:header"
}
