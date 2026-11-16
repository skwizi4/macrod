package irpress

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
)

const fixtures = "../../../testdata/irpress/"

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixtures + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseFeedSiemens(t *testing.T) {
	docs, err := ParseFeed(read(t, "siemens.rss"), config.IssuerPress{Kind: config.PressRSS}, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 3 {
		t.Fatalf("releases since 11-05 = %d, want 3", len(docs))
	}
	d := docs[0]
	if !strings.Contains(d.Title, "Lauterbach") || d.PublishedAt.Format(time.RFC3339) != "2026-11-09T11:40:27Z" ||
		!strings.HasPrefix(d.URL, "https://press.siemens.com/global/en/pressrelease/") {
		t.Errorf("first release %q %s %s", d.Title, d.PublishedAt, d.URL)
	}
	if !strings.Contains(d.Body, "Lauterbach") || len(d.Body) < 500 {
		t.Errorf("body %d chars: %.200q", len(d.Body), d.Body)
	}
}

// SAP's feed is mostly marketing: only titles with a configured word stay.
func TestParseFeedTitleWords(t *testing.T) {
	p := config.IssuerPress{Kind: config.PressRSS, Title: regexp.MustCompile(`(?i)(?:\bAstraZeneca\b|\bResults\b)`)}
	docs, err := ParseFeed(read(t, "sap.rss"), p, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || !strings.Contains(docs[0].Title, "AstraZeneca") {
		t.Fatalf("releases %v", docs)
	}
}

type pages map[string][]byte

func (p pages) Get(_ context.Context, url string) ([]byte, error) {
	if b, ok := p[url]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("unexpected request %s", url)
}

func TestFetchHTMLList(t *testing.T) {
	p := config.IssuerPress{Kind: config.PressHTML, URL: "https://www.airbus.com/en/newsroom/press-releases",
		Item: "article.awx-card", Body: "article.node--press-release",
		Link: regexp.MustCompile(`/en/newsroom/press-releases/\d{4}-\d{2}-`), Excluded: regexp.MustCompile(`(?i)share buybacks for week`)}
	const cathay = "https://www.airbus.com/en/newsroom/press-releases/2026-11-the-cathay-group-orders-14-additional-airbus-aircraft"
	items, err := ParseList(read(t, "airbus_list.html"), p, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 7 {
		t.Errorf("items = %d, want 7 (8 cards, one weekly buyback report)", len(items))
	}
	for _, it := range items {
		if strings.Contains(it.Title, "buybacks for week") {
			t.Errorf("excluded %q", it.Title)
		}
	}
	if items[0].URL != cathay || items[0].Title != "The Cathay Group orders 14 additional Airbus aircraft" ||
		items[0].Published.Format(time.RFC3339) != "2026-11-11T03:59:24Z" {
		t.Errorf("first item %+v", items[0])
	}
	srv := pages{p.URL: read(t, "airbus_list.html"), cathay: read(t, "airbus_article.html")}
	c := New(srv, nil)
	docs, err := c.Fetch(context.Background(), "AIR", "eu", p, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Source != "ir:AIR" || docs[0].DocType != DocType || docs[0].Region != "eu" {
		t.Fatalf("docs %+v", docs)
	}
	if !strings.Contains(docs[0].Body, "A330-900s") {
		t.Errorf("body %.300q", docs[0].Body)
	}
	// A stored release is not fetched again.
	docs, err = c.Fetch(context.Background(), "AIR", "eu", p, time.Time{}, func(string) bool { return true })
	if err != nil || len(docs) != 0 {
		t.Errorf("known releases: %d, %v", len(docs), err)
	}
}
