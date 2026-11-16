package treasury

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/htmltext"
)

// Refunding document types (documents.doc_type).
const (
	DocBorrowingEstimates = "borrowing_estimates" // "Financing Estimates" press release
	DocRefunding          = "refunding"           // "Policy Statement": the quarterly refunding statement
)

// Refunding calendar event names (releases.source = "treasury").
const (
	NameFinancingEstimates = "Refunding: Financing Estimates"
	NameRefundingStatement = "Refunding: Policy Statement"
)

// RefundingSection is one "DOCUMENTS RELEASED at …" block of the Most
// Recent Quarterly Refunding Documents page.
type RefundingSection struct {
	ReleasedAt time.Time // ET
	Next       time.Time // next release of the same documents, ET; zero if not announced
	Docs       []RefundingLink
}

// RefundingLink is a document link of a section.
type RefundingLink struct {
	Title, URL string
}

// kind tells which tracked documents the section carries.
func (s RefundingSection) kind() (docType, event string) {
	for _, d := range s.Docs {
		switch {
		case strings.HasPrefix(d.Title, "Financing Estimates"):
			return DocBorrowingEstimates, NameFinancingEstimates
		case strings.HasPrefix(d.Title, "Policy Statement"):
			return DocRefunding, NameRefundingStatement
		}
	}
	return "", ""
}

var (
	reReleased = regexp.MustCompile(`(?i)DOCUMENTS RELEASED at (\d{1,2}:\d{2} [AP]M) \w+, (\w+ \d{1,2} ?, ?\d{4})`)
	reNext     = regexp.MustCompile(`(?i)next release is scheduled for (\w+ \d{1,2} ?, ?\d{4})`)
	reSplit    = regexp.MustCompile(`(?i)DOCUMENTS RELEASED`)
)

// ParseRefundingPage splits the page body into its release sections (the
// same text also sits in the meta description, which is skipped). A block
// without a release time is page prose and is skipped; a page with no timed
// section is an error.
func ParseRefundingPage(raw []byte, base string, et *time.Location) ([]RefundingSection, error) {
	full, err := goquery.NewDocumentFromReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("refunding page: %w", err)
	}
	sel := full.Find("div.field--name-field-page-body").First()
	body, err := sel.Html()
	if err != nil || body == "" {
		return nil, fmt.Errorf("refunding page: no page body")
	}
	page := []byte(body)
	idx := reSplit.FindAllIndex(page, -1)
	if len(idx) == 0 {
		return nil, fmt.Errorf("refunding page: no \"DOCUMENTS RELEASED\" sections")
	}
	var out []RefundingSection
	for i, loc := range idx {
		end := len(page)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page[loc[0]:end]))
		if err != nil {
			return nil, fmt.Errorf("refunding page section %d: %w", i+1, err)
		}
		text := strings.Join(strings.Fields(doc.Text()), " ")
		m := reReleased.FindStringSubmatch(text)
		if m == nil {
			// Not a release block: page prose that mentions "documents
			// released" (e.g. a pointer to the archives).
			continue
		}
		sec := RefundingSection{}
		if sec.ReleasedAt, err = pageTime(m[2], m[1], et); err != nil {
			return nil, fmt.Errorf("refunding page section %d: %w", i+1, err)
		}
		if n := reNext.FindStringSubmatch(text); n != nil {
			if sec.Next, err = pageTime(n[1], m[1], et); err != nil {
				return nil, fmt.Errorf("refunding page section %d next: %w", i+1, err)
			}
		}
		doc.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
			title := strings.Join(strings.Fields(a.Text()), " ")
			href, _ := a.Attr("href")
			if title == "" {
				return
			}
			if u, err := resolve(base, href); err == nil {
				sec.Docs = append(sec.Docs, RefundingLink{Title: strings.TrimSuffix(title, "\u200b"), URL: u})
			}
		})
		out = append(out, sec)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("refunding page: no section with a release time in %.80q", strings.Join(strings.Fields(sel.Text()), " "))
	}
	return out, nil
}

// pageTime parses "august 3, 2026" / "November 4 , 2026" and "3:00 PM" in ET.
func pageTime(day, clock string, et *time.Location) (time.Time, error) {
	day = strings.Join(strings.Fields(strings.ReplaceAll(day, " ,", ",")), " ")
	if day != "" {
		day = strings.ToUpper(day[:1]) + strings.ToLower(day[1:])
	}
	t, err := time.ParseInLocation("January 2, 2006 3:04 PM", day+" "+strings.ToUpper(clock), et)
	if err != nil {
		return time.Time{}, fmt.Errorf("date %q %q: %w", day, clock, err)
	}
	return t, nil
}

