package rss

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/sources/httpx"
)

func testFeed(t *testing.T, historyDays int) *Feed {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "rss", "macro_letter_feed.xml"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	t.Cleanup(srv.Close)
	f := New(config.RSSFeed{Name: "macro_letter", URL: srv.URL + "/feed", Region: "usa", Type: "opinion", HistoryDays: historyDays},
		httpx.New(httpx.WithInterval(0), httpx.WithRetries(0, 0)), nil)
	f.Now = func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) }
	return f
}

func TestFetchHistory(t *testing.T) {
	// The newest article is 2026-07-13: 30 days of history bring nothing
	// and the cursor stays empty, so the next fetch looks back again.
	docs, cursor, err := testFeed(t, 30).FetchDocuments(context.Background(), "")
	if err != nil || len(docs) != 0 || cursor != "" {
		t.Fatalf("30 days: %d docs, cursor %q, %v", len(docs), cursor, err)
	}

	f := testFeed(t, 200) // since 2026-04-08
	docs, cursor, err = f.FetchDocuments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || cursor != "2026-07-13T07:14:51Z" {
		t.Fatalf("200 days: %d docs, cursor %q", len(docs), cursor)
	}
	d := docs[1]
	if f.Name() != "rss:macro_letter" || d.Source != f.Name() || d.DocType != DocType || d.Region != "usa" || !d.IsOpinion ||
		d.ExternalID != "https://macro_letter.substack.com/p/the-maradona-theory-of-interest-rates" ||
		d.URL != "https://macro_letter.substack.com/p/the-maradona-theory-of-interest-rates" ||
		d.Title != "The Maradona Theory of Interest Rates" || d.Author != "Alfonso Peccatiello (Alf)" ||
		!d.PublishedAt.Equal(time.Date(2026, 7, 13, 7, 14, 51, 0, time.UTC)) {
		t.Errorf("doc = %+v", d)
	}
	if !strings.HasPrefix(d.Body, "Hi everyone, Alf here - I am back with another research piece for you.\n\nAs always,") ||
		!strings.Contains(d.Body, "The Maradona theory of interest rates is likely to materialize.") {
		t.Errorf("body = %q…", d.Body[:200])
	}
	if strings.Contains(d.Body, "Subscribe now") || strings.Contains(d.Body, "Share") {
		t.Error("subscribe/share buttons left in the body")
	}
	if docs[0].PublishedAt.After(d.PublishedAt) {
		t.Error("docs not oldest first")
	}
}

func TestFetchSinceCursor(t *testing.T) {
	f := testFeed(t, 30)
	docs, cursor, err := f.FetchDocuments(context.Background(), "2026-05-26T06:18:58Z")
	if err != nil {
		t.Fatal(err)
	}
	// The cursor item itself comes back too (stored as unchanged).
	if len(docs) != 2 || cursor != "2026-07-13T07:14:51Z" {
		t.Errorf("docs = %d, cursor = %q", len(docs), cursor)
	}
	docs, cursor, err = f.FetchDocuments(context.Background(), "2026-07-14T00:00:00Z")
	if err != nil || len(docs) != 0 || cursor != "2026-07-14T00:00:00Z" {
		t.Errorf("no news: %d docs, cursor %q, %v", len(docs), cursor, err)
	}
	if _, _, err := f.FetchDocuments(context.Background(), "yesterday"); err == nil || !strings.Contains(err.Error(), "bad cursor") {
		t.Errorf("bad cursor err = %v", err)
	}
}

func TestFetchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusForbidden)
	}))
	defer srv.Close()
	f := New(config.RSSFeed{Name: "x", URL: srv.URL, HistoryDays: 30}, httpx.New(httpx.WithInterval(0)), nil)
	if _, _, err := f.FetchDocuments(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "rss x") {
		t.Errorf("err = %v", err)
	}
}

func TestHTMLText(t *testing.T) {
	in := `<p>Intro <strong>bold</strong>&nbsp;text.</p>
<div class="captioned-image-container"><figure><a class="image-link"><picture><img src="x.png"></picture></a><figcaption>Chart: US yields</figcaption></figure></div>
<ul><li><p>first</p></li><li>second</li></ul>
<hr>
<p>After the break.<br>New line.</p>
<p class="button-wrapper"><a class="button primary" href="/subscribe"><span>Subscribe now</span></a></p>`
	want := "Intro bold text.\n\nChart: US yields\n\n• first\n\n• second\n\nAfter the break.\nNew line."
	if got := htmlText(in); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if htmlText("  ") != "" {
		t.Error("blank input")
	}
}
