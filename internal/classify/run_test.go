package classify

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

var runNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) // 05:00 in Los Angeles

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addPost(t *testing.T, s *store.Store, source, id, text string, at time.Time) {
	t.Helper()
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, _, err := store.UpsertDocument(tx, model.Document{
			Source: source, ExternalID: id, DocType: "post", Region: "usa", PublishedAt: at, Body: text,
		}, at)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func testRunner(t *testing.T, s *store.Store, g *Gemini) *Runner {
	return &Runner{
		Store: s, Keywords: testKeywords(t), Gemini: g, Prompt: testPrompt(t),
		BatchSize: 2, DailyLimit: 3, MinConf: 0.5,
		Sources: map[string]Source{
			"telegram:marketnews_ua": {Channel: "marketnews_ua"},
			"telegram:macro_views":  {Channel: "macro_views", DefaultTag: "usa_fiscal"},
		},
		Now: func() time.Time { return runNow },
	}
}

// tags maps "source/external_id" to "classifier:primary,tag…" or "junk".
func tags(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	rows, err := s.ReadDB().Query(`SELECT d.source || '/' || d.external_id, d.classifier, d.is_junk,
		coalesce((SELECT group_concat(tag, ',') FROM (SELECT tag FROM document_tags WHERE document_id = d.id ORDER BY is_primary DESC, score DESC, tag)), '')
		FROM documents d WHERE d.classified_at IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var ref, by, ts string
		var junk bool
		if err := rows.Scan(&ref, &by, &junk, &ts); err != nil {
			t.Fatal(err)
		}
		if junk {
			ts = "junk"
		}
		out[ref] = by + ":" + ts
	}
	return out
}

// answerFor answers posts at the given batch positions with usa_monetary.
func answerFor(ids ...string) []byte {
	var as []string
	for _, id := range ids {
		as = append(as, `{"id":"`+id+`","tags":[{"tag":"usa_monetary","confidence":0.9}],"primary":"usa_monetary","is_junk":false,"reason":"Fed."}`)
	}
	return modelResponse("["+strings.Join(as, ",")+"]", "STOP")
}

// An unusable answer or a missing post only affects its own batch: the
// post goes to keywords, Gemini goes on with the next batch.
func TestRunPartialAnswers(t *testing.T) {
	s := openStore(t)
	at := runNow.Add(-time.Hour)
	addPost(t, s, "telegram:marketnews_ua", "1", "Пауелл і ФРС: ставка", at)
	addPost(t, s, "telegram:marketnews_ua", "3", "Мінфін США: аукціон і дефіцит", at.Add(time.Minute))
	addPost(t, s, "telegram:marketnews_ua", "4", "a", at.Add(2*time.Minute))
	addPost(t, s, "telegram:marketnews_ua", "5", "b", at.Add(3*time.Minute))
	gs := newGeminiServer(t,
		[2]any{200, modelResponse(`[{"id":"2","tags":[],"primary":null,"is_junk":true,"reason":""},{"id":"3","tags":[],"primary":null,"is_junk":false,"reason":""}]`, "STOP")},
		[2]any{200, answerFor("1", "2")})
	st, err := testRunner(t, s, testGemini(t, gs)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Requests != 2 || st.Gemini != 3 || st.Keywords != 1 || st.Unanswered != 1 {
		t.Errorf("stats = %+v", st)
	}
	got := tags(t, s)
	if got["telegram:marketnews_ua/1"] != "keywords:usa_monetary" || got["telegram:marketnews_ua/3"] != "gemini:junk" ||
		got["telegram:marketnews_ua/4"] != "gemini:usa_monetary" {
		t.Errorf("tags = %v", got)
	}

	// A batch without a single usable answer: keywords for it, Gemini stays on.
	addPost(t, s, "telegram:marketnews_ua", "6", "Пауелл і ФРС знову", at.Add(4*time.Minute))
	addPost(t, s, "telegram:marketnews_ua", "7", "c", at.Add(5*time.Minute))
	addPost(t, s, "telegram:marketnews_ua", "8", "d", at.Add(6*time.Minute))
	gs.responses = [][2]any{{200, modelResponse(`[{"id":"9"}]`, "STOP")}, {200, answerFor("1")}}
	r := testRunner(t, s, testGemini(t, gs))
	r.DailyLimit = 10
	st, err = r.Run(context.Background())
	if err != nil || st.Requests != 2 || st.Keywords != 2 || st.Gemini != 1 || st.GeminiErr == nil {
		t.Errorf("unusable batch: %+v, %v", st, err)
	}
}

func TestRunGeminiCacheAndDefaultTag(t *testing.T) {
	s := openStore(t)
	at := runNow.Add(-time.Hour)
	addPost(t, s, "telegram:marketnews_ua", "1", "ФРС утримала ставку", at)
	addPost(t, s, "telegram:marketnews_ua", "2", "Пауелл: інфляція знижується", at.Add(time.Minute))
	addPost(t, s, "telegram:macro_views", "3", "Fed: rate path", at.Add(2*time.Minute))
	gs := newGeminiServer(t,
		[2]any{200, answerFor("1", "2")},
		[2]any{200, answerFor("1")})
	r := testRunner(t, s, testGemini(t, gs))
	st, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Posts != 3 || st.Gemini != 3 || st.Requests != 2 || st.Keywords != 0 || st.GeminiErr != nil {
		t.Errorf("stats = %+v", st)
	}
	got := tags(t, s)
	// macro_views has default_tag usa_fiscal: added with min_confidence.
	if got["telegram:marketnews_ua/1"] != "gemini:usa_monetary" || got["telegram:macro_views/3"] != "gemini:usa_monetary,usa_fiscal" {
		t.Errorf("tags = %v", got)
	}
	if n, _ := GeminiRequestsToday(context.Background(), s, runNow); n != 2 {
		t.Errorf("requests today = %d", n)
	}

	// The same text in another channel is answered from the cache: no
	// request (the server has no responses left).
	addPost(t, s, "telegram:macro_views", "4", "ФРС утримала ставку", at.Add(3*time.Minute))
	st, err = r.Run(context.Background())
	if err != nil || st.Cached != 1 || st.Requests != 0 {
		t.Fatalf("cached run: %+v, %v", st, err)
	}
	if got := tags(t, s)["telegram:macro_views/4"]; got != "gemini:usa_monetary,usa_fiscal" {
		t.Errorf("cached tags = %s", got)
	}

	// A changed prompt makes the cache stale.
	r.Prompt.Hash = "other"
	addPost(t, s, "telegram:marketnews_ua", "5", "ФРС утримала ставку", at.Add(4*time.Minute))
	gs.responses = [][2]any{{200, answerFor("1")}}
	if st, err = r.Run(context.Background()); err != nil || st.Cached != 0 || st.Requests != 1 {
		t.Errorf("stale cache run: %+v, %v", st, err)
	}

	// Nothing left: no requests, no work.
	if st, err = r.Run(context.Background()); err != nil || st.Posts != 0 {
		t.Errorf("idle run: %+v, %v", st, err)
	}
}

// After a Gemini failure the batch and the rest of the run use keywords.
func TestRunFallbackToKeywords(t *testing.T) {
	s := openStore(t)
	at := runNow.Add(-time.Hour)
	addPost(t, s, "telegram:marketnews_ua", "1", "🏦Ймовірність утримання ставки ФРС вже 92%", at)
	addPost(t, s, "telegram:marketnews_ua", "2", "🎁 РОЗІГРАШ! Підпишись на канал", at.Add(time.Minute))
	addPost(t, s, "telegram:marketnews_ua", "3", "📉 BTC - найгірший день", at.Add(2*time.Minute))
	gs := newGeminiServer(t, [2]any{403, []byte(`{"error":{"message":"key blocked","status":"PERMISSION_DENIED"}}`)})
	st, err := testRunner(t, s, testGemini(t, gs)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Keywords != 3 || st.Gemini != 0 || st.Requests != 1 || len(gs.requests) != 1 || st.GeminiErr == nil || st.Junk != 1 || st.Uncategorized != 1 {
		t.Errorf("stats = %+v (%d requests)", st, len(gs.requests))
	}
	got := tags(t, s)
	if got["telegram:marketnews_ua/1"] != "keywords:usa_monetary" || got["telegram:marketnews_ua/2"] != "keywords:junk" || got["telegram:marketnews_ua/3"] != "keywords:" {
		t.Errorf("tags = %v", got)
	}
	var cached int
	s.ReadDB().QueryRow(`SELECT count(*) FROM classify_cache`).Scan(&cached)
	if cached != 0 {
		t.Errorf("keyword results cached: %d", cached)
	}
}

func TestRunDailyLimitAndNoKey(t *testing.T) {
	s := openStore(t)
	addPost(t, s, "telegram:marketnews_ua", "1", "ФРС утримала ставку", runNow.Add(-time.Hour))
	// Three requests already made today (Pacific): the limit of 3 is reached.
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		return store.SetFetchOK(tx, GeminiCounter, "2026-10-25:3", runNow)
	})
	if err != nil {
		t.Fatal(err)
	}
	gs := newGeminiServer(t)
	st, err := testRunner(t, s, testGemini(t, gs)).Run(context.Background())
	if err != nil || st.Keywords != 1 || st.Requests != 0 || st.GeminiErr == nil || !strings.Contains(st.GeminiErr.Error(), "daily limit") {
		t.Errorf("limit: %+v, %v", st, err)
	}

	// A count from yesterday does not limit today.
	r := testRunner(t, s, nil)
	r.Now = func() time.Time { return runNow.Add(24 * time.Hour) }
	if n, _ := GeminiRequestsToday(context.Background(), s, r.Now()); n != 0 {
		t.Errorf("tomorrow count = %d", n)
	}

	// No key: keywords only.
	addPost(t, s, "telegram:marketnews_ua", "2", "Пауелл і ФРС", runNow)
	if st, err := r.Run(context.Background()); err != nil || st.Keywords != 1 || st.Requests != 0 {
		t.Errorf("no key: %+v, %v", st, err)
	}
}

// An edited post is classified again (store.UpsertDocument clears it).
func TestRunEditedPost(t *testing.T) {
	s := openStore(t)
	at := runNow.Add(-time.Hour)
	addPost(t, s, "telegram:marketnews_ua", "1", "Ринки сьогодні спокійні", at)
	r := testRunner(t, s, nil)
	if st, _ := r.Run(context.Background()); st.Uncategorized != 1 {
		t.Fatalf("first run: %+v", st)
	}
	addPost(t, s, "telegram:marketnews_ua", "1", "Ринки сьогодні спокійні: ФРС утримала ставку, Пауелл обережний", at)
	if st, _ := r.Run(context.Background()); st.Posts != 1 {
		t.Fatalf("edited run: %+v", st)
	}
	if got := tags(t, s)["telegram:marketnews_ua/1"]; got != "keywords:usa_monetary" {
		t.Errorf("edited tags = %s", got)
	}
}
