package fedtexts

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"macro-collector/internal/model"
)

// fedSite serves testdata/fed by file name (the last path segment); pages
// that were not recorded are 404, like pages not yet published.
type fedSite struct {
	t        *testing.T
	mu       sync.Mutex
	requests []string
}

func (s *fedSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.URL.Path)
	s.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(fixtureDir, path.Base(r.URL.Path)))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if path.Ext(r.URL.Path) == ".pdf" {
		w.Header().Set("Last-Modified", pdfPosted) // as recorded
	}
	w.Write(b)
}

func (s *fedSite) reset() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests
	s.requests = nil
	return r
}

func newSite(t *testing.T) (*fedSite, string) {
	t.Helper()
	site := &fedSite{t: t}
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	return site, srv.URL
}

func ny(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func newFeed(t *testing.T, base, name string) *Feed {
	return &Feed{Client: newTestClient(), FeedName: name, URL: base + "/feeds/" + name + ".xml",
		BaseURL: base, Location: ny(t), Log: slog.New(slog.DiscardHandler)}
}

func byID(docs []model.Document) map[string]model.Document {
	out := make(map[string]model.Document)
	for _, d := range docs {
		out[d.ExternalID] = d
	}
	return out
}

func TestFeedPressMonetary(t *testing.T) {
	site, base := newSite(t)
	f := newFeed(t, base, FeedPressMonetary)
	docs, cursor, err := f.FetchDocuments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	got := byID(docs)
	ids := slices.Sorted(func(yield func(string) bool) {
		for id := range got {
			if !yield(id) {
				return
			}
		}
	})
	want := []string{"fomcminutes20260916", "fomcprojtabl20260916", "monetary20260729a", "monetary20260916a"}
	if !slices.Equal(ids, want) {
		t.Fatalf("documents = %v, want %v", ids, want)
	}

	st := got["monetary20260916a"]
	if st.DocType != DocStatement || st.Source != "fed" ||
		st.PublishedAt.Format(time.RFC3339) != "2026-09-16T18:00:00Z" || st.URL != base+"/newsevents/pressreleases/monetary20260916a.htm" {
		t.Errorf("statement = %+v", st)
	}
	if !strings.HasPrefix(st.Body, "The Federal Open Market Committee approved the following statement") ||
		strings.Contains(st.Body, "media inquiries") || strings.Contains(st.Body, "Implementation Note") {
		t.Errorf("statement body:\n%s", st.Body)
	}

	m := got["fomcminutes20260916"]
	if m.DocType != DocMinutes || m.Title != "Minutes of the Federal Open Market Committee, September 15-16, 2026" ||
		m.PublishedAt.Format(time.RFC3339) != "2026-10-07T18:00:00Z" || !strings.Contains(m.Body, "Developments in Financial Markets") ||
		strings.Contains(m.Body, "Return to text") {
		t.Errorf("minutes = %s %q %v, body %d chars", m.DocType, m.Title, m.PublishedAt, len(m.Body))
	}

	sep := got["fomcprojtabl20260916"]
	if sep.DocType != DocSEP || !strings.HasPrefix(sep.Body, "The attached tables and charts") ||
		!strings.Contains(sep.Body, "Table 1. Economic projections") || !strings.Contains(sep.Body, "Change in real GDP | 2.3 | 2.4 | 2.2 | 2.1 | 2.0 |") {
		t.Errorf("sep body:\n%s", sep.Body)
	}

	// Discount-rate minutes and other press releases are skipped without a
	// request; items whose pages are missing are left out of the cursor.
	reqs := site.reset()
	if slices.Contains(reqs, "/newsevents/pressreleases/monetary20261013a.htm") {
		t.Error("skipped release was fetched")
	}
	seen := strings.Fields(cursor)
	for _, id := range []string{"monetary20261013a", "monetary20260709a", "monetary20260916a", "monetary20261007a", "monetary20260916b"} {
		if !slices.Contains(seen, id) {
			t.Errorf("cursor lacks %s", id)
		}
	}
	failed := []string{"monetary20260819a", "monetary20260708a", "monetary20260617a", "monetary20260617b", "monetary20260520a", "monetary20260429a"}
	for _, id := range failed {
		if slices.Contains(seen, id) {
			t.Errorf("cursor has failed item %s", id)
		}
	}

	// Next poll: only the failed items are tried again.
	docs, cursor2, err := f.FetchDocuments(context.Background(), cursor)
	if err != nil || len(docs) != 0 || cursor2 != cursor {
		t.Fatalf("second poll: %d docs, cursor changed %v, %v", len(docs), cursor2 != cursor, err)
	}
	var pages []string
	for _, r := range site.reset() {
		if strings.HasPrefix(r, "/newsevents/") {
			pages = append(pages, strings.TrimSuffix(path.Base(r), ".htm"))
		}
	}
	slices.Sort(pages)
	slices.Sort(failed)
	if !slices.Equal(pages, failed) {
		t.Errorf("second poll fetched %v, want %v", pages, failed)
	}
}

func TestFeedSpeechesAndTestimony(t *testing.T) {
	_, base := newSite(t)
	docs, _, err := newFeed(t, base, FeedSpeeches).FetchDocuments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("speeches = %d, want 1 (only one page recorded)", len(docs))
	}
	sp := docs[0]
	if sp.ExternalID != "waller20261008a" || sp.DocType != DocSpeech || sp.Author != "Governor Christopher J. Waller" ||
		sp.Title != "The Signaling Value of the Summary of Economic Projections" ||
		sp.PublishedAt.Format(time.RFC3339) != "2026-10-08T08:30:00Z" ||
		strings.Contains(sp.Body, "views expressed here are my own") {
		t.Errorf("speech = %+v", sp)
	}

	docs, cursor, err := newFeed(t, base, FeedTestimony).FetchDocuments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	// Staff testimony (Guynn) is fetched but not kept; it still goes to the cursor.
	got := byID(docs)
	if len(docs) != 2 || got["warsh20260714a"].Author != "Chairman Kevin Warsh" || got["warsh20260714a"].DocType != DocTestimony {
		t.Fatalf("testimony = %+v", docs)
	}
	// The feed dates barr20241120a "Sat, 30 Dec 1899": the page date is used.
	if p := got["barr20241120a"].PublishedAt.Format(time.RFC3339); p != "2024-11-20T05:00:00Z" {
		t.Errorf("barr20241120a published %s, want page date 2024-11-20 00:00 ET", p)
	}
	if !slices.Contains(strings.Fields(cursor), "guynn20260326a") {
		t.Error("staff testimony not in cursor")
	}
}

