package scheduler

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/fedtexts"
)

type fakeChannel struct{ docs []model.Document }

func (fakeChannel) Name() string { return "telegram:marketnews_ua" }

func (f fakeChannel) FetchDocuments(context.Context, string) ([]model.Document, string, error) {
	return f.docs, "2", nil
}

// The posts job fetches, classifies (keywords: no Gemini key) and hashes
// for dedup in one pass.
func TestPostsJob(t *testing.T) {
	t.Setenv(config.EnvDataDir, t.TempDir())
	cfg, err := config.LoadAt(filepath.Join("..", "..", "testdata", "config", "us_eu.yaml"), filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	st := openStore(t)
	root := slog.New(slog.DiscardHandler)
	posts, err := collect.NewPosts(cfg, st, root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(posts.Sources) != 3 || posts.Classify.Gemini != nil { // 3 channels of the test config, no RSS
		t.Fatalf("sources = %d, gemini = %v", len(posts.Sources), posts.Classify.Gemini != nil)
	}
	at := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	posts.Sources = []sources.TextSource{fakeChannel{docs: []model.Document{
		{Source: "telegram:marketnews_ua", ExternalID: "1", DocType: "post", Region: "usa", PublishedAt: at,
			Body: "🏦Ймовірність утримання ставки ФРС вже 92%"},
		{Source: "telegram:marketnews_ua", ExternalID: "2", DocType: "post", Region: "usa", PublishedAt: at.Add(time.Minute),
			Body: "🎁 РОЗІГРАШ! Підпишись на канал партнера"},
	}}}
	s := New(Deps{Config: cfg, Store: st, FRED: fakeFull{&fakeFRED{}}, Fed: fedtexts.New(), Posts: posts,
		Root: root, FetchCron: "0 6 * * *"})
	var job *Job
	for i := range s.Jobs {
		if s.Jobs[i].Name == "posts" {
			job = &s.Jobs[i]
		}
	}
	if job == nil || job.Spec != "@every 15m0s" {
		t.Fatalf("posts job = %+v", job)
	}
	job.Run(context.Background())

	var classified, junk, tagged, hashed int
	st.ReadDB().QueryRow(`SELECT count(*), sum(is_junk), sum(dedup_hash IS NOT NULL) FROM documents WHERE classified_at IS NOT NULL`).
		Scan(&classified, &junk, &hashed)
	st.ReadDB().QueryRow(`SELECT count(*) FROM document_tags WHERE tag = 'usa_monetary' AND classifier = 'keywords'`).Scan(&tagged)
	if classified != 2 || junk != 1 || tagged != 1 || hashed != 2 {
		t.Errorf("classified %d, junk %d, tagged %d, hashed %d", classified, junk, tagged, hashed)
	}
	if fs, ok, _ := st.FetchStateOf(context.Background(), "telegram:marketnews_ua"); !ok || fs.Cursor != "2" {
		t.Errorf("cursor = %+v", fs)
	}
}
