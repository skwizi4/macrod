package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"macro-collector/internal/transform"
)

// defaultMentionTopicsTop is news.mention_topics_top when absent.
const defaultMentionTopicsTop = 3

// PostTypes are the valid types of a channel or feed.
var PostTypes = []string{"news", "opinion"}

// Posts holds settings shared by Telegram channels and RSS feeds.
type Posts struct {
	PollInterval time.Duration `yaml:"poll_interval"`
	HistoryDays  int           `yaml:"history_days"` // history loaded on the first poll of a new source
}

// Telegram is the Telegram channels section. Only the web mode (t.me/s/,
// public channels, D12) is implemented.
type Telegram struct {
	Enabled  bool      `yaml:"enabled"`
	Mode     string    `yaml:"mode"`
	BaseURL  string    `yaml:"base_url"` // https://t.me
	Channels []Channel `yaml:"channels"`
}

// Channel is one public Telegram channel.
type Channel struct {
	Handle      string `yaml:"handle"`
	Region      string `yaml:"region"`
	Type        string `yaml:"type"`
	DefaultTag  string `yaml:"default_tag"`
	HistoryDays int    `yaml:"history_days"` // 0 = posts.history_days
	Profile     string `yaml:"profile"`      // who the channel is, shown to the News agent
	// OpinionsFull overrides news.opinions_per_channel for this channel:
	// its opinions shown in full in USA News; 0 = the news default.
	OpinionsFull int `yaml:"opinions_full"`
}

// RSSFeed is one RSS or Atom feed (Substack, news).
type RSSFeed struct {
	Name        string `yaml:"name"`
	URL         string `yaml:"url"`
	Region      string `yaml:"region"`
	Type        string `yaml:"type"`
	DefaultTag  string `yaml:"default_tag"`
	HistoryDays int    `yaml:"history_days"` // 0 = posts.history_days
	Profile     string `yaml:"profile"`      // who the author is, shown to the News agent
}

// News configures the cleanup of posts in USA/EU News (FIXES_W44 batch E).
// Words and phrases use the classify.rules syntax (whole words, "word*").
type News struct {
	MinTextChars      int      `yaml:"min_text_chars"`      // shorter posts (an image with a caption) are dropped
	SignatureShare    float64  `yaml:"signature_share"`     // a line in this share of a channel's posts is its signature
	SignatureMinPosts int      `yaml:"signature_min_posts"` // fewer posts of a channel in the week: no signature search
	PromoWords        []string `yaml:"promo_words"`         // lines with these words are cut
	OpinionMarkers    []string `yaml:"opinion_markers"`     // a post with one of these is an opinion
	CalendarPosts     []string `yaml:"calendar_posts"`      // first-line phrases of daily calendar announcements
	GlobalMax         int      `yaml:"global_max"`          // posts in the "Глобально" section
	ViewsRefresh      bool     `yaml:"views_refresh"`       // re-read Telegram views before the weekly export

	CaptionMaxChars    int            `yaml:"caption_max_chars"`    // a photo post shorter than this, without digits or with a caption word, is a caption
	OpinionsPerChannel int            `yaml:"opinions_per_channel"` // opinions of a channel in full, by resonance; the rest as one line; 0 = all
	DropKeywords       NewsDrop       `yaml:"drop_keywords"`        // whole posts dropped by meaning
	Topics             NewsTopicWords `yaml:"topics"`               // words that split usa_micro and find market posts
	MentionTopics      []MentionTopic `yaml:"mention_topics"`       // "Темы недели в каналах" of the overview
	MentionTopicsTop   int            `yaml:"mention_topics_top"`   // rising topics named
	SurpriseFacts      []SurpriseFact `yaml:"surprise_facts"`       // "Сюрпризы недели": the actual value from our series
}

// NewsDrop are the words of posts dropped whole, by reason.
type NewsDrop struct {
	Announce []string `yaml:"announce"` // countdowns, "coming soon", streams and podcasts
	Product  []string `yaml:"product"`  // product launches
	Caption  []string `yaml:"caption"`  // a short photo post with one of these is a chart caption, digits or not
}

