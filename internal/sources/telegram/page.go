package telegram

import (
	"bytes"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// post is one message of a t.me/s/ page.
type post struct {
	Handle        string // channel username as Telegram spells it (data-post)
	ID            int64
	Published     time.Time
	Text          string // empty for media without a caption and service messages
	ForwardedFrom string // "<handle>/<id>" of the original, or the sender name without a link
	Views         int64  // views counter ("5.61K" = 5610); 0 if the page shows none
	HasPhoto      bool   // a photo or an album with photos
}

// page is a parsed t.me/s/ page (LOOKUP G1, G2).
type page struct {
	Posts  []post
	Before int64 // data-before of the "load older" marker; 0 if there are no older posts
}

var (
	spaces     = regexp.MustCompile(`[ \t\x{a0}]+`)
	blankLines = regexp.MustCompile(`\n{3,}`)
)

func parsePage(b []byte) (page, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(b))
	if err != nil {
		return page{}, fmt.Errorf("parse page: %w", err)
	}
	var p page
	var perr error
	doc.Find("div.tgme_widget_message[data-post]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		ref, _ := s.Attr("data-post")
		handle, id, ok := splitRef(ref)
		if !ok {
			perr = fmt.Errorf("parse page: bad data-post %q", ref)
			return false
		}
		dt, _ := s.Find(".tgme_widget_message_date time[datetime]").First().Attr("datetime")
		published, err := time.Parse(time.RFC3339, dt)
		if err != nil {
			perr = fmt.Errorf("parse page: post %s date %q: %w", ref, dt, err)
			return false
		}
		// .js-message_text is the post itself; the quoted post of a reply
		// is .js-message_reply_text and is not part of this post.
		text := ""
		// Service messages ("… pinned a photo") have a text block too.
		if t := s.Find(".js-message_text").First(); t.Length() > 0 && !s.HasClass("service_message") {
			text = nodeText(t)
		}
		p.Posts = append(p.Posts, post{
			Handle: handle, ID: id, Published: published.UTC(), Text: text,
			ForwardedFrom: forwardedFrom(s), Views: parseViews(s.Find(".tgme_widget_message_views").First().Text()),
			HasPhoto: s.Find(".tgme_widget_message_photo_wrap").Length() > 0,
		})
		return true
	})
	if perr != nil {
		return page{}, perr
	}
	if v, ok := doc.Find(".tme_messages_more[data-before]").First().Attr("data-before"); ok {
		if p.Before, err = strconv.ParseInt(v, 10, 64); err != nil {
			return page{}, fmt.Errorf("parse page: data-before %q: %w", v, err)
		}
	}
	return p, nil
}

// parseViews reads the views counter as Telegram prints it: "843",
// "5.61K", "1.2M". An unreadable counter is 0 (unknown).
func parseViews(s string) int64 {
	s = strings.TrimSpace(s)
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1e3, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1e6, strings.TrimSuffix(s, "M")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0
	}
	return int64(math.Round(v * mult))
}

// splitRef splits "<handle>/<id>".
func splitRef(ref string) (handle string, id int64, ok bool) {
	handle, num, found := strings.Cut(ref, "/")
	if !found || handle == "" {
		return "", 0, false
	}
	id, err := strconv.ParseInt(num, 10, 64)
	return handle, id, err == nil && id > 0
}

// forwardedFrom is the original post of a forward, "<handle>/<id>" from the
// link (the dedup key of reposts), or the sender name when there is no
// post link (forwards from users or private chats).
func forwardedFrom(s *goquery.Selection) string {
	f := s.Find(".tgme_widget_message_forwarded_from_name").First()
	if f.Length() == 0 {
		return ""
	}
	if href, ok := f.Attr("href"); ok {
		if u, err := url.Parse(href); err == nil {
			if _, _, ok := splitRef(strings.Trim(u.Path, "/")); ok {
				return strings.Trim(u.Path, "/")
			}
		}
	}
	return cleanText(f.Text())
}

// nodeText is the visible text of a message: <br> become line breaks,
// emoji (<i class="emoji"><b>🔥</b></i>) and link texts stay as text.
func nodeText(s *goquery.Selection) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.TextNode:
			b.WriteString(n.Data)
		case n.Type == html.ElementNode && n.Data == "br":
			b.WriteByte('\n')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range s.Nodes {
		walk(n)
	}
	return cleanText(b.String())
}

func cleanText(s string) string {
	lines := strings.Split(spaces.ReplaceAllString(s, " "), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
