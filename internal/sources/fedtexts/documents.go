package fedtexts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/mmcdole/gofeed"
	"macro-collector/internal/sources/htmltext"

	"macro-collector/internal/model"
	"macro-collector/internal/sources"
)

// documents.source of Fed texts.
const Source = "fed"

// documents.doc_type values.
const (
	DocStatement = "fomc_statement"
	DocMinutes   = "minutes"
	DocSEP       = "sep"
	DocSpeech    = "speech"
	DocTestimony = "testimony"
	DocBeigeBook = "beige_book"
)

// Feed names in config.fed_texts.feeds.
const (
	FeedPressMonetary = "press_monetary"
	FeedSpeeches      = "speeches"
	FeedTestimony     = "testimony"
)

var (
	minutesLink   = regexp.MustCompile(`/monetarypolicy/fomcminutes\d{8}\.htm$`)
	sepLink       = regexp.MustCompile(`/monetarypolicy/fomcprojtabl\d{8}\.htm$`)
	beigeBookLink = regexp.MustCompile(`/monetarypolicy/beigebook\d{6}-summary\.htm$`)
)

// Feed reads one federalreserve.gov RSS feed. Press releases are kept only
// if they are an FOMC statement, minutes or projections (ТЗ §12.2);
// speeches and testimony only if given by a Board member.
//
// The cursor is the space-separated list of item ids already handled. The
// feeds hold the latest 15 items and their pubDate is not posting time, so
// "id not seen" is the reliable test for new items.
type Feed struct {
	Client   *Client
	FeedName string // FeedPressMonetary | FeedSpeeches | FeedTestimony
	URL      string
	BaseURL  string         // resolves relative links
	Location *time.Location // America/New_York
	Log      *slog.Logger
}

var _ sources.TextSource = (*Feed)(nil)

// Name implements sources.TextSource; it is also the fetch_state key.
func (f *Feed) Name() string { return Source + ":" + f.FeedName }

// FetchDocuments implements sources.TextSource. An item whose page fails is
// logged and left out of the next cursor, so it is retried next time.
func (f *Feed) FetchDocuments(ctx context.Context, cursor string) ([]model.Document, string, error) {
	switch f.FeedName {
	case FeedPressMonetary, FeedSpeeches, FeedTestimony:
	default:
		return nil, cursor, fmt.Errorf("fed feed %s: unknown feed", f.FeedName)
	}
	body, err := f.Client.Get(ctx, f.URL)
	if err != nil {
		return nil, cursor, fmt.Errorf("fed feed %s: %w", f.FeedName, err)
	}
	feed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil {
		return nil, cursor, fmt.Errorf("fed feed %s parse: %w", f.FeedName, err)
	}
	seen := strings.Fields(cursor)
	var next []string
	var docs []model.Document
	for _, it := range feed.Items {
		id := itemID(it)
		if id == "" {
			continue
		}
		if slices.Contains(seen, id) {
			next = append(next, id)
			continue
		}
		doc, ok, err := f.document(ctx, it)
		if err != nil {
			if ctx.Err() != nil {
				return nil, cursor, ctx.Err()
			}
			f.Log.Warn("fed item failed, will retry", "feed", f.FeedName, "item", id, "err", err)
			continue
		}
		next = append(next, id)
		if ok {
			docs = append(docs, doc)
		}
	}
	return docs, strings.Join(next, " "), nil
}

// itemID is the page name of the item link: "monetary20260916a", "waller20261008a".
func itemID(it *gofeed.Item) string {
	link := it.Link
	if link == "" {
		link = it.GUID
	}
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(path.Base(u.Path), ".htm")
}

// document builds the Document of a feed item; ok is false for press
// releases we do not keep.
func (f *Feed) document(ctx context.Context, it *gofeed.Item) (model.Document, bool, error) {
	published := time.Time{}
	if it.PublishedParsed != nil {
		published = it.PublishedParsed.UTC()
	}
	title := htmltext.Clean(it.Title)
	switch f.FeedName {
	case FeedSpeeches, FeedTestimony:
		docType := DocSpeech
		if f.FeedName == FeedTestimony {
			docType = DocTestimony
		}
		d, err := f.fetch(ctx, it.Link, docType, published)
		if err != nil {
			return d, false, err
		}
		if !boardMember(d.Author) {
			f.Log.Info("fed item skipped: not a Board member", "feed", f.FeedName, "speaker", d.Author)
			return d, false, nil
		}
		return d, true, nil
	}
	switch {
	case strings.Contains(title, "issues FOMC statement"):
		d, err := f.statement(ctx, it.Link, published)
		return d, err == nil, err
	case strings.HasPrefix(title, "Minutes of the Federal Open Market Committee"):
		return f.linked(ctx, it.Link, minutesLink, DocMinutes, title, published, false)
	case strings.Contains(title, "release economic projections"):
		return f.linked(ctx, it.Link, sepLink, DocSEP, title, published, true)
	}
	return model.Document{}, false, nil
}

