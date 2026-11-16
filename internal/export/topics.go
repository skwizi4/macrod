package export

import (
	"cmp"
	"slices"
	"strings"

	"macro-collector/internal/classify"
	"macro-collector/internal/config"
	"macro-collector/internal/store"
)

// topicCounts counts, for each topic, the USA posts that mention it:
// canonical classified posts after the cleanup, without the posts the
// noise filters drop, the same set USA News starts from.
func topicCounts(news config.News, rep *config.Report, inNews func(store.WeekPost) bool, rules map[string][]string, posts []store.WeekPost) []int {
	topics := news.MentionTopics
	phrases := make([][]classify.Phrase, len(topics))
	for i, t := range topics {
		for _, w := range t.Words {
			phrases[i] = append(phrases[i], classify.CompilePhrase(w))
		}
	}
	cl := newNewsCleaner(news, rep, rules, posts)
	counts := make([]int, len(topics))
	for _, p := range posts {
		if !inNews(p) || p.DupOf != 0 || !p.Classified || p.IsJunk {
			continue
		}
		body := cl.clean(p)
		if _, drop := cl.drop(p, body, cl.topic(p, body)); drop {
			continue
		}
		ws := classify.Normalize(p.Title + "\n" + body)
		for i := range topics {
			if anyPhrase(phrases[i], ws) {
				counts[i]++
			}
		}
	}
	return counts
}

// MentionTopic is how many posts mention a topic, this and last week.
type MentionTopic struct {
	Name   string `json:"name"`
	Now    int    `json:"now"`
	Before int    `json:"before"`
}

// MentionRising is a topic mentioned more than last week.
type MentionRising struct {
	Name  string `json:"name"`
	Delta int    `json:"delta"`
}

// NewsMentions is "Темы недели в каналах"; Topics is empty when none are
// configured.
type NewsMentions struct {
	Topics []MentionTopic  `json:"topics"`
	Rising []MentionRising `json:"rising"`
}

// newsMentions is "Темы недели в каналах" of the USA News overview (moved
// from sentiment.md F7 on 2026-11-03): how often each topic came up this
// week and the week before, and the topics rising most.
func (w *week) newsMentions(cur, prev []store.WeekPost) NewsMentions {
	out := NewsMentions{Topics: []MentionTopic{}, Rising: []MentionRising{}}
	topics := w.cfg.News.MentionTopics
	if len(topics) == 0 {
		return out
	}
	now, before := topicCounts(w.cfg.News, w.newsConfig(), w.inNews, w.cfg.Classify.Rules, cur), topicCounts(w.cfg.News, w.newsConfig(), w.inNews, w.cfg.Classify.Rules, prev)
	for i, t := range topics {
		out.Topics = append(out.Topics, MentionTopic{Name: t.Name, Now: now[i], Before: before[i]})
		if d := now[i] - before[i]; d > 0 {
			out.Rising = append(out.Rising, MentionRising{Name: t.Name, Delta: d})
		}
	}
	slices.SortStableFunc(out.Rising, func(a, c MentionRising) int { return cmp.Compare(c.Delta, a.Delta) })
	if len(out.Rising) > w.cfg.News.MentionTopicsTop {
		out.Rising = out.Rising[:w.cfg.News.MentionTopicsTop]
	}
	return out
}

// signedCount is "+3", "0", "−1".
func signedCount(n int) string { return num(float64(n), 0, true) }

// risingList is "Тема (+3), Тема (+1)".
func risingList(rs []MentionRising) string {
	if len(rs) == 0 {
		return "нет"
	}
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, r.Name+" ("+signedCount(r.Delta)+")")
	}
	return strings.Join(parts, ", ")
}
