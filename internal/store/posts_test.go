package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"macro-collector/internal/model"
)

func TestPostsClassificationAndCounts(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := at("2026-10-25T12:00:00Z")
	var ids []int64
	for i, body := range []string{"ФРС утримала ставку", "Розіграш!", "Нічого цікавого", "ФРС утримала ставку"} {
		src := "telegram:a"
		if i == 3 {
			src = "rss:b"
		}
		id, _ := upsertDoc(t, s, model.Document{Source: src, ExternalID: string(rune('1' + i)), DocType: "post",
			PublishedAt: now.Add(-time.Duration(i) * time.Hour), Body: body}, now)
		ids = append(ids, id)
	}
	// An official text is never a post.
	upsertDoc(t, s, statement("20260916", "The Committee decided."), now)

	pending, err := s.UnclassifiedPosts(ctx, 10)
	if err != nil || len(pending) != 4 || pending[0].ID != ids[3] {
		t.Fatalf("pending = %d (first %v), %v", len(pending), pending, err)
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		for _, p := range []PostTags{
			{DocumentID: ids[0], Classifier: "gemini", Tags: []TagScore{{"usa_monetary", 0.9, true}, {"usa_fiscal", 0.6, false}}},
			{DocumentID: ids[1], Classifier: "keywords", IsJunk: true},
			{DocumentID: ids[2], Classifier: "gemini"},
		} {
			if err := SavePostTags(tx, p, now); err != nil {
				return err
			}
		}
		if err := SetDedup(tx, ids[3], "h", ids[0]); err != nil {
			return err
		}
		return PutClassifyCache(tx, ContentHash("ФРС утримала ставку"), "m", "p", `{"tags":[]}`, now)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM document_tags WHERE classifier = 'gemini'`); n != 2 {
		t.Errorf("gemini tags = %d", n)
	}
	c, err := s.PostClassifyCounts(ctx, now.AddDate(0, 0, -7))
	want := ClassifyCounts{Gemini: 2, Keywords: 1, Junk: 1, Uncategorized: 1, Pending: 1, Duplicates: 1}
	if err != nil || c != want {
		t.Errorf("counts = %+v, %v; want %+v", c, err, want)
	}
	srcs, err := s.PostSources(ctx, now.Add(-90*time.Minute))
	if err != nil || len(srcs) != 2 || srcs[1].Source != "telegram:a" || srcs[1].Posts != 3 || srcs[1].Recent != 2 || !srcs[1].LastPost.Equal(now) {
		t.Errorf("sources = %+v, %v", srcs, err)
	}

	cached, err := s.CachedClassifications(ctx, []string{ContentHash("ФРС утримала ставку"), "x"}, "m", "p")
	if err != nil || len(cached) != 1 {
		t.Errorf("cache = %v, %v", cached, err)
	}
	if other, _ := s.CachedClassifications(ctx, []string{ContentHash("ФРС утримала ставку")}, "m", "other"); len(other) != 0 {
		t.Error("cache of another prompt returned")
	}

	// Reprocess the last 90 minutes: posts 1 and 2 (now, −1h).
	var n int64
	err = s.Write(ctx, func(tx *sql.Tx) error {
		n, err = ResetClassification(tx, now.Add(-90*time.Minute))
		return err
	})
	if err != nil || n != 2 {
		t.Fatalf("reset = %d, %v", n, err)
	}
	if left := count(t, s, `SELECT count(*) FROM document_tags`); left != 0 {
		t.Errorf("tags left = %d", left)
	}
	if pending, _ := s.UnclassifiedPosts(ctx, 10); len(pending) != 3 {
		t.Errorf("pending after reset = %d", len(pending))
	}
	from, to, ok, err := s.UndedupedRange(ctx)
	if err != nil || !ok || !from.Equal(now.Add(-2*time.Hour)) || !to.Equal(now) {
		t.Errorf("undeduped = %v…%v %v %v", from, to, ok, err)
	}
}
