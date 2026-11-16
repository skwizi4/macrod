// Package irtranscript reads official earnings call transcripts from
// investor relations sites (phase 5b, LOOKUP R1–R3, 2026-10-30): Microsoft
// posts the transcript on the event page of the fiscal quarter, Alphabet on
// an event page listed by its Q4 IR feed. Both are HTML. Only the Outlook
// block is used, as text: no numbers are taken from it.
package irtranscript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"macro-collector/internal/sources/htmltext"
	"macro-collector/internal/sources/httpx"
)

// Source is the source name (documents.source, fetch_state prefix).
const Source = "transcript"

// Client reads transcripts. The httpx client spaces requests by the sites'
// Crawl-delay.
type Client struct {
	http *httpx.Client
	log  *slog.Logger
}

// New returns a client.
func New(h *httpx.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: h, log: log}
}

// Transcript is the text of a call: paragraphs separated by blank lines.
type Transcript struct {
	URL  string
	Text string
}

// Fetch returns the transcript of fiscal quarter q of fiscal year fy from
// an event page template (page, with {fy} and {q}) or a Q4 IR feed (feed,
// with {fy}). ok is false while it is not published: no page or feed entry
// yet, or a page without the Outlook of the call.
func (c *Client) Fetch(ctx context.Context, page, feed string, fy, q int) (t Transcript, ok bool, err error) {
	url := PageURL(page, fy, q)
	if feed != "" {
		body, found, err := c.get(ctx, PageURL(feed, fy, q))
		if err != nil || !found {
			return Transcript{}, false, err
		}
		if url, found, err = EventPage(body, fy, q); err != nil || !found {
			return Transcript{}, false, err
		}
	}
	body, found, err := c.get(ctx, url)
	if err != nil || !found {
		return Transcript{}, false, err
	}
	text, err := Text(body)
	if err != nil {
		return Transcript{}, false, fmt.Errorf("transcript %s: %w", url, err)
	}
	if len(Outlook(text)) == 0 {
		c.log.Info("transcript not published yet", "url", url)
		return Transcript{}, false, nil
	}
	return Transcript{URL: url, Text: text}, true, nil
}

// get fetches url; a 404 means "not published yet", not an error.
func (c *Client) get(ctx context.Context, url string) ([]byte, bool, error) {
	body, err := c.http.Get(ctx, url)
	if errors.Is(err, httpx.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("transcript %s: %w", url, err)
	}
	return body, true, nil
}

// PageURL fills {fy} (four digits) and {q} of a template.
func PageURL(tmpl string, fy, q int) string {
	return strings.NewReplacer("{fy}", strconv.Itoa(fy), "{q}", strconv.Itoa(q)).Replace(tmpl)
}

var ordinals = []string{"First", "Second", "Third", "Fourth"}

// EventPage finds, in a Q4 IR feed (GetFinancialReportList), the report
// "<Ordinal> Quarter <fy>" and its document whose title has "Transcript"
// (Alphabet: "Webcast & Transcript").
func EventPage(feed []byte, fy, q int) (string, bool, error) {
	if q < 1 || q > 4 {
		return "", false, fmt.Errorf("transcript feed: quarter %d", q)
	}
	var raw struct {
		Reports []struct {
			Title     string `json:"ReportTitle"`
			Documents []struct {
				Title string `json:"DocumentTitle"`
				Path  string `json:"DocumentPath"`
			} `json:"Documents"`
		} `json:"GetFinancialReportListResult"`
	}
	if err := json.Unmarshal(feed, &raw); err != nil {
		return "", false, fmt.Errorf("transcript feed: decode: %w", err)
	}
	want := fmt.Sprintf("%s Quarter %d", ordinals[q-1], fy)
	for _, r := range raw.Reports {
		if !strings.EqualFold(strings.TrimSpace(r.Title), want) {
			continue
		}
		for _, d := range r.Documents {
			if strings.Contains(strings.ToLower(d.Title), "transcript") && (strings.HasPrefix(d.Path, "https://") || strings.HasPrefix(d.Path, "http://")) {
				return d.Path, true, nil
			}
		}
	}
	return "", false, nil
}

// Text returns the paragraphs of a page, without navigation, header and
// footer.
func Text(page []byte) (string, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return "", err
	}
	body := doc.Find("body")
	body.Find("nav, header, footer").Remove()
	return htmltext.Text(body), nil
}

var (
	// outlookRe marks the paragraph that opens the Outlook ("Now, moving to
	// the outlook for our second quarter", "I'll now turn to our outlook").
	outlookRe = regexp.MustCompile(`(?i)\boutlook\b`)
	// qaRe marks the start of the questions.
	qaRe = regexp.MustCompile(`(?i)(move (over )?to (the )?Q&A|first question|open (up )?the (call|line) (for|to) questions)`)
	// guidanceRe: words of a forecast (decision 2026-10-30).
	guidanceRe = regexp.MustCompile(`(?i)\b(outlook|guidance|expects?|expected|expecting|anticipates?|anticipated|forecasts?)\b`)
)

// QAStart returns the byte offset in text where the questions begin (the
// first paragraph moving to Q&A), len(text) for prepared remarks alone.
func QAStart(text string) int {
	off := 0
	for _, p := range strings.Split(text, "\n\n") {
		if qaRe.MatchString(p) {
			return off
		}
		off += len(p) + 2
	}
	return len(text)
}

// Outlook returns the forecast paragraphs of a call: from the last
// paragraph mentioning the outlook before the questions to the questions
// (to the end for prepared remarks without Q&A), those with forecast words.
// Empty when the text has no Outlook (the page before the call).
func Outlook(text string) []string {
	paras := strings.Split(text, "\n\n")
	end := len(paras)
	for i, p := range paras {
		if qaRe.MatchString(p) {
			end = i
			break
		}
	}
	start := -1
	for i := end - 1; i >= 0; i-- {
		if outlookRe.MatchString(paras[i]) {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var out []string
	for _, p := range paras[start:end] {
		if p = strings.TrimSpace(p); guidanceRe.MatchString(p) {
			out = append(out, p)
		}
	}
	return out
}
