package ecbtexts

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"

	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/htmltext"
)

// Source is documents.source of ECB texts and the name of their fetch.
const Source = "ecb_texts"

// Document types.
const (
	DocDecision        = "ecb_decision"         // press release "Monetary policy decisions"
	DocStatement       = "ecb_statement"        // monetary policy statement: the introduction only
	DocPressConference = "ecb_press_conference" // the Q&A of the same page
	DocAccounts        = "ecb_accounts"
	DocSpeech          = "ecb_speech"
	DocInterview       = "ecb_interview"
)

// QASuffix makes the external id of the Q&A from that of its statement.
const QASuffix = "#qa"

// kinds maps the page name prefix to the document type and the directory
// the page must be in; other pages (press releases of other kinds) are not
// kept.
var kinds = []struct {
	prefix, dir, docType string
}{
	{"ecb.mp", "/press/pr/", DocDecision},
	{"ecb.is", "/press/press_conference/", DocStatement},
	{"ecb.mg", "/press/accounts/", DocAccounts},
	{"ecb.sp", "/press/key/", DocSpeech},
	{"ecb.in", "/press/inter/", DocInterview},
}

// DocTypeOf is the document type of a page path, "" for pages not kept.
func DocTypeOf(p string) string {
	p = path.Clean("/" + p)
	if !strings.HasSuffix(p, ".en.html") {
		return ""
	}
	base := path.Base(p)
	for _, k := range kinds {
		if strings.HasPrefix(base, k.prefix) && strings.Contains(p, k.dir) {
			return k.docType
		}
	}
	return ""
}

// ExternalID is the page name without ".en.html": "ecb.mp261029~9a493adf17".
func ExternalID(p string) string { return strings.TrimSuffix(path.Base(p), ".en.html") }

// Site reads document pages.
type Site struct {
	Client   *Client
	BaseURL  string         // links are resolved against it, only their path is used
	Location *time.Location // Europe/Berlin
	// Times is the publication time of a document type after midnight,
	// used when the page is found in a list rather than the RSS feed.
	Times map[string]time.Duration
	Log   *slog.Logger
}

// local maps a link onto BaseURL by its path ("//press/…" of the feed
// included), so links never lead to another host.
func (s *Site) local(link string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return "", "", fmt.Errorf("link %q: %w", link, err)
	}
	p := path.Clean("/" + u.Path)
	b, err := url.Parse(s.BaseURL)
	if err != nil {
		return "", "", fmt.Errorf("base url %q: %w", s.BaseURL, err)
	}
	return b.ResolveReference(&url.URL{Path: p}).String(), p, nil
}

// Fetch reads the page of link into its documents: one, or two for a
// monetary policy statement whose Q&A is on the page. published is the
// feed time; zero takes the page date at the configured time of the type.
// author is the speaker named by the feed title, if any.
func (s *Site) Fetch(ctx context.Context, link string, published time.Time, title, author string) ([]model.Document, error) {
	u, p, err := s.local(link)
	if err != nil {
		return nil, err
	}
	docType := DocTypeOf(p)
	if docType == "" {
		return nil, fmt.Errorf("ecb page %s: not a kept document", p)
	}
	b, err := s.Client.Get(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("ecb page %s: %w", p, err)
	}
	pg, err := parsePage(b)
	if err != nil {
		return nil, fmt.Errorf("ecb page %s: %w", p, err)
	}
	if published.IsZero() {
		if pg.Date.IsZero() {
			return nil, fmt.Errorf("ecb page %s: no publication date", p)
		}
		published = At(pg.Date, s.Times[docType], s.Location)
	}
	switch {
	case docType == DocStatement:
		// The page heading is "PRESS CONFERENCE"; the feed names the speakers.
		title = "Monetary policy statement, " + published.In(s.Location).Format("2 January 2006")
	case docType == DocAccounts:
		title = "Account of the monetary policy meeting: " + pg.Title
	case title == "":
		title = pg.Title
	}
	if author == "" {
		author = speaker(docType, pg.Subtitle)
	}
	doc := model.Document{Source: Source, ExternalID: ExternalID(p), DocType: docType, PublishedAt: published,
		Author: author, Title: title, Body: pg.Body, URL: u}
	if docType != DocStatement {
		return []model.Document{doc}, nil
	}
	doc.Body = pg.Intro
	out := []model.Document{doc}
	if pg.QA != "" {
		qa := doc
		qa.ExternalID += QASuffix
		qa.DocType = DocPressConference
		qa.Title = "Q&A: " + title
		qa.URL = u + QASuffix
		qa.Body = pg.QA
		out = append(out, qa)
	}
	return out, nil
}