func TestBoardMember(t *testing.T) {
	for speaker, want := range map[string]bool{
		"Governor Christopher J. Waller":                                     true,
		"Chairman Kevin Warsh":                                               true,
		"Vice Chair for Supervision Michelle W. Bowman":                      true,
		"Randall D. Guynn, Director, Division of Supervision and Regulation": false,
		"": false,
	} {
		if got := boardMember(speaker); got != want {
			t.Errorf("%q: %v", speaker, got)
		}
	}
}

func TestFetchStatement(t *testing.T) {
	_, base := newSite(t)
	c := newTestClient()
	d, err := FetchStatement(context.Background(), c, base, day("2026-09-16"), ny(t))
	if err != nil {
		t.Fatal(err)
	}
	if d.ExternalID != "monetary20260916a" || d.DocType != DocStatement || d.PublishedAt.Format(time.RFC3339) != "2026-09-16T18:00:00Z" {
		t.Errorf("statement = %+v", d)
	}
	// Not published yet.
	if _, err := FetchStatement(context.Background(), c, base, day("2026-10-28"), ny(t)); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	// The 'b' page of a decision day is not a statement.
	if _, err := statementDocument("monetary20260916b", readFixture(t, "monetary20260916b.htm"), time.Time{}, ny(t)); err == nil {
		t.Error("SEP release accepted as a statement")
	}
}

func TestBeigeBook(t *testing.T) {
	_, base := newSite(t)
	s := &BeigeBook{Client: newTestClient(), URL: base + "/monetarypolicy/publications/beige-book-default.htm", Location: ny(t)}
	docs, cursor, err := s.FetchDocuments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || cursor != "beigebook202610" {
		t.Fatalf("docs = %d, cursor %q", len(docs), cursor)
	}
	d := docs[0]
	if d.ExternalID != "beigebook202610" || d.DocType != DocBeigeBook || d.PublishedAt.Format(time.RFC3339) != "2026-10-14T18:00:00Z" ||
		d.Title != "Beige Book — National Summary, October 2026" || !strings.Contains(d.Body, "Overall Economic Activity") {
		t.Errorf("beige book = %+v", d)
	}
	if docs, cursor2, err := s.FetchDocuments(context.Background(), cursor); err != nil || len(docs) != 0 || cursor2 != cursor {
		t.Errorf("repeat: %d docs, %q, %v", len(docs), cursor2, err)
	}
}