// NewsTopicWords assign the topics the classifier tags do not name.
type NewsTopicWords struct {
	AI     []string `yaml:"ai"`     // "AI и техсектор"
	Market []string `yaml:"market"` // "Рынок и позиционирование"
	Global []string `yaml:"global"` // "Глобально": other countries' central banks and politics
}

// MentionTopic is one counted topic; words use the classify.rules syntax
// (whole words, "word*", phrases).
type MentionTopic struct {
	Name  string   `yaml:"name"`
	Words []string `yaml:"words"`
}

// SurpriseFact names the series whose value is the actual of a surprise
// line: the line's indicator has one of Words, one of When (if set) and
// none of Unless. The value is Transform of the FRED series times Scale,
// in Unit ("%", "K", "M"). The first matching entry wins.
type SurpriseFact struct {
	Label     string   `yaml:"label"` // the indicator's name in a forecast row ("Core PCE"), with m/m or y/y
	Words     []string `yaml:"words"`
	When      []string `yaml:"when"`
	Unless    []string `yaml:"unless"`
	Series    string   `yaml:"series"`
	Transform string   `yaml:"transform"`
	Scale     float64  `yaml:"scale"` // 0 = 1
	Unit      string   `yaml:"unit"`
}

// SurpriseUnits are the units a surprise fact can be in.
var SurpriseUnits = []string{"%", "K", "M", "B"}

// Classify configures tagging of posts (§11, D14).
type Classify struct {
	MinScore       int                 `yaml:"min_score"` // keyword path
	Tags           []string            `yaml:"tags"`      // topic tags (§11), in tie-break order; rules and the prompt must name the same set
	Rules          map[string][]string `yaml:"rules"`
	DropIfContains []string            `yaml:"drop_if_contains"`
	Gemini         Gemini              `yaml:"gemini"`
}

// Gemini configures the primary classifier. The API key comes only from the
// GEMINI_API_KEY environment variable.
type Gemini struct {
	Enabled           bool          `yaml:"enabled"`
	Endpoint          string        `yaml:"endpoint"` // generateContent URL with {model}
	Model             string        `yaml:"model"`
	Prompt            string        `yaml:"prompt"` // relative to the config file
	BatchSize         int           `yaml:"batch_size"`
	MinConfidence     float64       `yaml:"min_confidence"`
	Timeout           time.Duration `yaml:"timeout"`
	DailyRequestLimit int           `yaml:"daily_request_limit"` // per Pacific day, as the API quota

	PromptPath string   `yaml:"-"` // resolved by Load
	Tags       []string `yaml:"-"` // classify.tags, filled by Load
}

// EnvGeminiKey is the environment variable with the Gemini API key.
const EnvGeminiKey = "GEMINI_API_KEY"

