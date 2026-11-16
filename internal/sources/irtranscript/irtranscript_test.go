package irtranscript

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"macro-collector/internal/sources/httpx"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "ir", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Recorded 2026-10-30 (LOOKUP R1): MSFT FY27 Q1 event page, Alphabet Q3
// 2026 event page and its IR feed for 2026.
const (
	msftPage  = "msft_fy-2027_q1.html"
	googlPage = "googl_2026-Q3-Earnings-Call.html"
	googlFeed = "googl_feed_2026.json"
)

func TestOutlookMicrosoft(t *testing.T) {
	text, err := Text(read(t, msftPage))
	if err != nil {
		t.Fatal(err)
	}
	o := Outlook(text)
	if len(o) != 19 {
		t.Fatalf("outlook has %d paragraphs:\n%s", len(o), strings.Join(o, "\n"))
	}
	if !strings.HasPrefix(o[0], "Now, moving to the outlook for our second quarter") {
		t.Errorf("first = %q", o[0])
	}
	if !strings.HasPrefix(o[len(o)-1], "Given our strong business performance, we expect") {
		t.Errorf("last = %q", o[len(o)-1])
	}
	for _, p := range o {
		if strings.Contains(p, "OPERATOR") || strings.Contains(p, "Q&A") {
			t.Errorf("questions in the outlook: %q", p)
		}
	}
}

func TestOutlookAlphabet(t *testing.T) {
	text, err := Text(read(t, googlPage))
	if err != nil {
		t.Fatal(err)
	}
	o := Outlook(text)
	if len(o) != 3 || !strings.HasPrefix(o[0], "I'll now turn to our outlook for the remainder of 2026") ||
		!strings.Contains(o[2], "CapEx guidance range") {
		t.Errorf("outlook:\n%s", strings.Join(o, "\n"))
	}
}

// Before the call the page has the safe harbor and no Outlook block;
// prepared remarks without Q&A run to the end.
func TestOutlookEdges(t *testing.T) {
	if o := Outlook("We will post our prepared remarks to our website immediately following the call.\n\nForward-looking statements."); o != nil {
		t.Errorf("pre-call page outlook = %q", o)
	}
	o := Outlook("Revenue grew.\n\nNow, moving to the outlook for Q2.\n\nWe expect revenue of $95 billion.\n\nThank you.")
	if len(o) != 2 || o[1] != "We expect revenue of $95 billion." {
		t.Errorf("prepared remarks outlook = %q", o)
	}
}

func TestEventPage(t *testing.T) {
	url, ok, err := EventPage(read(t, googlFeed), 2026, 3)
	if err != nil || !ok || url != "https://abc.xyz/investor/events/event-details/2026/2026-Q3-Earnings-Call-2026-Ujou16CTw9/default.aspx" {
		t.Errorf("Q3 2026 = %q %v %v", url, ok, err)
	}
	// Q2 2026 is "Second Quarter 2026"; another year is not matched.
	if url, ok, _ := EventPage(read(t, googlFeed), 2026, 2); !ok || !strings.Contains(url, "2026-Q2-Earnings-Call") {
		t.Errorf("Q2 2026 = %q %v", url, ok)
	}
	if _, ok, _ := EventPage(read(t, googlFeed), 2025, 3); ok {
		t.Error("Q3 2025 matched the 2026 feed")
	}
	if _, ok, _ := EventPage(read(t, googlFeed), 2026, 4); ok {
		t.Error("Q4 2026 found before it happened")
	}
}

func TestPageURL(t *testing.T) {
	got := PageURL("https://www.microsoft.com/en-us/investor/events/fy-{fy}/earnings-fy-{fy}-q{q}", 2027, 1)
	if got != "https://www.microsoft.com/en-us/investor/events/fy-2027/earnings-fy-2027-q1" {
		t.Errorf("url = %s", got)
	}
}

// Fetch through a feed: feed → event page → transcript; a missing page
// (404) and a page without Outlook are "not yet", not errors.
func TestFetch(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed/2026":
			feed := strings.ReplaceAll(string(read(t, googlFeed)), `https:\/\/abc.xyz\/investor\/events\/event-details\/2026\/2026-Q3-Earnings-Call-2026-Ujou16CTw9\/default.aspx`, srv.URL+"/q3")
			w.Write([]byte(feed))
		case "/q3":
			w.Write(read(t, googlPage))
		case "/fy-2027/q2":
			w.Write([]byte("<html><body><p>We will post our prepared remarks after the call.</p></body></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(httpx.New(httpx.WithRetries(0, 0)), nil)
	ctx := context.Background()

	tr, ok, err := c.Fetch(ctx, "", srv.URL+"/feed/{fy}", 2026, 3)
	if err != nil || !ok || tr.URL != srv.URL+"/q3" || len(Outlook(tr.Text)) != 3 {
		t.Errorf("feed fetch = %v %v %v", tr.URL, ok, err)
	}
	if _, ok, err := c.Fetch(ctx, srv.URL+"/fy-{fy}/q{q}", "", 2027, 2); ok || err != nil {
		t.Errorf("pre-call page = %v %v", ok, err)
	}
	if _, ok, err := c.Fetch(ctx, srv.URL+"/fy-{fy}/q{q}", "", 2027, 3); ok || err != nil {
		t.Errorf("404 page = %v %v", ok, err)
	}
	if _, ok, err := c.Fetch(ctx, "", srv.URL+"/nofeed/{fy}", 2026, 3); ok || err != nil {
		t.Errorf("404 feed = %v %v", ok, err)
	}
}
