// Package telegram reads public Telegram channels through their web
// preview t.me/s/<handle> (mode web, D12; LOOKUP G1–G4, 2026-10-25). No
// account or API key is needed. A page holds the last 20 posts; ?before=<id>
// pages back in history and ?after=<id> forward to newer posts.
package telegram

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
)

// DocType is documents.doc_type of posts.
const DocType = "post"

// SourcePrefix + handle is the source name (documents.source, fetch_state key).
const SourcePrefix = "telegram:"

// maxPages caps the pages read in one fetch, so a long outage or a huge
// history_days catches up over several polls instead of one long run.
const maxPages = 60

// ErrNoChannel means t.me/s/<handle> redirects instead of showing posts: the
// channel does not exist, is private or has the web preview disabled
// (LOOKUP G3).
var ErrNoChannel = errors.New("channel not found or has no public web preview")

// NewHTTP is the HTTP client for t.me: requests spaced 2 s apart and
// redirects not followed, so a missing channel is a 302 rather than the
// channel landing page.
func NewHTTP(log *slog.Logger) *httpx.Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return httpx.New(
		httpx.WithHTTPClient(&http.Client{
			Timeout:       60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}),
		httpx.WithInterval(2*time.Second),
		httpx.WithLogger(log),
	)
}

// Channel is a sources.TextSource for one channel.
type Channel struct {
	cfg     config.Channel
	baseURL string // https://t.me
	http    *httpx.Client
	log     *slog.Logger
	Now     func() time.Time // defaults to time.Now
}

// New returns the source of channel ch. HistoryDays of ch must be resolved
// (config.Load does that).
func New(baseURL string, ch config.Channel, client *httpx.Client, log *slog.Logger) *Channel {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Channel{cfg: ch, baseURL: baseURL, http: client, log: log}
}

// Name is "telegram:<handle>".
func (c *Channel) Name() string { return SourcePrefix + c.cfg.Handle }

// FetchDocuments returns posts newer than cursor (the last seen message id)
// and the new cursor. An empty cursor loads history_days of history. Posts
// without text (media without a caption, service messages) move the cursor
// but are not returned.
func (c *Channel) FetchDocuments(ctx context.Context, cursor string) ([]model.Document, string, error) {
	var posts []post
	var next int64
	var err error
	if cursor == "" {
		posts, next, err = c.history(ctx)
	} else {
		last, perr := strconv.ParseInt(cursor, 10, 64)
		if perr != nil {
			return nil, "", fmt.Errorf("telegram %s: bad cursor %q: %w", c.cfg.Handle, cursor, perr)
		}
		posts, next, err = c.newer(ctx, last)
	}
	if err != nil {
		return nil, "", err
	}
	slices.SortFunc(posts, func(a, b post) int { return cmp.Compare(a.ID, b.ID) })
	docs := make([]model.Document, 0, len(posts))
	for _, p := range posts {
		if p.Text == "" {
			continue
		}
		docs = append(docs, c.document(p))
	}
	nextCursor := cursor
	if next > 0 {
		nextCursor = strconv.FormatInt(next, 10)
	}
	return docs, nextCursor, nil
}

// history reads the latest page and pages back until posts are older than
// history_days.
func (c *Channel) history(ctx context.Context) ([]post, int64, error) {
	cutoff := c.now().AddDate(0, 0, -c.cfg.HistoryDays)
	out, maxID, err := c.since(ctx, cutoff)
	if err != nil {
		return nil, 0, err
	}
	c.log.Info("telegram history loaded", "channel", c.cfg.Handle, "posts", len(out), "since", cutoff.Format(time.DateOnly))
	return out, maxID, nil
}

// Seen is a post re-read by Views.
type Seen struct {
	Views    int64 // 0 if the page shows none
	HasPhoto bool
}

// Views re-reads the posts published since cutoff and returns their views
// and photo flag by external id (FIXES_W44 E5: views at fetch time, minutes
// after the post, are close to zero; posts fetched before schema v10 get
// their photo flag).
func (c *Channel) Views(ctx context.Context, cutoff time.Time) (map[string]Seen, error) {
	posts, _, err := c.since(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Seen, len(posts))
	for _, p := range posts {
		out[strconv.FormatInt(p.ID, 10)] = Seen{Views: p.Views, HasPhoto: p.HasPhoto}
	}
	return out, nil
}

// since reads the latest page and pages back until posts are older than
// cutoff; it returns the newer posts and the highest message id seen.
func (c *Channel) since(ctx context.Context, cutoff time.Time) ([]post, int64, error) {
	var out []post
	var maxID int64
	before := int64(0)
	for range maxPages {
		q := url.Values{}
		if before > 0 {
			q.Set("before", strconv.FormatInt(before, 10))
		}
		p, err := c.page(ctx, q)
		if err != nil {
			return nil, 0, err
		}
		if len(p.Posts) == 0 {
			break
		}
		older := false
		for _, ps := range p.Posts {
			maxID = max(maxID, ps.ID)
			if ps.Published.Before(cutoff) {
				older = true
				continue
			}
			out = append(out, ps)
		}
		if older || p.Before <= 0 {
			break
		}
		before = p.Before
	}
	return out, maxID, nil
}

// newer reads ?after=<last> pages until a page brings nothing new.
func (c *Channel) newer(ctx context.Context, last int64) ([]post, int64, error) {
	var out []post
	cur := last
	for range maxPages {
		p, err := c.page(ctx, url.Values{"after": {strconv.FormatInt(cur, 10)}})
		if err != nil {
			return nil, 0, err
		}
		top := cur
		for _, ps := range p.Posts {
			if ps.ID > cur {
				out = append(out, ps)
				top = max(top, ps.ID)
			}
		}
		if top == cur {
			break
		}
		cur = top
	}
	return out, cur, nil
}

func (c *Channel) page(ctx context.Context, q url.Values) (page, error) {
	u := c.baseURL + "/s/" + url.PathEscape(c.cfg.Handle)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, err := c.http.Get(ctx, u)
	var he *httpx.HTTPError
	if errors.As(err, &he) && he.Status >= 300 && he.Status < 400 || errors.Is(err, httpx.ErrNotFound) {
		return page{}, fmt.Errorf("telegram %s: %w", c.cfg.Handle, ErrNoChannel)
	}
	if err != nil {
		return page{}, fmt.Errorf("telegram %s: %w", c.cfg.Handle, err)
	}
	p, err := parsePage(body)
	if err != nil {
		return page{}, fmt.Errorf("telegram %s: %w", c.cfg.Handle, err)
	}
	return p, nil
}

func (c *Channel) document(p post) model.Document {
	return model.Document{
		Source:        c.Name(),
		ExternalID:    strconv.FormatInt(p.ID, 10),
		DocType:       DocType,
		Region:        c.cfg.Region,
		PublishedAt:   p.Published,
		Author:        "@" + p.Handle,
		Body:          p.Text,
		URL:           c.baseURL + "/" + p.Handle + "/" + strconv.FormatInt(p.ID, 10),
		IsOpinion:     c.cfg.Type == "opinion",
		ForwardedFrom: p.ForwardedFrom,
		Views:         p.Views,
		ViewsAt:       c.now().UTC(),
		HasPhoto:      p.HasPhoto,
	}
}

func (c *Channel) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
