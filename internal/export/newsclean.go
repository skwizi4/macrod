package export

import (
	"maps"
	"slices"
	"strings"
	"unicode"

	"macro-collector/internal/classify"
	"macro-collector/internal/config"
	"macro-collector/internal/store"
)

// newsCleaner applies the post cleanup of FIXES_W44 E1 and E4: channel
// signatures and promo lines are cut, image-only posts and calendar
// announcements are recognised, opinion markers make a post an opinion.
// The noise filters of 2026-11-03 drop announcements, image captions and
// product news whole, and topic words place posts the tags do not.
type newsCleaner struct {
	cfg        config.News
	promo      []classify.Phrase
	opinion    []classify.Phrase
	calendar   []classify.Phrase
	announce   []classify.Phrase
	product    []classify.Phrase
	caption    []classify.Phrase
	words      [][]classify.Phrase        // by topic: the words that vote for it
	signatures map[string]map[string]bool // source → signature lines
	// tagTopics are the topics the classifier tags name; their keywords in
	// classify.rules vote for them.
	tagTopics   map[string]int
	ownPrefixes []string // tags of this file's posts
	otherPrefix string   // tags of another region
}

// topicOf are the topics a configured tag_topics value can name.
var topicOf = map[string]int{
	config.TopicFed: topicFed, config.TopicFiscal: topicFiscal, config.TopicPolitics: topicPolitics,
	config.TopicEnergy: topicEnergy, config.TopicEarnings: topicEarnings, config.TopicEurope: topicEurope,
}

func newNewsCleaner(cfg config.News, rep *config.Report, rules map[string][]string, posts []store.WeekPost) *newsCleaner {
	compile := func(list []string) []classify.Phrase {
		var out []classify.Phrase
		for _, s := range list {
			if p := classify.CompilePhrase(s); len(p) > 0 {
				out = append(out, p)
			}
		}
		return out
	}
	tagTopics := make(map[string]int, len(rep.TagTopics))
	for tag, name := range rep.TagTopics {
		tagTopics[tag] = topicOf[name]
	}
	c := &newsCleaner{cfg: cfg, tagTopics: tagTopics, ownPrefixes: rep.OwnPrefixes(), otherPrefix: rep.OtherTagPrefix, promo: compile(cfg.PromoWords), opinion: compile(cfg.OpinionMarkers),
		calendar: compile(cfg.CalendarPosts), announce: compile(cfg.DropKeywords.Announce), product: compile(cfg.DropKeywords.Product),
		caption: compile(cfg.DropKeywords.Caption), words: make([][]classify.Phrase, len(newsTopics)),
		signatures: signatures(posts, cfg.SignatureShare, cfg.SignatureMinPosts)}
	for _, tag := range slices.Sorted(maps.Keys(c.tagTopics)) { // several tags may share a topic
		c.words[c.tagTopics[tag]] = append(c.words[c.tagTopics[tag]], compile(rules[tag])...)
	}
	c.words[topicAI], c.words[topicMarket], c.words[topicGlobal] = compile(cfg.Topics.AI), compile(cfg.Topics.Market), compile(cfg.Topics.Global)
	return c
}

// signatures finds the lines a channel repeats in at least share of its
// posts of the week (counted once per post); channels with fewer than
// minPosts posts are skipped. Reposts count as posts of their channel.
func signatures(posts []store.WeekPost, share float64, minPosts int) map[string]map[string]bool {
	counts := make(map[string]map[string]int)
	total := make(map[string]int)
	for _, p := range posts {
		total[p.Source]++
		seen := make(map[string]bool)
		for _, l := range strings.Split(p.Body, "\n") {
			l = strings.TrimSpace(l)
			if l == "" || seen[l] {
				continue
			}
			seen[l] = true
			if counts[p.Source] == nil {
				counts[p.Source] = make(map[string]int)
			}
			counts[p.Source][l]++
		}
	}
	out := make(map[string]map[string]bool)
	for src, n := range total {
		if n < minPosts {
			continue
		}
		for l, k := range counts[src] {
			if float64(k) >= share*float64(n) {
				if out[src] == nil {
					out[src] = make(map[string]bool)
				}
				out[src][l] = true
			}
		}
	}
	return out
}

// clean cuts signature and promo lines from a post body.
func (c *newsCleaner) clean(p store.WeekPost) string {
	sig := c.signatures[p.Source]
	var keep []string
	for _, l := range strings.Split(p.Body, "\n") {
		t := strings.TrimSpace(l)
		if sig[t] || t != "" && anyPhrase(c.promo, classify.Normalize(t)) {
			continue
		}
		keep = append(keep, l)
	}
	return cleanBlankLines(strings.Join(keep, "\n"))
}

// imageOnly is a post with almost no text: an image with a short caption
// (Telegram posts only; an article has its title).
func (c *newsCleaner) imageOnly(p store.WeekPost, body string) bool {
	return p.Title == "" && textChars(body) < c.cfg.MinTextChars
}

