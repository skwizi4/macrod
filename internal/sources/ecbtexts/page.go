package ecbtexts

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"macro-collector/internal/sources/htmltext"
)

// page is a parsed ecb.europa.eu document page: the <main> column with
// div.title (h1) and div.section (subtitle, publication date, text).
type page struct {
	Title    string
	Subtitle string    // "Keynote speech by Philip R. Lane, …"
	Date     time.Time // publication day (UTC midnight); zero if absent
	Body     string    // the whole text, footnotes left out
	// Intro and QA split a monetary policy statement at the #qa anchor;
	// QA is empty until the transcript is added to the page.
	Intro, QA string
}

var pageDate = regexp.MustCompile(`(\d{1,2} [A-Z][a-z]+ \d{4})`)

// parsePage reads a document page.
func parsePage(b []byte) (page, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(b))
	if err != nil {
		return page{}, fmt.Errorf("parse page: %w", err)
	}
	main := doc.Find("main").First()
	section := main.Find("div.section").First()
	if section.Length() == 0 {
		return page{}, fmt.Errorf("parse page: no div.section")
	}
	p := page{Title: htmltext.Clean(main.Find("div.title h1").First().Text())}
	p.Subtitle = htmltext.Clean(section.Find("h2.ecb-pressContentSubtitle").First().Text())
	if m := pageDate.FindString(htmltext.Clean(section.Find("p.ecb-publicationDate").First().Text())); m != "" {
		if p.Date, err = time.Parse("2 January 2006", m); err != nil {
			return page{}, fmt.Errorf("parse page date %q: %w", m, err)
		}
	}
	section = clean(section)
	p.Body = htmltext.Text(section)
	if p.Body == "" {
		return page{}, fmt.Errorf("parse page %q: empty body", p.Title)
	}
	p.Intro, p.QA = p.Body, ""
	if top := qaParagraph(section); top != nil {
		p.QA = htmltext.Text(top.NextAll())
		intro := section.Clone()
		it := qaParagraph(intro)
		it.NextAll().Remove()
		it.Remove()
		p.Intro = htmltext.Text(intro)
	}
	return p, nil
}

// qaParagraph is the child of section holding the #qa anchor ("* * *"),
// or nil.
func qaParagraph(section *goquery.Selection) *goquery.Selection {
	top := section.Find("a#qa").First()
	if top.Length() == 0 {
		return nil
	}
	for top.Parent().Length() > 0 && !top.Parent().IsSelection(section) {
		top = top.Parent()
	}
	return top
}

// clean returns a copy of the section without its header lines, the
// jump link to the Q&A, footnote markers and the footnotes.
func clean(section *goquery.Selection) *goquery.Selection {
	s := section.Clone()
	s.Find("h2.ecb-pressContentSubtitle, p.ecb-publicationDate, div.footnotes, script, style").Remove()
	s.Find(`a[href="#qa"]`).Each(func(_ int, a *goquery.Selection) {
		if p := a.Closest("p"); p.Length() > 0 && strings.TrimSpace(p.Text()) == strings.TrimSpace(a.Text()) {
			p.Remove()
			return
		}
		a.Remove()
	})
	s.Find(`a[href^="#footnote"]`).Each(func(_ int, a *goquery.Selection) {
		if sup := a.Closest("sup"); sup.Length() > 0 {
			sup.Remove()
			return
		}
		a.Remove()
	})
	return s
}
