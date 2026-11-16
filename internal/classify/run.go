package classify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

// GeminiCounter is the fetch_state key of the daily Gemini request count,
// cursor "<YYYY-MM-DD>:<n>" in Pacific time, when the API quota resets
// (LOOKUP M3).
const GeminiCounter = "gemini:requests"

var pacific = mustLocation("America/Los_Angeles")

func mustLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// Source of a post: channel or feed name and its default_tag.
type Source struct {
	Channel    string
	DefaultTag string
}

// Runner classifies stored posts that have no classification yet: cached
// Gemini answers first, then Gemini in batches, keywords for any post
// Gemini gives no usable answer for. After an API failure (quota, key,
// outage) the rest of the run uses keywords, so the poll does not stall; an
// unusable answer only affects its own batch.
type Runner struct {
	Store      *store.Store
	Keywords   *Keywords
	Gemini     *Gemini // nil: keywords only (no key or disabled)
	Prompt     Prompt
	BatchSize  int
	DailyLimit int               // Gemini requests per Pacific day
	MinConf    float64           // score of a default_tag added to a Gemini answer
	Sources    map[string]Source // documents.source → channel
	Log        *slog.Logger
	Now        func() time.Time // defaults to time.Now
}

// Stats counts what a run did.
type Stats struct {
	Posts, Cached, Gemini, Keywords, Junk, Uncategorized, Requests int
	Unanswered                                                     int   // posts Gemini gave no usable answer for
	GeminiErr                                                      error // the failure that switched the run to keywords
}

func (s Stats) String() string {
	out := fmt.Sprintf("posts %d: gemini %d (cached %d), keywords %d (gemini unanswered %d); junk %d, uncategorized %d; gemini requests %d",
		s.Posts, s.Gemini, s.Cached, s.Keywords, s.Unanswered, s.Junk, s.Uncategorized, s.Requests)
	if s.GeminiErr != nil {
		out += "; gemini failed: " + s.GeminiErr.Error()
	}
	return out
}

// pageSize is how many unclassified posts are read at a time.
const pageSize = 500

// Run classifies all unclassified posts.
func (r *Runner) Run(ctx context.Context) (Stats, error) {
	var st Stats
	useGemini := r.Gemini != nil
	for {
		docs, err := r.Store.UnclassifiedPosts(ctx, pageSize)
		if err != nil {
			return st, err
		}
		if len(docs) == 0 {
			return st, nil
		}
		for start := 0; start < len(docs); start += r.BatchSize {
			batch := docs[start:min(start+r.BatchSize, len(docs))]
			if err := r.batch(ctx, batch, &useGemini, &st); err != nil {
				return st, err
			}
		}
	}
}