// isCalendar is a daily or weekly calendar announcement: its first line
// starts with one of calendar_posts.
func (c *newsCleaner) isCalendar(body string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	ws := classify.Normalize(first)
	for _, ph := range c.calendar {
		if len(ws) >= len(ph) && ph.In(ws[:len(ph)]) {
			return true
		}
	}
	return false
}

// drop is the reason a cleaned post is not exported, by content: a calendar
// announcement, an image without text, a chart caption (a short photo post
// with a caption word: most posts of a news channel have a photo, so a
// photo and no digits alone is a headline, not a caption), an announcement
// or a product launch. A post with a surprise line is never an
// announcement or product news.
func (c *newsCleaner) drop(p store.WeekPost, body string, topic int) (dropReason, bool) {
	ws := classify.Normalize(p.Title + "\n" + body)
	facts := len(extractSurprises(body)) > 0
	switch {
	case c.isCalendar(body):
		return dropCalendar, true
	case c.imageOnly(p, body):
		return dropImageOnly, true
	case p.HasPhoto && p.Title == "" && textChars(body) < c.cfg.CaptionMaxChars && !anyPhrase(c.opinion, ws) && anyPhrase(c.caption, ws):
		return dropCaption, true
	case !facts && anyPhrase(c.announce, ws):
		return dropAnnounce, true
	case !facts && !strings.Contains(body, "%") && anyPhrase(c.product, ws) &&
		(topic == topicAI || topic == topicEarnings || topic == topicNone):
		return dropProduct, true
	}
	return 0, false
}

// Topic voting (review of W44, 2026-11-03): the classifier's tag is a
// hint, the words decide. A topic gets one vote per distinct word of it
// in the post (globalVote for a "Глобально" word: it names another country
// outright), the primary tag adds tagVote × its score.
const (
	tagVote    = 1.5
	globalVote = 2
	weakOwn    = 0.7 // a tag of this region below this next to a primary tag of another: not its post
)

// topic places a post. Votes: the topic's words (classify.rules of its tag,
// news.topics.ai/market/global) and the primary tag; a usa_micro tag votes
// for "AI и техсектор" or "Рынок и позиционирование" when their words are
// there, else for "Корпоративные отчёты". Energy and geopolitics without a
// US link (no usa_* tag, no Fed, fiscal or politics word) is "Глобально".
// With other_tag_prefix (a news file of one region): a primary tag of the
// other region is its news (topicOtherRegion) without an own tag,
// "Глобально" with only a weak one. A shared file has no other region: its
// eu_* tags vote for "Европа и ЕЦБ". topicNone: no tags and no words.
func (c *newsCleaner) topic(p store.WeekPost, body string) int {
	ws := classify.Normalize(p.Title + "\n" + body)
	votes := make([]float64, len(newsTopics))
	for t, words := range c.words {
		for _, ph := range words {
			if ph.In(ws) {
				votes[t]++
				if t == topicGlobal {
					votes[t] += globalVote - 1
				}
			}
		}
	}
	bonus := -1
	own := 0.0
	for _, t := range p.Tags {
		for _, pre := range c.ownPrefixes {
			if strings.HasPrefix(t.Tag, pre) {
				own = max(own, t.Score)
			}
		}
	}
	if len(p.Tags) > 0 {
		primary := p.Tags[0]
		other := c.otherPrefix != "" && strings.HasPrefix(primary.Tag, c.otherPrefix)
		switch {
		case other && own == 0:
			return topicOtherRegion
		case other && own < weakOwn:
			return topicGlobal
		}
		if t, ok := c.tagTopics[primary.Tag]; ok {
			if t == topicEarnings {
				switch {
				case votes[topicAI] > 0:
					t = topicAI
				case votes[topicMarket] > 0:
					t = topicMarket
				}
			}
			bonus = t
			votes[t] += tagVote * primary.Score
		}
	}
	if votes[topicEnergy] > 0 && own == 0 && votes[topicFed]+votes[topicFiscal]+votes[topicPolitics] == 0 {
		votes[topicGlobal] += votes[topicEnergy]
		votes[topicEnergy] = 0
		if bonus == topicEnergy {
			bonus = topicGlobal
		}
	}
	best := topicNone
	for t, v := range votes {
		if v <= 0 {
			continue
		}
		if best == topicNone || v > votes[best] || v == votes[best] && t == bonus {
			best = t
		}
	}
	if best == topicNone && len(p.Tags) > 0 {
		return topicOtherRegion // only tags with no topic of this news
	}
	return best
}

// isOpinion: the post itself has an opinion marker, else the channel type.
func (c *newsCleaner) isOpinion(p store.WeekPost) bool {
	return p.IsOpinion || anyPhrase(c.opinion, classify.Normalize(p.Title+"\n"+p.Body))
}

func anyPhrase(phrases []classify.Phrase, ws []string) bool {
	for _, ph := range phrases {
		if ph.In(ws) {
			return true
		}
	}
	return false
}

// textChars counts characters other than emoji, pictographs and their
// modifiers, after trimming.
func textChars(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		switch {
		case unicode.In(r, unicode.So, unicode.Sk, unicode.Cf, unicode.Me, unicode.Variation_Selector):
		default:
			n++
		}
	}
	return n
}

func cleanBlankLines(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	var out []string
	blank := false
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
