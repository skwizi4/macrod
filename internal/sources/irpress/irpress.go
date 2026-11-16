// Package irpress reads the press releases of an issuer (EU Micro, phase
// 11, ТЗ §18.7): an RSS feed (SAP, Siemens; the body from the item) or the
// HTML list of the newsroom (Airbus; the body from the release page). Text
// only: the export takes quotes and outlook paragraphs, no numbers. Pages
// built by a script or closed to robots (Allianz: 403) are not read.
package irpress

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/htmltext"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/rss"
)

// SourcePrefix + ticker is documents.source of a release ("ir:SAP").
const SourcePrefix = "ir:"

// DocType is documents.doc_type of a release.
const DocType = "press_release"

// Getter fetches a URL (httpx.Client).
type Getter interface {
	Get(ctx context.Context, url string) ([]byte, error)
}

var _ Getter = (*httpx.Client)(nil)

// Client reads press release lists.
type Client struct {
	http Getter
	log  *slog.Logger
}

// New returns a client.
func New(h Getter, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: h, log: log}
}

// Source is documents.source of the issuer's releases.
func Source(ticker string) string { return SourcePrefix + ticker }

// Fetch returns the releases of the list published from since, oldest
// first, that pass the title filters. known reports a release URL already
// stored: its page is not fetched again (HTML lists).
func (c *Client) Fetch(ctx context.Context, ticker, region string, p config.IssuerPress, since time.Time, known func(string) bool) ([]model.Document, error) {
	body, err := c.http.Get(ctx, p.URL)
	if err != nil {
		return nil, fmt.Errorf("press %s: %w", ticker, err)
	}
	var docs []model.Document
	switch p.Kind {
	case config.PressRSS:
		docs, err = ParseFeed(body, p, since)
	default:
		var items []Item
		if items, err = ParseList(body, p, since); err != nil {
			break
		}
		for _, it := range items {
			if known != nil && known(it.URL) {
				continue
			}
			page, err := c.http.Get(ctx, it.URL)
			if err != nil {
				return nil, fmt.Errorf("press %s %s: %w", ticker, it.URL, err)
			}
			text, err := PageText(page, p.Body)
			if err != nil {
				return nil, fmt.Errorf("press %s %s: %w", ticker, it.URL, err)
			}
			if text == "" {
				c.log.Warn("release page without body text", "url", it.URL, "selector", p.Body)
				continue
			}
			docs = append(docs, model.Document{ExternalID: it.URL, Title: it.Title, URL: it.URL, PublishedAt: it.Published, Body: text})
		}
	}
	if err != nil {
		return nil, fmt.Errorf("press %s: %w", ticker, err)
	}
	for i := range docs {
		docs[i].Source, docs[i].DocType, docs[i].Region = Source(ticker), DocType, region
	}
	slices.SortFunc(docs, func(a, b model.Document) int { return a.PublishedAt.Compare(b.PublishedAt) })
	return docs, nil
}

// keep reports whether a title passes the filters.
func keep(p config.IssuerPress, title string) bool {
	if p.Title != nil && !p.Title.MatchString(title) {
		return false
	}
	return p.Excluded == nil || !p.Excluded.MatchString(title)
}

// ParseFeed reads the releases of an RSS feed: the body is the item's
// content (else its description) as text.
func ParseFeed(body []byte, p config.IssuerPress, since time.Time) ([]model.Document, error) {
	feed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}
	var out []model.Document
	for _, it := range feed.Items {
		published := it.PublishedParsed
		if published == nil {
			published = it.UpdatedParsed
		}
		title := strings.TrimSpace(it.Title)
		if published == nil || it.Link == "" || published.Before(since) || !keep(p, title) {
			continue
		}
		html := it.Content
		if strings.TrimSpace(html) == "" {
			html = it.Description
		}
		text := rss.Text(html)
		if text == "" {
			continue
		}
		out = append(out, model.Document{ExternalID: it.Link, Title: title, URL: it.Link, PublishedAt: published.UTC(), Body: text})
	}
	return out, nil
}

// Item is an entry of an HTML release list.
type Item struct {
	Title     string
	URL       string
	Published time.Time
}

// ParseList reads the entries of an HTML list page: in each Item element
// the first link matching LinkPattern, its title attribute (else the
// element's heading or the link text), the time[datetime] of the entry.
// Entries without a date are skipped.
func ParseList(body []byte, p config.IssuerPress, since time.Time) ([]Item, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse list: %w", err)
	}
	base, err := url.Parse(p.URL)
	if err != nil {
		return nil, err
	}
	var out []Item
	seen := map[string]bool{}
	doc.Find(p.Item).Each(func(_ int, s *goquery.Selection) {
		var link *goquery.Selection
		s.Find("a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
			if href, _ := a.Attr("href"); p.Link.MatchString(href) {
				link = a
				return false
			}
			return true
		})
		if link == nil {
			return
		}
		href, _ := link.Attr("href")
		u, err := base.Parse(href)
		if err != nil || seen[u.String()] {
			return
		}
		title, _ := link.Attr("title")
		if title = htmltext.Clean(title); title == "" {
			title = htmltext.Clean(s.Find("h1, h2, h3, h4").First().Text())
		}
		if title == "" {
			title = htmltext.Clean(link.Text())
		}
		dt, _ := s.Find("time[datetime]").First().Attr("datetime")
		published, err := time.Parse(time.RFC3339, dt)
		if err != nil || published.Before(since) || !keep(p, title) {
			return
		}
		seen[u.String()] = true
		out = append(out, Item{Title: title, URL: u.String(), Published: published.UTC()})
	})
	return out, nil
}

// PageText is the text of the release page's body element.
func PageText(page []byte, selector string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return "", fmt.Errorf("parse page: %w", err)
	}
	return htmltext.Text(doc.Find(selector).First()), nil
}