func (r *Runner) batch(ctx context.Context, docs []model.Document, useGemini *bool, st *Stats) error {
	posts := make([]Post, len(docs))
	for i, d := range docs {
		posts[i] = r.post(d)
	}
	results := make([]Result, len(docs))
	done := make([]bool, len(docs))
	fresh := make([]bool, len(docs)) // Gemini answers to cache

	if r.Gemini != nil {
		hashes := make([]string, len(docs))
		for i, d := range docs {
			hashes[i] = d.ContentHash
		}
		cached, err := r.Store.CachedClassifications(ctx, hashes, r.Gemini.Model(), r.Prompt.Hash)
		if err != nil {
			return err
		}
		for i, d := range docs {
			js, ok := cached[d.ContentHash]
			if !ok {
				continue
			}
			var res Result
			if err := json.Unmarshal([]byte(js), &res); err != nil {
				r.log().Warn("bad cached classification ignored", "hash", d.ContentHash, "err", err)
				continue
			}
			res.By = ByGemini
			results[i], done[i] = res, true
			st.Cached++
		}
	}

	var todo []int
	for i := range docs {
		if !done[i] {
			todo = append(todo, i)
		}
	}
	var count counter
	if len(todo) > 0 && *useGemini {
		var err error
		if count, err = r.counter(ctx); err != nil {
			return err
		}
		if count.n >= r.DailyLimit {
			*useGemini = false
			st.GeminiErr = fmt.Errorf("daily limit of %d requests reached", r.DailyLimit)
			r.log().Warn("gemini daily limit reached, using keywords", "limit", r.DailyLimit)
		}
	}
	if len(todo) > 0 && *useGemini {
		sub := make([]Post, len(todo))
		for j, i := range todo {
			sub[j] = posts[i]
		}
		count.n++
		st.Requests++
		res, err := r.Gemini.Classify(ctx, sub)
		switch {
		case err == nil:
			for j, i := range todo {
				if res[j].By == "" {
					st.Unanswered++ // keywords below
					continue
				}
				results[i], done[i], fresh[i] = res[j], true, true
			}
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, ErrInvalidAnswer):
			// A bad answer to this batch: keywords for it, Gemini goes on.
			st.Unanswered += len(sub)
			st.GeminiErr = err
			r.log().Warn("gemini answer unusable, keywords for this batch", "posts", len(sub), "err", err)
		default:
			*useGemini = false
			st.GeminiErr = err
			r.log().Warn("gemini failed, using keywords", "posts", len(sub), "err", err)
		}
	}
	for i := range docs {
		if !done[i] {
			res, _ := r.Keywords.Classify(ctx, posts[i:i+1])
			results[i] = res[0]
		}
	}

	now := r.now()
	return r.Store.Write(ctx, func(tx *sql.Tx) error {
		for i, d := range docs {
			res := results[i]
			if fresh[i] {
				js, err := json.Marshal(res)
				if err != nil {
					return fmt.Errorf("classify cache encode: %w", err)
				}
				if err := store.PutClassifyCache(tx, d.ContentHash, r.Gemini.Model(), r.Prompt.Hash, string(js), now); err != nil {
					return err
				}
			}
			if res.By == ByGemini {
				res = r.withDefaultTag(res, posts[i].DefaultTag)
			}
			if err := store.SavePostTags(tx, toStore(d.ID, res), now); err != nil {
				return err
			}
			st.Posts++
			switch {
			case res.By == ByGemini:
				st.Gemini++
			default:
				st.Keywords++
			}
			switch {
			case res.IsJunk:
				st.Junk++
			case len(res.Tags) == 0:
				st.Uncategorized++
			}
		}
		if count.day != "" {
			return store.SetFetchOK(tx, GeminiCounter, count.String(), now)
		}
		return nil
	})
}

// post is the text sent to a classifier: articles with their title.
func (r *Runner) post(d model.Document) Post {
	src := r.Sources[d.Source]
	ch := src.Channel
	if ch == "" {
		_, ch, _ = strings.Cut(d.Source, ":")
	}
	ref := ch + "/" + d.ExternalID
	text := d.Body
	if d.Title != "" {
		text = d.Title + "\n\n" + d.Body
	}
	if strings.HasPrefix(d.Source, "rss:") {
		ref = ch + "/" + strconv.FormatInt(d.ID, 10) // external ids of feeds are URLs
	}
	return Post{Ref: ref, Channel: ch, Text: text, DefaultTag: src.DefaultTag}
}

// withDefaultTag adds the channel's default_tag to a Gemini answer with the
// confidence threshold as its score (§11.5); junk gets no tags.
func (r *Runner) withDefaultTag(res Result, tag string) Result {
	if tag == "" || res.IsJunk {
		return res
	}
	for _, t := range res.Tags {
		if t.Tag == tag {
			return res
		}
	}
	res.Tags = append(append([]Tag(nil), res.Tags...), Tag{Tag: tag, Score: r.MinConf})
	res.setPrimary(r.Keywords.tags)
	return res
}

func toStore(id int64, res Result) store.PostTags {
	p := store.PostTags{DocumentID: id, IsJunk: res.IsJunk, Classifier: res.By}
	for _, t := range res.Tags {
		p.Tags = append(p.Tags, store.TagScore{Tag: t.Tag, Score: t.Score, Primary: t.Tag == res.Primary})
	}
	return p
}

// counter is the Gemini request count of a Pacific day.
type counter struct {
	day string
	n   int
}

func (c counter) String() string { return c.day + ":" + strconv.Itoa(c.n) }

// counter reads today's count; a count of another day starts over.
func (r *Runner) counter(ctx context.Context) (counter, error) {
	today := r.now().In(pacific).Format(time.DateOnly)
	c := counter{day: today}
	fs, ok, err := r.Store.FetchStateOf(ctx, GeminiCounter)
	if err != nil || !ok {
		return c, err
	}
	day, n, found := strings.Cut(fs.Cursor, ":")
	if found && day == today {
		c.n, _ = strconv.Atoi(n)
	}
	return c, nil
}

// GeminiRequestsToday is the request count of the current Pacific day.
func GeminiRequestsToday(ctx context.Context, st *store.Store, now time.Time) (int, error) {
	r := Runner{Store: st, Now: func() time.Time { return now }}
	c, err := r.counter(ctx)
	return c.n, err
}

func (r *Runner) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
