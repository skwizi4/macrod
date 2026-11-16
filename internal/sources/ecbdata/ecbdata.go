// Package ecbdata reads series from the ECB Data Portal (SDMX REST, spec
// 6.1; LOOKUP 2026-11-09): one CSV per series key, no API key.
package ecbdata

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/httpx"
)

// Source is the source name (observations.source, fetch_state prefix).
const Source = "ecb"

// Client reads the data service.
type Client struct {
	http     *httpx.Client
	endpoint string // …/service/data
	log      *slog.Logger
}

// New returns a client of the data service at endpoint.
func New(endpoint string, h *httpx.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: h, endpoint: strings.TrimRight(endpoint, "/"), log: log}
}

// Name implements sources.NumericSource.
func (c *Client) Name() string { return Source }

// URL is the request for the series key ("FM/D.U2.EUR.4F.KR.DFR.LEV": flow
// and key) from since on, to the end date when it is not zero. A calendar
// date is accepted for every frequency.
func (c *Client) URL(key string, since, to time.Time) string {
	q := url.Values{"format": {"csvdata"}, "detail": {"dataonly"}}
	if !since.IsZero() {
		q.Set("startPeriod", since.Format(time.DateOnly))
	}
	if !to.IsZero() {
		q.Set("endPeriod", to.Format(time.DateOnly))
	}
	return c.endpoint + "/" + key + "?" + q.Encode()
}

// NextYear in a key marks the dimension of a forecast's target year: the
// series is stitched from one request with that dimension open, taking for
// each survey period the forecast for the next calendar year (SPF: the
// survey of 2026-Q4 for 2027). Batch 11f, user decision 2026-11-12.
const NextYear = "{next_year}"

// FetchSeries implements sources.NumericSource: the observations of key
// from since on. A range without observations is not an error.
func (c *Client) FetchSeries(ctx context.Context, key string, since time.Time) ([]model.Observation, error) {
	pos := -1
	if strings.Contains(key, NextYear) {
		flow, dims, _ := strings.Cut(key, "/")
		parts := strings.Split(dims, ".")
		pos = slices.Index(parts, NextYear)
		if pos < 0 {
			return nil, fmt.Errorf("ecb fetch %s: %s must be a whole key dimension", key, NextYear)
		}
		parts[pos] = ""
		key = flow + "/" + strings.Join(parts, ".")
	}
	body, err := c.http.Get(ctx, c.URL(key, since, time.Time{}))
	if err != nil {
		if errors.Is(err, httpx.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("ecb fetch %s: %w", key, err)
	}
	var obs []model.Observation
	if pos >= 0 {
		obs, err = ParseNextYear(body, pos)
	} else {
		obs, err = Parse(body)
	}
	if err != nil {
		return nil, fmt.Errorf("ecb fetch %s: %w", key, err)
	}
	return obs, nil
}

// ParseNextYear reads a csvdata response of several target years (the
// dimension at pos of the series key, after the flow, holds the year) and
// keeps, for each period, the value whose target is the next calendar year.
func ParseNextYear(body []byte, pos int) ([]model.Observation, error) {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("decode csv: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	key, okK := col["KEY"]
	period, okP := col["TIME_PERIOD"]
	value, okV := col["OBS_VALUE"]
	if !okK || !okP || !okV {
		return nil, errors.New("csv has no KEY, TIME_PERIOD and OBS_VALUE columns")
	}
	var out []model.Observation
	for n, row := range rows[1:] {
		if len(row) <= max(key, period, value) {
			return nil, fmt.Errorf("csv row %d: %d fields", n+2, len(row))
		}
		parts := strings.Split(row[key], ".")
		if len(parts) <= pos+1 {
			return nil, fmt.Errorf("csv row %d: key %q has no dimension %d", n+2, row[key], pos+1)
		}
		target, err := strconv.Atoi(parts[pos+1])
		if err != nil {
			continue // a horizon that is not a year (LT, P24M)
		}
		d, err := sources.ParsePeriod(row[period])
		if err != nil {
			return nil, fmt.Errorf("csv row %d: %w", n+2, err)
		}
		raw := strings.TrimSpace(row[value])
		if target != d.Year()+1 || raw == "" || raw == "NaN" {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("csv row %d: value %q", n+2, raw)
		}
		out = append(out, model.Observation{Date: d, Value: v})
	}
	slices.SortFunc(out, func(a, b model.Observation) int { return a.Date.Compare(b.Date) })
	for i := 1; i < len(out); i++ {
		if out[i].Date.Equal(out[i-1].Date) {
			return nil, fmt.Errorf("two values for %s", out[i].Date.Format(time.DateOnly))
		}
	}
	return out, nil
}

// Parse reads a csvdata response: the columns TIME_PERIOD and OBS_VALUE are
// found by name, rows without a value (NaN, empty) are skipped. Periods
// become dates by sources.ParsePeriod.
func Parse(body []byte) ([]model.Observation, error) {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("decode csv: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	period, value := -1, -1
	for i, h := range rows[0] {
		switch h {
		case "TIME_PERIOD":
			period = i
		case "OBS_VALUE":
			value = i
		}
	}
	if period < 0 || value < 0 {
		return nil, errors.New("csv has no TIME_PERIOD and OBS_VALUE columns")
	}
	var out []model.Observation
	for n, row := range rows[1:] {
		if len(row) <= period || len(row) <= value {
			return nil, fmt.Errorf("csv row %d: %d fields", n+2, len(row))
		}
		raw := strings.TrimSpace(row[value])
		if raw == "" || raw == "NaN" {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("csv row %d: value %q", n+2, raw)
		}
		d, err := sources.ParsePeriod(row[period])
		if err != nil {
			return nil, fmt.Errorf("csv row %d: %w", n+2, err)
		}
		if len(out) > 0 && !d.After(out[len(out)-1].Date) {
			return nil, fmt.Errorf("csv row %d: period %s is not after the previous one", n+2, row[period])
		}
		out = append(out, model.Observation{Date: d, Value: v})
	}
	return out, nil
}
