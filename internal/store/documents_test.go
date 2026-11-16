package store

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"macro-collector/internal/model"
)

func upsertDoc(t *testing.T, s *Store, d model.Document, now time.Time) (int64, DocOutcome) {
	t.Helper()
	var id int64
	var out DocOutcome
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		id, out, err = UpsertDocument(tx, d, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id, out
}

func statement(date, body string) model.Document {
	return model.Document{
		Source: "fed", ExternalID: "monetary" + date + "a", DocType: "fomc_statement", Region: "usa",
		PublishedAt: at(date[:4] + "-" + date[4:6] + "-" + date[6:] + "T18:00:00Z"),
		Title:       "Federal Reserve issues FOMC statement", Body: body,
		URL: "https://www.federalreserve.gov/newsevents/pressreleases/monetary" + date + "a.htm",
	}
}

func TestUpsertDocument(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	t1 := at("2026-09-16T18:00:20Z")
	t2 := at("2026-09-16T18:15:00Z")
	t3 := at("2026-09-17T06:00:00Z")

	id, out := upsertDoc(t, s, statement("20260916", "The Committee decided to raise the target range."), t1)
	if out != DocInserted || id == 0 {
		t.Fatalf("insert: %d %s", id, out)
	}
	// Same text with different whitespace: unchanged, fetched_at kept.
	id2, out := upsertDoc(t, s, statement("20260916", "  The Committee decided\n to raise   the target range. "), t2)
	if out != DocUnchanged || id2 != id {
		t.Fatalf("whitespace: %d %s", id2, out)
	}
	// Corrected text: updated with a new fetched_at.
	id3, out := upsertDoc(t, s, statement("20260916", "The Committee decided to raise the target range by 1/4 point."), t3)
	if out != DocUpdated || id3 != id {
		t.Fatalf("update: %d %s", id3, out)
	}
	if n := count(t, s, `SELECT count(*) FROM documents`); n != 1 {
		t.Fatalf("rows = %d", n)
	}
	docs, err := s.LatestDocuments(ctx, "fed", "fomc_statement", 5)
	if err != nil || len(docs) != 1 {
		t.Fatalf("latest = %d, %v", len(docs), err)
	}
	d := docs[0]
	if !d.FetchedAt.Equal(t3) || d.Body != "The Committee decided to raise the target range by 1/4 point." ||
		d.ContentHash != ContentHash(d.Body) || d.Region != "usa" || d.Author != "" {
		t.Fatalf("stored = %+v", d)
	}
}

// An edited post keeps its id but loses its tags, classification and dedup
// state, so the classifier and dedup look at it again.
func TestUpsertEditedPostClearsClassification(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	post := model.Document{
		Source: "telegram:macro_views", ExternalID: "280", DocType: "post", Region: "usa",
		PublishedAt: at("2026-04-08T21:55:00Z"), Author: "@macro_views", Body: "CDS SoftBank 432bp",
		URL: "https://t.me/macro_views/280", ForwardedFrom: "credit_notes/2740",
	}
	id, _ := upsertDoc(t, s, post, at("2026-10-25T10:00:00Z"))
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE documents SET classified_at = ?, classifier = 'gemini', dedup_hash = 'h', dup_of = ? WHERE id = ?`,
			formatTS(at("2026-10-25T10:01:00Z")), id, id); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO document_tags (document_id, tag, score, is_primary, classifier) VALUES (?, 'usa_micro', 0.6, 1, 'gemini')`, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	post.Body = "CDS SoftBank 432bp — максимум с 2022 года"
	if id2, out := upsertDoc(t, s, post, at("2026-10-25T10:15:00Z")); out != DocUpdated || id2 != id {
		t.Fatalf("update: %d %s", id2, out)
	}
	if n := count(t, s, `SELECT count(*) FROM document_tags`); n != 0 {
		t.Errorf("tags = %d, want 0", n)
	}
	if n := count(t, s, `SELECT count(*) FROM documents WHERE classified_at IS NULL AND classifier IS NULL AND dedup_hash IS NULL AND dup_of IS NULL`); n != 1 {
		t.Error("classification and dedup state not cleared")
	}
	d, ok, err := s.Document(ctx, "telegram:macro_views", strconv.FormatInt(id, 10))
	if err != nil || !ok || d.ForwardedFrom != "credit_notes/2740" {
		t.Errorf("stored = %+v, %v, %v", d, ok, err)
	}
}

func TestLatestDocumentsOrder(t *testing.T) {
	s := openTest(t)
	now := at("2026-10-24T06:00:00Z")
	upsertDoc(t, s, statement("20260729", "July."), now)
	upsertDoc(t, s, statement("20260916", "September."), now)
	upsertDoc(t, s, statement("20260617", "June."), now)
	speech := statement("20261014", "Speech.")
	speech.ExternalID, speech.DocType = "bowman20261014a", "speech"
	upsertDoc(t, s, speech, now)

	docs, err := s.LatestDocuments(context.Background(), "fed", "fomc_statement", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].Body != "September." || docs[1].Body != "July." {
		t.Fatalf("latest two = %+v", docs)
	}
}

func TestContentHashIgnoresWhitespace(t *testing.T) {
	if ContentHash("a  b\n c") != ContentHash("a b c") {
		t.Error("whitespace changes the hash")
	}
	if ContentHash("a b c") == ContentHash("a b d") {
		t.Error("different texts share a hash")
	}
}

func TestDocumentByRef(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := upsertDoc(t, s, statement("20260916", "September."), at("2026-10-24T06:00:00Z"))
	for _, ref := range []string{"monetary20260916a", strconv.FormatInt(id, 10)} {
		d, ok, err := s.Document(ctx, "fed", ref)
		if err != nil || !ok || d.ID != id || d.Body != "September." {
			t.Errorf("%s: %+v ok=%v err=%v", ref, d, ok, err)
		}
	}
	if _, ok, err := s.Document(ctx, "fed", "nope"); ok || err != nil {
		t.Errorf("missing: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := s.Document(ctx, "ecb", "monetary20260916a"); ok {
		t.Error("found under another source")
	}
}

// The photo flag is stored on insert and filled in by a views refresh for
// posts fetched before schema v10.
func TestDocumentPhoto(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	post := func(id string, photo bool) model.Document {
		return model.Document{Source: "telegram:x", ExternalID: id, DocType: "telegram_post", Region: "usa",
			PublishedAt: at("2026-10-28T10:00:00Z"), Author: "@x", Body: "Risk on " + id, HasPhoto: photo}
	}
	upsertDoc(t, s, post("1", true), at("2026-10-28T10:01:00Z"))
	upsertDoc(t, s, post("2", false), at("2026-10-28T10:01:00Z"))
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := UpdateViews(tx, "telegram:x", map[string]PostSeen{"2": {Views: 900, HasPhoto: true}, "1": {HasPhoto: true}}, at("2026-11-01T15:00:00Z"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, views := range map[string]int64{"1": 0, "2": 900} {
		d, _, err := s.Document(ctx, "telegram:x", id)
		if err != nil || !d.HasPhoto || d.Views != views {
			t.Errorf("post %s: photo=%v views=%d err=%v", id, d.HasPhoto, d.Views, err)
		}
	}
}
