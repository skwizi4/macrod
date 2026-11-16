// Package classify tags posts by topic (ТЗ §11, D14). Gemini is the primary
// classifier; keywords are the fallback when Gemini has no key, fails, runs
// out of quota or answers something invalid.
package classify

import (
	"context"
	"slices"
)

// Classifier names, stored in documents.classifier and document_tags.classifier.
const (
	ByGemini   = "gemini"
	ByKeywords = "keywords"
)

// Post is the text of one document to classify.
type Post struct {
	Ref        string // id shown to the model: "<channel>/<id>" or "<feed>/<n>"
	Channel    string // handle or feed name
	Text       string
	DefaultTag string // default_tag of the channel or feed, may be empty
}

// Tag is a topic tag with its score: keyword hits or Gemini confidence.
type Tag struct {
	Tag   string  `json:"tag"`
	Score float64 `json:"confidence"`
}

// Result is the classification of one post. No tags and not junk means
// uncategorized (§11.4).
type Result struct {
	Tags    []Tag  `json:"tags"`
	Primary string `json:"primary"`
	IsJunk  bool   `json:"is_junk"`
	Reason  string `json:"reason,omitempty"`
	By      string `json:"-"`
}

// Classifier classifies a batch; results are in the order of posts.
type Classifier interface {
	Classify(ctx context.Context, posts []Post) ([]Result, error)
}

// setPrimary orders tags by score (ties by the order of tags) and sets
// Primary to the first.
func (r *Result) setPrimary(tags []string) {
	slices.SortStableFunc(r.Tags, func(a, b Tag) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return slices.Index(tags, a.Tag) - slices.Index(tags, b.Tag)
	})
	r.Primary = ""
	if len(r.Tags) > 0 {
		r.Primary = r.Tags[0].Tag
	}
}