// local maps an item link onto BaseURL: only its path is used, so items
// never lead to another host (and tests can serve them).
func (f *Feed) local(link string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return "", fmt.Errorf("item link %q: %w", link, err)
	}
	return resolve(f.BaseURL, u.EscapedPath())
}

// boardMember reports whether a speaker line names a Board member
// ("Governor …", "Chair …", "Chairman …", "Vice Chair …"); staff testimony
// is left out (decision 2026-10-24: Board only).
func boardMember(speaker string) bool {
	for _, p := range []string{"Governor ", "Chair ", "Chairman ", "Vice Chair "} {
		if strings.HasPrefix(speaker, p) {
			return true
		}
	}
	return false
}

// fetch reads a page into a Document.
func (f *Feed) fetch(ctx context.Context, link, docType string, published time.Time) (model.Document, error) {
	u, err := f.local(link)
	if err != nil {
		return model.Document{}, err
	}
	b, err := f.Client.Get(ctx, u)
	if err != nil {
		return model.Document{}, err
	}
	p, err := parsePage(b)
	if err != nil {
		return model.Document{}, fmt.Errorf("%s: %w", u, err)
	}
	return newDocument(u, docType, p.Title, p.Speaker, p.Body, publishedAt(published, p.Date, f.Location)), nil
}

// publishedAt is the feed time, or the page date (midnight ET) when the
// feed has none or a placeholder: the testimony feed carries
// "Sat, 30 Dec 1899" for some items.
func publishedAt(feed, pageDate time.Time, loc *time.Location) time.Time {
	if feed.Year() >= 2000 || pageDate.IsZero() {
		return feed
	}
	return time.Date(pageDate.Year(), pageDate.Month(), pageDate.Day(), 0, 0, 0, 0, loc).UTC()
}

// statement reads an FOMC statement; its time is the page's release time.
func (f *Feed) statement(ctx context.Context, link string, published time.Time) (model.Document, error) {
	u, err := f.local(link)
	if err != nil {
		return model.Document{}, err
	}
	b, err := f.Client.Get(ctx, u)
	if err != nil {
		return model.Document{}, err
	}
	return statementDocument(u, b, published, f.Location)
}

// linked reads a press release that announces another page (minutes, SEP
// tables) and stores that page under its own id. For the SEP only Table 1
// (medians, central tendencies, ranges) is kept, after the release text.
func (f *Feed) linked(ctx context.Context, link string, target *regexp.Regexp, docType, title string,
	published time.Time, sep bool) (model.Document, bool, error) {
	u, err := f.local(link)
	if err != nil {
		return model.Document{}, false, err
	}
	b, err := f.Client.Get(ctx, u)
	if err != nil {
		return model.Document{}, false, err
	}
	release, err := parsePage(b)
	if err != nil {
		return model.Document{}, false, fmt.Errorf("%s: %w", u, err)
	}
	href := release.link(target)
	if href == "" {
		return model.Document{}, false, fmt.Errorf("%s: no link matching %s", u, target)
	}
	tu, err := resolve(u, href)
	if err != nil {
		return model.Document{}, false, err
	}
	tb, err := f.Client.Get(ctx, tu)
	if err != nil {
		return model.Document{}, false, err
	}
	if !sep {
		p, err := parsePage(tb)
		if err != nil {
			return model.Document{}, false, fmt.Errorf("%s: %w", tu, err)
		}
		return newDocument(tu, docType, title, "", p.Body, publishedAt(published, release.Date, f.Location)), true, nil
	}
	table, err := sepTable(tb)
	if err != nil {
		return model.Document{}, false, fmt.Errorf("%s: %w", tu, err)
	}
	return newDocument(tu, docType, title, "", firstParagraph(release.Body)+"\n\n"+table,
		publishedAt(published, release.Date, f.Location)), true, nil
}

// sepTable renders Table 1 of the projections page.
func sepTable(b []byte) (string, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(b))
	if err != nil {
		return "", fmt.Errorf("parse projections: %w", err)
	}
	t := doc.Find("div.data-table").FilterFunction(func(_ int, s *goquery.Selection) bool {
		return strings.HasPrefix(htmltext.Clean(s.Find("h4").First().Text()), "Table 1.")
	}).First()
	if t.Length() == 0 {
		return "", errors.New("projections: no Table 1")
	}
	return articleText(t), nil
}

func firstParagraph(s string) string {
	p, _, _ := strings.Cut(s, "\n\n")
	return p
}

