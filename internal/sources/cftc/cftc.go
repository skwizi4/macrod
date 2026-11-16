// Package cftc reads the Commitments of Traders, Traders in Financial
// Futures — Futures Only, from the CFTC Socrata API (LOOKUP C1–C3,
// 2026-10-24). Positions are stored as levels (long and short per trader
// group, open interest); nets are computed by derived.Net.
package cftc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
)

// Source is the source name (observations.source, fetch_state key).
const Source = "cftc"

// Fields of a contract, stored as series "<code>:<field>".
const (
	FieldLevLong      = "lev_long"  // Leveraged Funds long
	FieldLevShort     = "lev_short" // Leveraged Funds short
	FieldAMLong       = "am_long"   // Asset Managers long
	FieldAMShort      = "am_short"  // Asset Managers short
	FieldOpenInterest = "oi"
)

// Fields lists every stored field.
var Fields = []string{FieldLevLong, FieldLevShort, FieldAMLong, FieldAMShort, FieldOpenInterest}

var columns = map[string]string{
	FieldLevLong:      "lev_money_positions_long",
	FieldLevShort:     "lev_money_positions_short",
	FieldAMLong:       "asset_mgr_positions_long",
	FieldAMShort:      "asset_mgr_positions_short",
	FieldOpenInterest: "open_interest_all",
}

// SeriesID is the series of a contract field.
func SeriesID(code, field string) string { return code + ":" + field }

// pageSize is the Socrata $limit per request.
const pageSize = 5000

// Client reads the TFF dataset.
type Client struct {
	http     *httpx.Client
	endpoint string
	pageSize int
	log      *slog.Logger
}

// New returns a client for the dataset endpoint.
func New(endpoint string, h *httpx.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: h, endpoint: endpoint, pageSize: pageSize, log: log}
}

// Query is the request for codes' reports in [since, until] (zero until:
// no upper bound), one page.
func (c *Client) Query(codes []string, since, until time.Time, offset int) string {
	quoted := make([]string, len(codes))
	for i, code := range codes {
		quoted[i] = "'" + strings.ReplaceAll(code, "'", "''") + "'"
	}
	sel := []string{"report_date_as_yyyy_mm_dd", "cftc_contract_market_code"}
	for _, f := range Fields {
		sel = append(sel, columns[f])
	}
	where := fmt.Sprintf("cftc_contract_market_code in (%s) AND report_date_as_yyyy_mm_dd >= '%s'",
		strings.Join(quoted, ","), since.Format(time.DateOnly))
	if !until.IsZero() {
		where += fmt.Sprintf(" AND report_date_as_yyyy_mm_dd <= '%s'", until.Format(time.DateOnly))
	}
	q := url.Values{
		"$select": {strings.Join(sel, ",")},
		"$where":  {where},
		"$order":  {"report_date_as_yyyy_mm_dd,cftc_contract_market_code"},
		"$limit":  {strconv.Itoa(c.pageSize)},
		"$offset": {strconv.Itoa(offset)},
	}
	return c.endpoint + "?" + q.Encode()
}

// Fetch returns the observations of every field of codes from since on,
// keyed by SeriesID, ascending by report date (Tuesday).
func (c *Client) Fetch(ctx context.Context, codes []string, since time.Time) (map[string][]model.Observation, error) {
	out := make(map[string][]model.Observation)
	for offset := 0; ; {
		body, err := c.http.Get(ctx, c.Query(codes, since, time.Time{}, offset))
		if err != nil {
			return nil, fmt.Errorf("cftc fetch: %w", err)
		}
		n, err := parse(body, out)
		if err != nil {
			return nil, fmt.Errorf("cftc fetch: %w", err)
		}
		offset += n
		if n < c.pageSize {
			break
		}
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool { return out[id][i].Date.Before(out[id][j].Date) })
	}
	return out, nil
}

// parse adds the rows of one page to out and returns how many there were.
func parse(body []byte, out map[string][]model.Observation) (int, error) {
	var rows []map[string]string
	if err := json.Unmarshal(body, &rows); err != nil {
		return 0, fmt.Errorf("decode: %w", err)
	}
	for _, r := range rows {
		ds := r["report_date_as_yyyy_mm_dd"]
		if len(ds) < len(time.DateOnly) {
			return 0, fmt.Errorf("report date %q", ds)
		}
		d, err := time.Parse(time.DateOnly, ds[:len(time.DateOnly)])
		if err != nil {
			return 0, fmt.Errorf("report date %q: %w", ds, err)
		}
		code := r["cftc_contract_market_code"]
		for _, f := range Fields {
			s, ok := r[columns[f]]
			if !ok || s == "" {
				continue // Socrata omits empty fields
			}
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return 0, fmt.Errorf("%s %s %s %q: %w", code, ds, f, s, err)
			}
			id := SeriesID(code, f)
			out[id] = append(out[id], model.Observation{Date: d, Value: v})
		}
	}
	return len(rows), nil
}

// FixtureFile is the recorded response in testdata/cftc.
const FixtureFile = "tff_2025_2026.json"

// FixtureCodes and FixtureSince pin the recording: a year of the US
// contracts, enough for a 52-week z-score.
var (
	FixtureCodes = []string{"209742", "043602"}
	FixtureSince = time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	FixtureUntil = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
)

// RecordFixtures writes the fixture into dir.
func (c *Client) RecordFixtures(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cftc record mkdir %s: %w", dir, err)
	}
	body, err := c.http.Get(ctx, c.Query(FixtureCodes, FixtureSince, FixtureUntil, 0))
	if err != nil {
		return fmt.Errorf("cftc record: %w", err)
	}
	path := filepath.Join(dir, FixtureFile)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("cftc record: %w", err)
	}
	c.log.Info("fixture recorded", "file", path, "bytes", len(body))
	return nil
}