var (
	speechBy    = regexp.MustCompile(`(?:speech|remarks|lecture|statement|address|presentation) by ([^,]+),`)
	interviewOf = regexp.MustCompile(`^Interview with ([^,]+),`)
)

// speaker reads the speaker from a page subtitle: "Keynote speech by
// Philip R. Lane, Member of …", "Interview with Christine Lagarde, …".
func speaker(docType, subtitle string) string {
	var m []string
	switch docType {
	case DocSpeech:
		m = speechBy.FindStringSubmatch(subtitle)
	case DocInterview:
		m = interviewOf.FindStringSubmatch(subtitle)
	}
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// Feed reads the press RSS feed (the latest 15 items) and keeps decisions,
// statements, accounts, speeches and interviews.
//
// The cursor is the space-separated list of item ids already handled.
type Feed struct {
	Site *Site
	URL  string
}

var _ sources.TextSource = (*Feed)(nil)

// Name implements sources.TextSource; it is also the fetch_state key.
func (f *Feed) Name() string { return Source + ":rss" }

// FetchDocuments implements sources.TextSource. An item whose page fails is
// logged and left out of the next cursor, so it is retried next time.
func (f *Feed) FetchDocuments(ctx context.Context, cursor string) ([]model.Document, string, error) {
	body, err := f.Site.Client.Get(ctx, f.URL)
	if err != nil {
		return nil, cursor, fmt.Errorf("ecb rss: %w", err)
	}
	feed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil {
		return nil, cursor, fmt.Errorf("ecb rss parse: %w", err)
	}
	seen := strings.Fields(cursor)
	var next []string
	var docs []model.Document
	for _, it := range feed.Items {
		link := it.Link
		if link == "" {
			link = it.GUID
		}
		u, err := url.Parse(strings.TrimSpace(link))
		if err != nil || u.Path == "" {
			continue
		}
		id := ExternalID(u.Path)
		if slices.Contains(seen, id) {
			next = append(next, id)
			continue
		}
		docType := DocTypeOf(u.Path)
		if docType == "" {
			next = append(next, id)
			continue
		}
		var published time.Time
		if it.PublishedParsed != nil {
			published = it.PublishedParsed.UTC()
		}
		title, author := itemTitle(docType, htmltext.Clean(it.Title))
		ds, err := f.Site.Fetch(ctx, link, published, title, author)
		if err != nil {
			if ctx.Err() != nil {
				return nil, cursor, ctx.Err()
			}
			f.Site.Log.Warn("ecb item failed, will retry", "item", id, "err", err)
			continue
		}
		next = append(next, id)
		docs = append(docs, ds...)
	}
	return docs, strings.Join(next, " "), nil
}

// itemTitle splits "Philip R. Lane: AI adoption in Europe" of a speech or
// interview into title and speaker; other titles stay whole. A statement's
// "Christine Lagarde, Boris Vujčić: Monetary policy statement (with Q&A)"
// keeps the page title.
func itemTitle(docType, s string) (title, author string) {
	switch docType {
	case DocSpeech, DocInterview:
		if a, t, ok := strings.Cut(s, ": "); ok {
			return t, a
		}
	case DocStatement:
		return "", ""
	}
	return s, ""
}

// List reads a yearly list of one document type
// (/press/govcdec/mopo/<year>/html/index_include.en.html and the like):
// it catches what has left the 15 items of the feed and is the only path
// of the accounts. URL holds "{year}"; the current year is read, in
// January also the previous one.
//
// The cursor is the space-separated list of page ids already stored.
type List struct {
	Site    *Site
	DocType string
	URL     string
	Now     func() time.Time // defaults to time.Now
}

var _ sources.TextSource = (*List)(nil)

// Name implements sources.TextSource.
func (l *List) Name() string { return Source + ":list:" + l.DocType }

// FetchDocuments implements sources.TextSource.
func (l *List) FetchDocuments(ctx context.Context, cursor string) ([]model.Document, string, error) {
	now := time.Now()
	if l.Now != nil {
		now = l.Now()
	}
	now = now.In(l.Site.Location)
	years := []int{now.Year()}
	if now.Month() == time.January {
		years = append(years, now.Year()-1)
	}
	seen := strings.Fields(cursor)
	next := slices.Clone(seen)
	var docs []model.Document
	for _, y := range years {
		u, _, err := l.Site.local(strings.ReplaceAll(l.URL, "{year}", fmt.Sprint(y)))
		if err != nil {
			return nil, cursor, err
		}
		b, err := l.Site.Client.Get(ctx, u)
		if err != nil {
			return nil, cursor, fmt.Errorf("ecb list %s %d: %w", l.DocType, y, err)
		}
		links, err := ParseList(b, l.DocType)
		if err != nil {
			return nil, cursor, fmt.Errorf("ecb list %s %d: %w", l.DocType, y, err)
		}
		for _, link := range links {
			id := ExternalID(link)
			if slices.Contains(next, id) {
				continue
			}
			ds, err := l.Site.Fetch(ctx, link, time.Time{}, "", "")
			if err != nil {
				if ctx.Err() != nil {
					return nil, cursor, ctx.Err()
				}
				l.Site.Log.Warn("ecb list item failed, will retry", "item", id, "err", err)
				continue
			}
			next = append(next, id)
			docs = append(docs, ds...)
		}
	}
	return docs, strings.Join(next, " "), nil
}

// ParseList returns the English page links of docType in a yearly list,
// newest first: the link of each entry's title (the related links of an
// entry are other documents).
func ParseList(b []byte, docType string) ([]string, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("parse list: %w", err)
	}
	var out []string
	doc.Find("dd").Each(func(_ int, dd *goquery.Selection) {
		href, ok := dd.Find("div.title a[href]").First().Attr("href")
		if ok && DocTypeOf(href) == docType && !slices.Contains(out, href) {
			out = append(out, href)
		}
	})
	if len(out) == 0 && doc.Find("dt").Length() > 0 {
		return nil, fmt.Errorf("parse list: entries but no %s links (page layout changed?)", docType)
	}
	return out, nil
}

