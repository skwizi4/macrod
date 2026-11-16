package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"macro-collector/internal/classify"
	"macro-collector/internal/config"
	"macro-collector/internal/dedup"
	"macro-collector/internal/ingest"
	"macro-collector/internal/logging"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/rss"
	"macro-collector/internal/sources/telegram"
	"macro-collector/internal/store"
)

// Posts is the phase 4 pass over Telegram channels and RSS feeds: fetch new
// posts, classify them, merge reposts. Shared by macroctl and the daemon.
type Posts struct {
	Sources  []sources.TextSource
	Texts    *ingest.Texts
	Classify *classify.Runner
	Store    *store.Store
	Log      *slog.Logger
}

// PostSources are the configured channels (if telegram.enabled) and feeds.
func PostSources(cfg *config.Config, root *slog.Logger) []sources.TextSource {
	var out []sources.TextSource
	if cfg.Telegram.Enabled && len(cfg.Telegram.Channels) > 0 {
		log := logging.For(root, "telegram")
		client := telegram.NewHTTP(log)
		for _, ch := range cfg.Telegram.Channels {
			out = append(out, telegram.New(cfg.Telegram.BaseURL, ch, client, log))
		}
	}
	if len(cfg.RSSFeeds) > 0 {
		log := logging.For(root, "rss")
		client := httpx.New(httpx.WithLogger(log), httpx.WithInterval(time.Second))
		for _, f := range cfg.RSSFeeds {
			out = append(out, rss.New(f, client, log))
		}
	}
	return out
}

// NewClassifier is the classification runner of the config. Gemini is used
// when enabled and geminiKey is set; otherwise keywords only, with a warning.
func NewClassifier(cfg *config.Config, st *store.Store, root *slog.Logger, geminiKey string) (*classify.Runner, error) {
	log := logging.For(root, "classify")
	g := cfg.Classify.Gemini
	r := &classify.Runner{
		Store: st, Keywords: classify.NewKeywords(cfg.Classify), BatchSize: g.BatchSize,
		DailyLimit: g.DailyRequestLimit, MinConf: g.MinConfidence, Sources: make(map[string]classify.Source), Log: log,
	}
	if r.BatchSize <= 0 {
		r.BatchSize = 20
	}
	for _, ch := range cfg.Telegram.Channels {
		r.Sources[telegram.SourcePrefix+ch.Handle] = classify.Source{Channel: ch.Handle, DefaultTag: ch.DefaultTag}
	}
	for _, f := range cfg.RSSFeeds {
		r.Sources[rss.SourcePrefix+f.Name] = classify.Source{Channel: f.Name, DefaultTag: f.DefaultTag}
	}
	switch {
	case !g.Enabled:
		log.Info("gemini disabled in config: keywords only")
	case geminiKey == "":
		log.Warn("GEMINI_API_KEY is not set: keywords only")
	default:
		prompt, err := classify.LoadPrompt(g.PromptPath, g.Tags)
		if err != nil {
			return nil, err
		}
		r.Prompt = prompt
		r.Gemini = classify.NewGemini(g, geminiKey, prompt, log)
	}
	return r, nil
}

// NewPosts wires the pass from the config.
func NewPosts(cfg *config.Config, st *store.Store, root *slog.Logger, geminiKey string) (*Posts, error) {
	r, err := NewClassifier(cfg, st, root, geminiKey)
	if err != nil {
		return nil, err
	}
	return &Posts{
		Sources:  PostSources(cfg, root),
		Texts:    &ingest.Texts{Store: st, Log: logging.For(root, "posts")},
		Classify: r,
		Store:    st,
		Log:      logging.For(root, "posts"),
	}, nil
}

// PostsSummary is the result of one pass.
type PostsSummary struct {
	Fetch    []ingest.TextResult
	Classify classify.Stats
	Dedup    dedup.Stats
	Err      error // classify or dedup failure; source failures are in Fetch
}

// New is the number of new posts.
func (s PostsSummary) New() int {
	n := 0
	for _, r := range s.Fetch {
		n += len(r.Inserted)
	}
	return n
}

// Failed lists the failed sources.
func (s PostsSummary) Failed() []ingest.TextResult {
	var out []ingest.TextResult
	for _, r := range s.Fetch {
		if r.Err != nil {
			out = append(out, r)
		}
	}
	return out
}

// Error joins source, classify and dedup failures.
func (s PostsSummary) Error() error {
	errs := []error{s.Err}
	for _, r := range s.Failed() {
		errs = append(errs, fmt.Errorf("%s: %w", r.Source, r.Err))
	}
	return errors.Join(errs...)
}

// Run fetches every source (one failing does not stop the others), then
// classifies and deduplicates whatever is pending, including posts left
// from an earlier interrupted pass.
func (p *Posts) Run(ctx context.Context) PostsSummary {
	var sum PostsSummary
	for _, src := range p.Sources {
		if ctx.Err() != nil {
			sum.Err = ctx.Err()
			return sum
		}
		sum.Fetch = append(sum.Fetch, p.Texts.Run(ctx, src))
	}
	return p.Process(ctx, sum)
}

// Process classifies and deduplicates pending posts.
func (p *Posts) Process(ctx context.Context, sum PostsSummary) PostsSummary {
	var err error
	if sum.Classify, err = p.Classify.Run(ctx); err != nil {
		sum.Err = fmt.Errorf("classify: %w", err)
		return sum
	}
	if sum.Dedup, err = dedup.Run(ctx, p.Store); err != nil {
		sum.Err = fmt.Errorf("dedup: %w", err)
	}
	return sum
}

// RefreshViews re-reads the views and photo flag of Telegram posts published since cutoff
// (FIXES_W44 E5) and returns the number of posts updated. A failed channel
// does not stop the others.
func (p *Posts) RefreshViews(ctx context.Context, cutoff time.Time) (int, error) {
	updated := 0
	var errs []error
	for _, src := range p.Sources {
		ch, ok := src.(*telegram.Channel)
		if !ok {
			continue
		}
		views, err := ch.Views(ctx, cutoff)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		seen := make(map[string]store.PostSeen, len(views))
		for id, v := range views {
			seen[id] = store.PostSeen{Views: v.Views, HasPhoto: v.HasPhoto}
		}
		at := time.Now()
		if err := p.Store.Write(ctx, func(tx *sql.Tx) error {
			n, err := store.UpdateViews(tx, src.Name(), seen, at)
			updated += n
			return err
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return updated, errors.Join(errs...)
}

// Only keeps the sources whose name starts with prefix ("telegram:",
// "rss:", or a full source name).
func (p *Posts) Only(prefix string) *Posts {
	q := *p
	q.Sources = nil
	for _, s := range p.Sources {
		if strings.HasPrefix(s.Name(), prefix) {
			q.Sources = append(q.Sources, s)
		}
	}
	return &q
}
