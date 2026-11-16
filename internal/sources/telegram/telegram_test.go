package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/sources/httpx"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "telegram", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// server serves recorded pages by query ("" = latest page) and records the
// queries it got.
func server(t *testing.T, pages map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.RawQuery)
		mu.Unlock()
		name, ok := pages[r.URL.RawQuery]
		if !ok {
			t.Errorf("unexpected request %s", r.URL)
			http.Error(w, "no fixture", http.StatusInternalServerError)
			return
		}
		w.Write(fixture(t, name))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func testChannel(srv *httptest.Server, ch config.Channel) *Channel {
	c := New(srv.URL, ch, httpx.New(httpx.WithInterval(0), httpx.WithRetries(0, 0)), nil)
	c.Now = func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) }
	return c
}

func TestParsePage(t *testing.T) {
	p, err := parsePage(fixture(t, "marketnews_ua_latest.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Posts) != 20 || p.Posts[0].ID != 37226 || p.Posts[19].ID != 37245 || p.Before != 37226 {
		t.Fatalf("posts = %d (%d…%d), before = %d", len(p.Posts), p.Posts[0].ID, p.Posts[len(p.Posts)-1].ID, p.Before)
	}
	last := p.Posts[19]
	if last.Handle != "marketnews_ua" || last.Published != time.Date(2026, 10, 24, 19, 18, 2, 0, time.UTC) {
		t.Errorf("last = %+v", last)
	}
	// Emoji are kept, <br/><br/> is a blank line, entities are decoded.
	if !strings.HasPrefix(last.Text, "🔥Ініціатива щодо Starlink все ж належала Маску, а не Трампу!\n\nІлон пояснив") {
		t.Errorf("text = %q", last.Text[:120])
	}
	if !p.Posts[0].HasPhoto || p.Posts[1].HasPhoto || p.Posts[3].HasPhoto {
		t.Errorf("photo flags 37226/37227/37229 (video) = %v/%v/%v", p.Posts[0].HasPhoto, p.Posts[1].HasPhoto, p.Posts[3].HasPhoto)
	}
	// 37227 replies to 37217: the quoted post is not part of its text.
	if got := p.Posts[1].Text; got != "🔽📖$META -7%" {
		t.Errorf("reply text = %q", got)
	}

	p, err = parsePage(fixture(t, "macro_views_latest.html"))
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[int64]string)
	for _, ps := range p.Posts {
		ids[ps.ID] = ps.Text
	}
	if len(p.Posts) != 19 || ids[367] != "" {
		t.Errorf("posts = %d, 367 (photo without caption) = %q", len(p.Posts), ids[367])
	}
	if _, ok := ids[378]; ok || ids[377] == "" {
		t.Error("album 377–378 should be one post 377")
	}

	p, err = parsePage(fixture(t, "macro_views_before_300.html"))
	if err != nil {
		t.Fatal(err)
	}
	if f := p.Posts[0]; f.ID != 277 || f.ForwardedFrom != "credit_notes/2740" || !strings.HasPrefix(f.Text, "Проделана тьма работы") {
		t.Errorf("forward = %+v", f)
	}
	if p.Posts[1].ForwardedFrom != "" {
		t.Errorf("not a forward: %+v", p.Posts[1])
	}

	p, err = parsePage(fixture(t, "macro_views_before_360.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ps := range p.Posts {
		if ps.ID == 353 && ps.Text != "" {
			t.Errorf("service message 353 (pinned a photo) has text %q", ps.Text)
		}
	}

	p, err = parsePage(fixture(t, "marketnews_ua_after_37245_empty.html"))
	if err != nil || len(p.Posts) != 0 {
		t.Errorf("empty page: %d posts, %v", len(p.Posts), err)
	}
}

// The first fetch pages back until posts are older than history_days:
// 60 days from 2026-10-25 12:00 UTC keep 2026-08-26 12:00 onwards.
func TestFetchHistory(t *testing.T) {
	srv, got := server(t, map[string]string{
		"":           "macro_views_latest.html",
		"before=360": "macro_views_before_360.html",
	})
	c := testChannel(srv, config.Channel{Handle: "macro_views", Region: "usa", Type: "opinion", HistoryDays: 60})
	docs, cursor, err := c.FetchDocuments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(*got, ",") != ",before=360" {
		t.Errorf("requests = %q", *got)
	}
	// 19 posts on the latest page minus 367 (no text), plus 359 (2026-08-27).
	if len(docs) != 19 || cursor != "379" {
		t.Fatalf("docs = %d, cursor = %s", len(docs), cursor)
	}
	first, last := docs[0], docs[len(docs)-1]
	if first.ExternalID != "359" || last.ExternalID != "379" {
		t.Errorf("order = %s … %s", first.ExternalID, last.ExternalID)
	}
	if c.Name() != "telegram:macro_views" || last.Source != c.Name() || last.DocType != DocType || last.Region != "usa" ||
		!last.IsOpinion || last.Author != "@macro_views" || last.URL != srv.URL+"/macro_views/379" ||
		!last.PublishedAt.Equal(time.Date(2026, 10, 21, 11, 20, 20, 0, time.UTC)) ||
		last.Views != 778 || !last.ViewsAt.Equal(c.Now()) {
		t.Errorf("doc = %+v", last)
	}
}

func TestParseViews(t *testing.T) {
	for in, want := range map[string]int64{"843": 843, "5.61K": 5610, "12K": 12000, "1.2M": 1200000, "": 0, "n/a": 0} {
		if got := parseViews(in); got != want {
			t.Errorf("parseViews(%q) = %d, want %d", in, got, want)
		}
	}
}

// Views re-reads the pages back to the cutoff (37226 is older), posts
// without text included.
func TestViews(t *testing.T) {
	srv, got := server(t, map[string]string{"": "marketnews_ua_latest.html"})
	c := testChannel(srv, config.Channel{Handle: "marketnews_ua", Region: "usa", Type: "news"})
	views, err := c.Views(context.Background(), time.Date(2026, 10, 22, 13, 45, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || views["37226"].Views != 0 || views["37227"].Views != 5590 || views["37228"].Views != 5630 ||
		views["37227"].HasPhoto || !views["37230"].HasPhoto {
		t.Errorf("requests = %q, views = %v", *got, views)
	}
}

// Later fetches read ?after=<cursor> until a page brings nothing new.
func TestFetchNewer(t *testing.T) {
	srv, got := server(t, map[string]string{
		"after=37230": "marketnews_ua_after_37230.html",
		"after=37245": "marketnews_ua_after_37245_empty.html",
	})
	c := testChannel(srv, config.Channel{Handle: "marketnews_ua", Region: "usa", Type: "news", HistoryDays: 30})
	docs, cursor, err := c.FetchDocuments(context.Background(), "37230")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(*got, ",") != "after=37230,after=37245" {
		t.Errorf("requests = %q", *got)
	}
	if len(docs) != 15 || docs[0].ExternalID != "37231" || cursor != "37245" || docs[0].IsOpinion {
		t.Fatalf("docs = %d (first %+v), cursor = %s", len(docs), docs[0], cursor)
	}

	// Nothing new: no documents, cursor kept.
	docs, cursor, err = c.FetchDocuments(context.Background(), "37245")
	if err != nil || len(docs) != 0 || cursor != "37245" {
		t.Errorf("no news: %d docs, cursor %s, %v", len(docs), cursor, err)
	}
}

func TestFetchNoChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/"+strings.TrimPrefix(r.URL.Path, "/s/"), http.StatusFound)
	}))
	defer srv.Close()
	c := New(srv.URL, config.Channel{Handle: "no_such_channel", HistoryDays: 30}, NewHTTP(nil), nil)
	_, _, err := c.FetchDocuments(context.Background(), "")
	if !errors.Is(err, ErrNoChannel) || !strings.Contains(err.Error(), "no_such_channel") {
		t.Errorf("err = %v", err)
	}
	if _, _, err := c.FetchDocuments(context.Background(), "abc"); err == nil || !strings.Contains(err.Error(), "bad cursor") {
		t.Errorf("bad cursor err = %v", err)
	}
}