// PressConferences re-reads recent statement pages until their Q&A is
// there (it is added after the press conference). Pending lists the stored
// statements still without one. A changed introduction comes back too and
// is stored as a revision of the same document.
type PressConferences struct {
	Site    *Site
	Pending func(ctx context.Context) ([]model.Document, error)
}

var _ sources.TextSource = (*PressConferences)(nil)

// Name implements sources.TextSource.
func (p *PressConferences) Name() string { return Source + ":qa" }

// FetchDocuments implements sources.TextSource. The cursor only records
// how many statements were waiting ("pending 1"), so status shows the run.
func (p *PressConferences) FetchDocuments(ctx context.Context, cursor string) ([]model.Document, string, error) {
	pending, err := p.Pending(ctx)
	if err != nil {
		return nil, cursor, err
	}
	var docs []model.Document
	for _, st := range pending {
		ds, err := p.Site.Fetch(ctx, st.URL, st.PublishedAt, st.Title, st.Author)
		if err != nil {
			if ctx.Err() != nil {
				return nil, cursor, ctx.Err()
			}
			p.Site.Log.Warn("ecb statement re-read failed", "id", st.ExternalID, "err", err)
			continue
		}
		docs = append(docs, ds...)
	}
	return docs, fmt.Sprintf("pending %d", len(pending)), nil
}

// RecordFixtures saves the feed, the yearly lists of year, the meeting
// calendar and the two latest pages of each list under dir by URL path
// (testdata/ecb_texts), for tests without network.
func (s *Site) RecordFixtures(ctx context.Context, dir, rss, calendar string, lists map[string]string, year int) error {
	save := func(link string) ([]byte, error) {
		u, p, err := s.local(link)
		if err != nil {
			return nil, err
		}
		b, err := s.Client.Get(ctx, u)
		if err != nil {
			return nil, fmt.Errorf("record %s: %w", p, err)
		}
		dst := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(p, "/")))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		return b, os.WriteFile(dst, b, 0o644)
	}
	for _, u := range []string{rss, calendar} {
		if _, err := save(u); err != nil {
			return err
		}
	}
	for docType, tmpl := range lists {
		b, err := save(strings.ReplaceAll(tmpl, "{year}", fmt.Sprint(year)))
		if err != nil {
			return err
		}
		links, err := ParseList(b, docType)
		if err != nil {
			return err
		}
		for _, l := range links[:min(2, len(links))] {
			if _, err := save(l); err != nil {
				return err
			}
		}
	}
	return nil
}
