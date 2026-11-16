package fedtexts

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"macro-collector/internal/sources/htmltext"
)

// page is a parsed federalreserve.gov document page.
type page struct {
	doc     *goquery.Document
	Title   string
	Date    time.Time // article__time, calendar day (UTC midnight); zero if absent
	Release string    // "For release at 2:00 p.m. EDT", if present
	Speaker string    // speeches and testimony
	Body    string
}

// boilerplate is press-release text that is not part of the document.
var boilerplate = regexp.MustCompile(`^(For media inquiries|Implementation Note issued)`)

// parsePage reads title, date, speaker and the cleaned body text of a page.
// The body is the article column (press releases, speeches) or the whole
// article (minutes, Beige Book).
func parsePage(b []byte) (page, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(b))
	if err != nil {
		return page{}, fmt.Errorf("parse page: %w", err)
	}
	article := doc.Find("#article").First()
	if article.Length() == 0 {
		return page{}, fmt.Errorf("parse page: no #article")
	}
	p := page{doc: doc}
	title := article.Find("h3.title").First()
	if title.Length() == 0 {
		title = article.Find("h3").First()
	}
	p.Title = htmltext.Clean(title.Text())
	if d := htmltext.Clean(article.Find(".article__time").First().Text()); d != "" {
		if p.Date, err = time.Parse("January 02, 2006", d); err != nil {
			return page{}, fmt.Errorf("parse page date %q: %w", d, err)
		}
	}
	p.Release = htmltext.Clean(article.Find(".releaseTime").First().Text())
	p.Speaker = htmltext.Clean(article.Find(".speaker").First().Text())

	body := article
	article.ChildrenFiltered("div.col-sm-8").Not(".heading").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if strings.TrimSpace(s.Text()) != "" {
			body = s
			return false
		}
		return true
	})
	p.Body = articleText(body)
	if p.Body == "" {
		return page{}, fmt.Errorf("parse page %q: empty body", p.Title)
	}
	return p, nil
}

// releaseAt combines the article date with "For release at 2:00 p.m. EDT";
// ok is false when either is missing.
func (p page) releaseAt(loc *time.Location) (time.Time, bool) {
	if p.Date.IsZero() {
		return time.Time{}, false
	}
	m := regexp.MustCompile(`(\d{1,2}):(\d{2}) ([ap])\.m\.`).FindStringSubmatch(p.Release)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.Parse("3:04 pm", m[1]+":"+m[2]+" "+m[3]+"m")
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(p.Date.Year(), p.Date.Month(), p.Date.Day(), t.Hour(), t.Minute(), 0, 0, loc), true
}

// link returns the first href in the article matching re, or "".
func (p page) link(re *regexp.Regexp) string {
	var out string
	p.doc.Find("#article a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		if h, _ := a.Attr("href"); re.MatchString(h) {
			out = h
			return false
		}
		return true
	})
	return out
}

// articleText renders sel as plain text (htmltext) after the
// federalreserve.gov cleanup: share widgets, footnote markers and footnotes
// (everything after the first <hr>) removed, press-release boilerplate
// paragraphs dropped.
func articleText(sel *goquery.Selection) string {
	sel = sel.Clone()
	sel.Find(".shareDL, li.share, a.watchLive, a#back-top, .heading").Remove()
	// Footnote markers (<a href="#fn1"><sup>1</sup></a>), back links and anchors.
	sel.Find(`a[href^="#"]`).Each(func(_ int, a *goquery.Selection) {
		if strings.Contains(a.Text(), "Return to text") || a.Find("sup").Length() > 0 || strings.TrimSpace(a.Text()) == "" {
			a.Remove()
		}
	})
	sel.Find("hr").Each(func(_ int, hr *goquery.Selection) {
		hr.NextAll().Remove()
		hr.Remove()
	})
	var paras []string
	for _, p := range htmltext.Paragraphs(sel) {
		if !boilerplate.MatchString(p) {
			paras = append(paras, p)
		}
	}
	return strings.Join(paras, "\n\n")
}
