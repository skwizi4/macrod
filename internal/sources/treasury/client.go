// Package treasury reads the US Treasury: Fiscal Data (DTS, MTS, buybacks,
// release calendar), TreasuryDirect TA_WS (auction results and upcoming
// auctions) and the Quarterly Refunding page on home.treasury.gov
// (LOOKUP T1–T9, 2026-10-24).
package treasury

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/sources/httpx"
)

// Source names (fetch_state keys, documents.source, series_meta.source).
const (
	Source        = "treasury"
	SourceAuction = "treasury_auction" // releases.source of auction events
)

// Fiscal Data pages are capped at 10000 rows.
const defaultPageSize = 10000

// Client reads the Treasury endpoints configured in config.Treasury.
type Client struct {
	http         *httpx.Client
	fiscalURL    string
	calendarURL  string
	directURL    string
	refundingURL string
	pageSize     int
	log          *slog.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithPageSize sets the Fiscal Data page size (tests).
func WithPageSize(n int) Option { return func(c *Client) { c.pageSize = n } }

// WithLogger sets the logger; it should already carry source=treasury.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// New returns a client for the URLs in cfg.
func New(cfg config.Treasury, h *httpx.Client, opts ...Option) *Client {
	c := &Client{
		http:         h,
		fiscalURL:    strings.TrimRight(cfg.FiscalDataURL, "/"),
		calendarURL:  cfg.ReleaseCalendarURL,
		directURL:    strings.TrimRight(cfg.TreasuryDirectURL, "/"),
		refundingURL: cfg.QuarterlyRefundingURL,
		pageSize:     defaultPageSize,
		log:          slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// row is one Fiscal Data record. All values arrive as strings; missing
// ones as "null".
type row map[string]string

type fiscalPage struct {
	Data []map[string]any `json:"data"`
	Meta struct {
		TotalPages int `json:"total-pages"`
	} `json:"meta"`
}

// fiscal returns every row of a Fiscal Data endpoint, following pages.
// params holds fields/filter/sort; paging is added here.
func (c *Client) fiscal(ctx context.Context, endpoint string, params url.Values) ([]row, error) {
	var out []row
	for page := 1; ; page++ {
		q := url.Values{}
		for k, v := range params {
			q[k] = v
		}
		q.Set("page[number]", strconv.Itoa(page))
		q.Set("page[size]", strconv.Itoa(c.pageSize))
		body, err := c.http.Get(ctx, c.fiscalURL+"/"+endpoint+"?"+q.Encode())
		if err != nil {
			return nil, err
		}
		var p fiscalPage
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("decode %s page %d: %w", endpoint, page, err)
		}
		for _, d := range p.Data {
			r := make(row, len(d))
			for k, v := range d {
				r[k] = str(v)
			}
			out = append(out, r)
		}
		if page >= p.Meta.TotalPages || len(p.Data) == 0 {
			return out, nil
		}
	}
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

// directGet decodes a TA_WS JSON array.
func (c *Client) directGet(ctx context.Context, path string, params url.Values) ([]map[string]string, error) {
	q := url.Values{"format": {"json"}}
	for k, v := range params {
		q[k] = v
	}
	body, err := c.http.Get(ctx, c.directURL+"/"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var recs []map[string]string
	if err := json.Unmarshal(body, &recs); err != nil {
		return nil, fmt.Errorf("decode treasurydirect %s: %w", path, err)
	}
	return recs, nil
}

// number parses a Fiscal Data / TA_WS number; "", "null" and "N/A" are absent.
func number(s string) (float64, bool, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "", "null", "N/A":
		return 0, false, nil
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	if err != nil {
		return 0, false, fmt.Errorf("number %q: %w", s, err)
	}
	return v, true, nil
}

// date parses "2026-10-22" or "2026-10-22T00:00:00"; "" and "null" give zero.
func date(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return time.Time{}, nil
	}
	if len(s) > len(time.DateOnly) {
		s = s[:len(time.DateOnly)]
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("date %q: %w", s, err)
	}
	return t, nil
}

// clock converts "01:00 PM" to "13:00"; "" stays "".
func clock(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return "", nil
	}
	t, err := time.Parse("03:04 PM", s)
	if err != nil {
		return "", fmt.Errorf("time %q: %w", s, err)
	}
	return t.Format("15:04"), nil
}
