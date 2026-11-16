// Package cboe reads the daily put/call ratios behind the Cboe "Daily
// Market Statistics" page (FIXES_W44 F3, LOOKUP 2026-11-02): one JSON per
// trading day, block "ratios". The JSON is the page's own data file, not a
// documented API, so the source is non-critical like Yahoo (D7).
package cboe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/sources/httpx"
)

// Source is the source name (observations.source, fetch_state key).
const Source = "cboe"

// Stored series.
const (
	SeriesTotal  = "PC_TOTAL"
	SeriesEquity = "PC_EQUITY"
	SeriesIndex  = "PC_INDEX"
)

// Series lists the stored ratios with their names in the JSON and labels.
var Series = []struct{ ID, Name, Label string }{
	{SeriesTotal, "TOTAL PUT/CALL RATIO", "Put/call total (Cboe)"},
	{SeriesEquity, "EQUITY PUT/CALL RATIO", "Put/call акции (Cboe)"},
	{SeriesIndex, "INDEX PUT/CALL RATIO", "Put/call индексы (Cboe)"},
}

// Client reads the daily file.
type Client struct {
	http     *httpx.Client
	endpoint string // with {date} (YYYY-MM-DD)
	log      *slog.Logger
}

// New returns a client; endpoint contains {date}.
func New(endpoint string, h *httpx.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: h, endpoint: endpoint, log: log}
}

// URL is the file of day.
func (c *Client) URL(day time.Time) string {
	return strings.ReplaceAll(c.endpoint, "{date}", day.Format(time.DateOnly))
}

// Day returns the ratios of day by series ID; ok is false when Cboe has no
// file for it (a weekend, a holiday or not yet published: status 403).
func (c *Client) Day(ctx context.Context, day time.Time) (ratios map[string]float64, ok bool, err error) {
	body, err := c.http.Get(ctx, c.URL(day))
	if err != nil {
		var he *httpx.HTTPError
		if errors.Is(err, httpx.ErrNotFound) || (errors.As(err, &he) && he.Status == 403) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("cboe %s: %w", day.Format(time.DateOnly), err)
	}
	r, err := Parse(body)
	if err != nil {
		return nil, false, fmt.Errorf("cboe %s: %w", day.Format(time.DateOnly), err)
	}
	return r, true, nil
}

// Parse reads the ratios of a daily file; a missing ratio is left out.
func Parse(body []byte) (map[string]float64, error) {
	var f struct {
		Ratios []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"ratios"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("decode daily options: %w", err)
	}
	out := map[string]float64{}
	for _, r := range f.Ratios {
		for _, s := range Series {
			if r.Name != s.Name {
				continue
			}
			v, err := strconv.ParseFloat(strings.TrimSpace(r.Value), 64)
			if err != nil {
				return nil, fmt.Errorf("ratio %q value %q: %w", r.Name, r.Value, err)
			}
			out[s.ID] = v
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no put/call ratios in the file")
	}
	return out, nil
}
