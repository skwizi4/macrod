package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Labeled is one hand-labeled example of testdata/classify/labeled.json
// (§11.8). Primary "" means no tags; Accept lists other acceptable primary
// tags of ambiguous posts; JunkAny accepts either junk value.
type Labeled struct {
	ID      string   `json:"id"`
	Channel string   `json:"channel"`
	Text    string   `json:"text"`
	Primary string   `json:"primary"`
	Accept  []string `json:"accept"`
	Junk    bool     `json:"junk"`
	JunkAny bool     `json:"junk_any"`
	Note    string   `json:"note"`
}

// LabeledFile is the name of the labeled examples in testdata/classify.
const LabeledFile = "labeled.json"

// LoadLabeled reads the labeled examples.
func LoadLabeled(path string) ([]Labeled, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("labeled examples: %w", err)
	}
	var ls []Labeled
	if err := json.Unmarshal(raw, &ls); err != nil {
		return nil, fmt.Errorf("labeled examples %s: %w", path, err)
	}
	return ls, nil
}

// LabeledPosts turns examples into posts.
func LabeledPosts(ls []Labeled) []Post {
	out := make([]Post, len(ls))
	for i, l := range ls {
		out[i] = Post{Ref: l.ID, Channel: l.Channel, Text: l.Text}
	}
	return out
}

// RecordedBatchFile is the recorded raw response for batch i of the
// labeled examples.
func RecordedBatchFile(i int) string { return fmt.Sprintf("labeled_gemini_%d.json", i) }

// RecordFixtures sends the labeled examples of dir to the live API in
// batches of batch_size and writes the raw responses next to them, for the
// offline quality test.
func (g *Gemini) RecordFixtures(ctx context.Context, dir string) error {
	ls, err := LoadLabeled(filepath.Join(dir, LabeledFile))
	if err != nil {
		return err
	}
	posts := LabeledPosts(ls)
	for i, start := 0, 0; start < len(posts); i, start = i+1, start+g.cfg.BatchSize {
		batch := posts[start:min(start+g.cfg.BatchSize, len(posts))]
		body, err := g.request(batch)
		if err != nil {
			return err
		}
		raw, err := g.do(ctx, body)
		if err != nil {
			return fmt.Errorf("record batch %d: %w", i, err)
		}
		path := filepath.Join(dir, RecordedBatchFile(i))
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			return fmt.Errorf("record batch %d: %w", i, err)
		}
		fmt.Printf("%s: %d posts\n", path, len(batch))
	}
	return nil
}