// FetchStatement reads the FOMC statement of the decision on day directly
// (newsevents/pressreleases/monetaryYYYYMMDDa.htm), for FOMC-day polling.
// It returns an error wrapping ErrNotFound until the page is published.
func FetchStatement(ctx context.Context, c *Client, baseURL string, day time.Time, loc *time.Location) (model.Document, error) {
	u, err := resolve(baseURL, "/newsevents/pressreleases/monetary"+day.Format("20060102")+"a.htm")
	if err != nil {
		return model.Document{}, err
	}
	b, err := c.Get(ctx, u)
	if err != nil {
		return model.Document{}, fmt.Errorf("fomc statement %s: %w", day.Format(time.DateOnly), err)
	}
	return statementDocument(u, b, time.Time{}, loc)
}

// statementDocument checks that the page is an FOMC statement and dates it
// by its "For release at" time, falling back to the feed time.
func statementDocument(u string, b []byte, fallback time.Time, loc *time.Location) (model.Document, error) {
	p, err := parsePage(b)
	if err != nil {
		return model.Document{}, fmt.Errorf("%s: %w", u, err)
	}
	if !strings.Contains(p.Title, "FOMC statement") {
		return model.Document{}, fmt.Errorf("%s: not an FOMC statement: %q", u, p.Title)
	}
	published := fallback
	if t, ok := p.releaseAt(loc); ok {
		published = t.UTC()
	}
	if published.IsZero() {
		return model.Document{}, fmt.Errorf("%s: no release time", u)
	}
	return newDocument(u, DocStatement, p.Title, "", p.Body, published), nil
}

func newDocument(u, docType, title, author, body string, published time.Time) model.Document {
	return model.Document{
		Source: Source, ExternalID: strings.TrimSuffix(path.Base(u), ".htm"), DocType: docType,
		PublishedAt: published, Author: author, Title: title, Body: body, URL: u,
	}
}

func resolve(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("base url %q: %w", base, err)
	}
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", fmt.Errorf("link %q: %w", ref, err)
	}
	return b.ResolveReference(r).String(), nil
}

// BeigeBook reads the latest Beige Book (national summary) linked from the
// schedule page. The cursor is the id of the last one stored.
type BeigeBook struct {
	Client   *Client
	URL      string // schedule page, config fed_texts.beige_book
	Location *time.Location
}

var _ sources.TextSource = (*BeigeBook)(nil)

// Name implements sources.TextSource.
func (s *BeigeBook) Name() string { return Source + ":" + DocBeigeBook }

// FetchDocuments implements sources.TextSource.
func (s *BeigeBook) FetchDocuments(ctx context.Context, cursor string) ([]model.Document, string, error) {
	b, err := s.Client.Get(ctx, s.URL)
	if err != nil {
		return nil, cursor, fmt.Errorf("beige book: %w", err)
	}
	href, day, err := latestBeigeBook(b)
	if err != nil {
		return nil, cursor, err
	}
	u, err := resolve(s.URL, href)
	if err != nil {
		return nil, cursor, err
	}
	id := strings.TrimSuffix(path.Base(u), "-summary.htm")
	if id == cursor {
		return nil, cursor, nil
	}
	pb, err := s.Client.Get(ctx, u)
	if err != nil {
		return nil, cursor, fmt.Errorf("beige book: %w", err)
	}
	p, err := parsePage(pb)
	if err != nil {
		return nil, cursor, fmt.Errorf("beige book %s: %w", u, err)
	}
	published := time.Date(day.Year(), day.Month(), day.Day(), releaseHour, 0, 0, 0, s.Location).UTC()
	d := newDocument(u, DocBeigeBook, "Beige Book — "+p.Title+", "+day.Format("January 2006"), "", p.Body, published)
	d.ExternalID = id
	return []model.Document{d}, id, nil
}

// latestBeigeBook returns the summary link and date of the newest
// published Beige Book on the schedule page.
func latestBeigeBook(page []byte) (href string, day time.Time, err error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("beige book parse: %w", err)
	}
	doc.Find("table").Each(func(_ int, table *goquery.Selection) {
		year := strings.TrimSpace(table.Find("th#year").Text())
		table.Find("tbody td").Each(func(_ int, td *goquery.Selection) {
			a := td.Find("a[href]").FilterFunction(func(_ int, a *goquery.Selection) bool {
				h, _ := a.Attr("href")
				return beigeBookLink.MatchString(h)
			}).First()
			if a.Length() == 0 {
				return // not published yet
			}
			text, _, _ := strings.Cut(strings.TrimSpace(td.Text()), ":")
			d, perr := time.Parse("January 2 2006", strings.TrimSpace(text)+" "+year)
			if perr != nil || !d.After(day) {
				return
			}
			day = d
			href, _ = a.Attr("href")
		})
	})
	if href == "" {
		return "", time.Time{}, errors.New("beige book: no published edition found")
	}
	return href, day, nil
}