var (
	handleRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{3,31}$`)
	feedNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)
)

func (c *Config) validatePosts(configDir string) error {
	var errs []error
	if c.Posts.PollInterval <= 0 {
		errs = append(errs, errors.New("posts.poll_interval must be positive"))
	}
	if c.Posts.HistoryDays <= 0 {
		errs = append(errs, errors.New("posts.history_days must be positive"))
	}

	t := &c.Telegram
	if t.Enabled {
		if t.Mode != "web" {
			errs = append(errs, fmt.Errorf("telegram.mode %q: only web is supported", t.Mode))
		}
		errs = append(errs, checkURL("telegram.base_url", t.BaseURL))
	}
	seen := make(map[string]bool)
	for i := range t.Channels {
		ch := &t.Channels[i]
		where := fmt.Sprintf("telegram.channels[%d] %s", i, ch.Handle)
		if !handleRe.MatchString(ch.Handle) {
			errs = append(errs, fmt.Errorf("%s: handle must be the channel username from t.me/<handle> (letters, digits, _; 4–32 chars)", where))
		}
		key := strings.ToLower(ch.Handle)
		if seen[key] {
			errs = append(errs, fmt.Errorf("%s: duplicate handle", where))
		}
		seen[key] = true
		errs = append(errs, c.checkSourceTags(where, ch.Region, ch.Type, ch.DefaultTag, ch.HistoryDays), checkProfile(where, ch.Profile))
		if ch.OpinionsFull < 0 {
			errs = append(errs, fmt.Errorf("%s: opinions_full must not be negative", where))
		}
		if ch.HistoryDays == 0 {
			ch.HistoryDays = c.Posts.HistoryDays
		}
	}

	seen = make(map[string]bool)
	for i := range c.RSSFeeds {
		f := &c.RSSFeeds[i]
		where := fmt.Sprintf("rss_feeds[%d] %s", i, f.Name)
		errs = append(errs, checkProfile(where, f.Profile))
		if !feedNameRe.MatchString(f.Name) {
			errs = append(errs, fmt.Errorf("%s: name must be lowercase letters, digits and _", where))
		}
		if seen[f.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name", where))
		}
		seen[f.Name] = true
		errs = append(errs, checkURL(where+" url", f.URL), c.checkSourceTags(where, f.Region, f.Type, f.DefaultTag, f.HistoryDays))
		if f.HistoryDays == 0 {
			f.HistoryDays = c.Posts.HistoryDays
		}
	}

	if len(c.Classify.Tags) == 0 && (len(t.Channels) > 0 || len(c.RSSFeeds) > 0) {
		errs = append(errs, errors.New("classify.tags is empty"))
	}
	errs = append(errs, c.Classify.validate(configDir), c.News.validate())
	return errors.Join(errs...)
}

// checkProfile rejects a placeholder profile.
func checkProfile(where, profile string) error {
	if strings.Contains(profile, "[[") {
		return fmt.Errorf("%s: profile is a placeholder %q", where, profile)
	}
	return nil
}

func (n *News) validate() error {
	var errs []error
	if n.MinTextChars < 0 {
		errs = append(errs, errors.New("news.min_text_chars must not be negative"))
	}
	if n.SignatureShare <= 0 || n.SignatureShare > 1 {
		errs = append(errs, fmt.Errorf("news.signature_share %v: want (0, 1]", n.SignatureShare))
	}
	if n.SignatureMinPosts < 2 {
		errs = append(errs, errors.New("news.signature_min_posts must be at least 2"))
	}
	if n.GlobalMax < 0 {
		errs = append(errs, errors.New("news.global_max must not be negative"))
	}
	if n.CaptionMaxChars < 0 || n.OpinionsPerChannel < 0 {
		errs = append(errs, errors.New("news.caption_max_chars and news.opinions_per_channel must not be negative"))
	}
	if n.MentionTopicsTop == 0 {
		n.MentionTopicsTop = defaultMentionTopicsTop
	}
	lists := map[string][]string{"promo_words": n.PromoWords, "opinion_markers": n.OpinionMarkers, "calendar_posts": n.CalendarPosts,
		"drop_keywords.announce": n.DropKeywords.Announce, "drop_keywords.product": n.DropKeywords.Product,
		"drop_keywords.caption": n.DropKeywords.Caption,
		"topics.ai":             n.Topics.AI, "topics.market": n.Topics.Market, "topics.global": n.Topics.Global}
	for i, t := range n.MentionTopics {
		if t.Name == "" || len(t.Words) == 0 {
			errs = append(errs, fmt.Errorf("news.mention_topics[%d] %q: name and words are required", i, t.Name))
		}
		lists[fmt.Sprintf("mention_topics[%d]", i)] = t.Words
	}
	for i, f := range n.SurpriseFacts {
		where := fmt.Sprintf("news.surprise_facts[%d] %s", i, f.Series)
		if len(f.Words) == 0 || f.Series == "" {
			errs = append(errs, fmt.Errorf("%s: words and series are required", where))
		}
		if !slices.Contains(transform.Codes, f.Transform) {
			errs = append(errs, fmt.Errorf("%s: transform %q: want one of %s", where, f.Transform, strings.Join(transform.Codes, ", ")))
		}
		if !slices.Contains(SurpriseUnits, f.Unit) {
			errs = append(errs, fmt.Errorf("%s: unit %q: want one of %s", where, f.Unit, strings.Join(SurpriseUnits, ", ")))
		}
		if f.Scale < 0 {
			errs = append(errs, fmt.Errorf("%s: scale must not be negative", where))
		}
		lists[fmt.Sprintf("surprise_facts[%d]", i)] = slices.Concat(f.Words, f.When, f.Unless)
	}
	for name, list := range lists {
		for _, w := range list {
			if strings.TrimSpace(w) == "" || strings.Contains(w, "[[") {
				errs = append(errs, fmt.Errorf("news.%s: empty or placeholder entry %q", name, w))
			}
		}
	}
	return errors.Join(errs...)
}

func (cl *Classify) validate(configDir string) error {
	var errs []error
	if cl.MinScore <= 0 {
		errs = append(errs, errors.New("classify.min_score must be positive"))
	}
	cl.Gemini.Tags = cl.Tags
	for _, tag := range cl.Tags {
		if len(cl.Rules[tag]) == 0 {
			errs = append(errs, fmt.Errorf("classify.rules: no keywords for tag %s", tag))
		}
	}
	for tag, words := range cl.Rules {
		if !slices.Contains(cl.Tags, tag) {
			errs = append(errs, fmt.Errorf("classify.rules: unknown tag %q (want one of %s)", tag, strings.Join(cl.Tags, ", ")))
		}
		for _, w := range words {
			if strings.TrimSpace(w) == "" || strings.Contains(w, "[[") {
				errs = append(errs, fmt.Errorf("classify.rules.%s: empty or placeholder keyword %q", tag, w))
			}
		}
	}
	for _, w := range cl.DropIfContains {
		if strings.TrimSpace(w) == "" || strings.Contains(w, "[[") {
			errs = append(errs, fmt.Errorf("classify.drop_if_contains: empty or placeholder word %q", w))
		}
	}

	g := &cl.Gemini
	if g.Enabled {
		if !strings.Contains(g.Endpoint, "{model}") {
			errs = append(errs, fmt.Errorf("classify.gemini.endpoint %q: must contain {model}", g.Endpoint))
		} else {
			errs = append(errs, checkURL("classify.gemini.endpoint", g.Endpoint))
		}
		if g.Model == "" {
			errs = append(errs, errors.New("classify.gemini.model is empty"))
		}
		if g.BatchSize <= 0 {
			errs = append(errs, errors.New("classify.gemini.batch_size must be positive"))
		}
		if g.MinConfidence <= 0 || g.MinConfidence > 1 {
			errs = append(errs, fmt.Errorf("classify.gemini.min_confidence %v: want (0, 1]", g.MinConfidence))
		}
		if g.Timeout <= 0 {
			errs = append(errs, errors.New("classify.gemini.timeout must be positive"))
		}
		if g.DailyRequestLimit <= 0 {
			errs = append(errs, errors.New("classify.gemini.daily_request_limit must be positive"))
		}
		g.PromptPath = g.Prompt
		if g.PromptPath != "" && !filepath.IsAbs(g.PromptPath) {
			g.PromptPath = filepath.Join(configDir, g.PromptPath)
		}
		if g.Prompt == "" {
			errs = append(errs, errors.New("classify.gemini.prompt is empty"))
		} else if _, err := os.Stat(g.PromptPath); err != nil {
			errs = append(errs, fmt.Errorf("classify.gemini.prompt: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (c *Config) checkSourceTags(where, region, typ, defaultTag string, historyDays int) error {
	var errs []error
	if !slices.Contains(c.RegionIDs(), region) {
		errs = append(errs, fmt.Errorf("%s: region %q (want one of %s)", where, region, strings.Join(c.RegionIDs(), ", ")))
	}
	if !slices.Contains(PostTypes, typ) {
		errs = append(errs, fmt.Errorf("%s: type %q (want one of %s)", where, typ, strings.Join(PostTypes, ", ")))
	}
	if defaultTag != "" && !slices.Contains(c.Classify.Tags, defaultTag) {
		errs = append(errs, fmt.Errorf("%s: default_tag %q (want empty or one of %s)", where, defaultTag, strings.Join(c.Classify.Tags, ", ")))
	}
	if historyDays < 0 {
		errs = append(errs, fmt.Errorf("%s: history_days must not be negative", where))
	}
	return errors.Join(errs...)
}

func checkURL(where, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%s %q: want an http(s) URL", where, raw)
	}
	return nil
}