func resolve(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

// RefundingEvents are the Financing Estimates and Policy Statement releases
// on the page: the current one (so it is not marked rescheduled once the
// page moves on) and the next one announced.
func RefundingEvents(secs []RefundingSection) []model.ReleaseEvent {
	var out []model.ReleaseEvent
	for _, s := range secs {
		_, name := s.kind()
		if name == "" {
			continue
		}
		for _, at := range []time.Time{s.ReleasedAt, s.Next} {
			if !at.IsZero() {
				out = append(out, model.ReleaseEvent{
					Source: Source, Name: name, ScheduledAt: at.UTC(), Importance: "high",
				})
			}
		}
	}
	return out
}

// Refunding is the text source of the refunding documents: the borrowing
// estimates and the refunding statement linked from the page. The cursor is
// the space-separated list of press-release ids already stored.
type Refunding struct {
	Client *Client
	ET     *time.Location
}

var _ sources.TextSource = (*Refunding)(nil)

// Name implements sources.TextSource; it is also the fetch_state key.
func (r *Refunding) Name() string { return Source + ":refunding" }

// FetchDocuments implements sources.TextSource.
func (r *Refunding) FetchDocuments(ctx context.Context, cursor string) ([]model.Document, string, error) {
	secs, err := r.Client.RefundingSections(ctx, r.ET)
	if err != nil {
		return nil, cursor, err
	}
	seen := strings.Fields(cursor)
	var docs []model.Document
	for _, s := range secs {
		docType, _ := s.kind()
		if docType == "" {
			continue
		}
		for _, l := range s.Docs {
			if !strings.Contains(l.URL, "/news/press-releases/") || !trackedTitle(l.Title) {
				continue
			}
			id := l.URL[strings.LastIndex(l.URL, "/")+1:]
			if slices.Contains(seen, id) {
				continue
			}
			d, err := r.Client.pressRelease(ctx, l, id, docTypeOf(l.Title), s.ReleasedAt)
			if err != nil {
				return nil, cursor, err
			}
			docs = append(docs, d)
			seen = append(seen, id)
		}
	}
	return docs, strings.Join(seen, " "), nil
}

func trackedTitle(t string) bool {
	return strings.HasPrefix(t, "Financing Estimates") || strings.HasPrefix(t, "Policy Statement")
}

func docTypeOf(title string) string {
	if strings.HasPrefix(title, "Financing Estimates") {
		return DocBorrowingEstimates
	}
	return DocRefunding
}

// RefundingSections fetches and parses the refunding page.
func (c *Client) RefundingSections(ctx context.Context, et *time.Location) ([]RefundingSection, error) {
	page, err := c.http.Get(ctx, c.refundingURL)
	if err != nil {
		return nil, fmt.Errorf("treasury refunding page: %w", err)
	}
	secs, err := ParseRefundingPage(page, c.refundingURL, et)
	if err != nil {
		return nil, fmt.Errorf("treasury %w", err)
	}
	return secs, nil
}

func (c *Client) pressRelease(ctx context.Context, l RefundingLink, id, docType string, published time.Time) (model.Document, error) {
	b, err := c.http.Get(ctx, l.URL)
	if err != nil {
		return model.Document{}, fmt.Errorf("treasury refunding %s: %w", id, err)
	}
	body, err := PressReleaseText(b)
	if err != nil {
		return model.Document{}, fmt.Errorf("treasury refunding %s: %w", id, err)
	}
	return model.Document{
		Source: Source, ExternalID: id, DocType: docType,
		PublishedAt: published.UTC(), Author: "U.S. Department of the Treasury",
		Title: l.Title, Body: body, URL: l.URL,
	}, nil
}

// PressReleaseText is the plain text of a home.treasury.gov press release.
func PressReleaseText(page []byte) (string, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return "", err
	}
	sel := doc.Find("div.field--name-field-news-body").First()
	if sel.Length() == 0 {
		return "", fmt.Errorf("press release: no news body")
	}
	text := htmltext.Text(sel)
	if text == "" {
		return "", fmt.Errorf("press release: empty body")
	}
	return text, nil
}
