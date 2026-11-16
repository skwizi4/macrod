// Package rss reads RSS and Atom feeds of authors and news sites (Substack
// and the like; LOOKUP R1, 2026-10-25). A feed carries only its latest
// items, so history is whatever the feed holds within history_days.
package rss

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
)

// DocType is documents.doc_type of feed items.
const DocType = "article"

// SourcePrefix + name is the source name (documents.source, fetch_state key).
const SourcePrefix = "rss:"

// Feed is a sources.TextSource for one feed.
type Feed struct {
	cfg  config.RSSFeed
	http *httpx.Client
	log  *slog.Logger
	Now  func() time.Time // defaults to time.Now
}

// New returns the source of feed f. HistoryDays of f must be resolved
// (config.Load does that).
func New(f config.RSSFeed, client *httpx.Client, log *slog.Logger) *Feed {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Feed{cfg: f, http: client, log: log}
}

// Name is "rss:<name>".
func (f *Feed) Name() string { return SourcePrefix + f.cfg.Name }

// FetchDocuments returns the feed items published since cursor (RFC 3339
// time of the newest item seen) or, on the first fetch, within
// history_days, and the new cursor. Items already stored come back as
// unchanged; an edited article is updated.
func (f *Feed) FetchDocuments(ctx context.Context, cursor string) ([]model.Document, string, error) {
	since := f.now().AddDate(0, 0, -f.cfg.HistoryDays)
	if cursor != "" {
		t, err := time.Parse(time.RFC3339, cursor)
		if err != nil {
			return nil, "", fmt.Errorf("rss %s: bad cursor %q: %w", f.cfg.Name, cursor, err)
		}
		since = t
	}
	body, err := f.http.Get(ctx, f.cfg.URL)
	if err != nil {
		return nil, "", fmt.Errorf("rss %s: %w", f.cfg.Name, err)
	}
	feed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("rss %s: parse: %w", f.cfg.Name, err)
	}
	var docs []model.Document
	newest := since
	for _, it := range feed.Items {
		d, ok := f.document(feed, it)
		if !ok || d.PublishedAt.Before(since) {
			continue
		}
		docs = append(docs, d)
		if d.PublishedAt.After(newest) {
			newest = d.PublishedAt
		}
	}
	slices.SortFunc(docs, func(a, b model.Document) int { return a.PublishedAt.Compare(b.PublishedAt) })
	next := cursor
	if len(docs) > 0 {
		next = newest.Format(time.RFC3339)
	}
	return docs, next, nil
}

func (f *Feed) document(feed *gofeed.Feed, it *gofeed.Item) (model.Document, bool) {
	id := it.GUID
	if id == "" {
		id = it.Link
	}
	published := it.PublishedParsed
	if published == nil {
		published = it.UpdatedParsed
	}
	if id == "" || published == nil {
		f.log.Warn("feed item without id or date skipped", "feed", f.cfg.Name, "title", it.Title)
		return model.Document{}, false
	}
	body := it.Content
	if strings.TrimSpace(body) == "" {
		body = it.Description
	}
	text := htmlText(body)
	if text == "" {
		return model.Document{}, false
	}
	return model.Document{
		Source:      f.Name(),
		ExternalID:  id,
		DocType:     DocType,
		Region:      f.cfg.Region,
		PublishedAt: published.UTC(),
		Author:      author(feed, it),
		Title:       strings.TrimSpace(it.Title),
		Body:        text,
		URL:         it.Link,
		IsOpinion:   f.cfg.Type == "opinion",
	}, true
}

// author is the item author (dc:creator on Substack), else the feed title.
func author(feed *gofeed.Feed, it *gofeed.Item) string {
	if it.DublinCoreExt != nil && len(it.DublinCoreExt.Creator) > 0 && it.DublinCoreExt.Creator[0] != "" {
		return it.DublinCoreExt.Creator[0]
	}
	if len(it.Authors) > 0 && it.Authors[0].Name != "" {
		return it.Authors[0].Name
	}
	return strings.TrimSpace(feed.Title)
}

// Not article text: Substack subscribe/share buttons (LOOKUP R1), image
// widgets, embedded media.
const dropSelector = "script, style, noscript, svg, button, picture, img, iframe, " +
	".button-wrapper, .subscription-widget-wrap, .subscription-widget, .image-link-expand"

var blockTags = map[string]bool{
	"p": true, "div": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "blockquote": true, "figure": true, "figcaption": true,
	"table": true, "tr": true, "pre": true, "hr": true, "section": true,
}

var (
	spaces     = regexp.MustCompile(`[ \t\x{a0}]+`)
	blankLines = regexp.MustCompile(`\n{3,}`)
)

// htmlText converts article HTML to plain text: blocks become paragraphs,
// list items start with "• ", images and buttons are dropped. Unlike
// fedtexts.ArticleText it keeps text after <hr>, which Substack uses as a
// section break.
// Text is the plain text of an item's HTML body, without share buttons,
// images and embedded media; the issuer press feeds read their items with
// it too (phase 11).
func Text(s string) string { return htmlText(s) }

func htmlText(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(s))
	if err != nil {
		return ""
	}
	doc.Find(dropSelector).Remove()
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.TextNode:
			b.WriteString(strings.ReplaceAll(n.Data, "\n", " "))
			return
		case n.Type == html.ElementNode && n.Data == "br":
			b.WriteByte('\n')
			return
		}
		block := n.Type == html.ElementNode && blockTags[n.Data]
		if block {
			b.WriteString("\n\n")
			if n.Data == "li" {
				b.WriteString("• ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if block {
			b.WriteString("\n\n")
		}
	}
	for _, n := range doc.Find("body").Nodes {
		walk(n)
	}
	lines := strings.Split(spaces.ReplaceAllString(b.String(), " "), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	text := blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	// "• " alone on a line when the item's text was in a nested <p>.
	text = strings.ReplaceAll(text, "•\n\n", "• ")
	return strings.TrimSpace(text)
}

func (f *Feed) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}
