// Package bundesbank reads time series from the Bundesbank statistics API
// (SDMX REST, spec 6.1; LOOKUP 2026-11-09): one CSV per series key, no API
// key. The CSV starts with a BOM, is separated by semicolons, writes
// decimals with a comma and marks a day without a value (a weekend, a
// holiday) with ".".
package bundesbank

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/httpx"
)

// Source is the source name (observations.source, fetch_state prefix).
const Source = "bundesbank"

// Client reads the data service.
type Client struct {
	http     *httpx.Client
	endpoint string // …/rest/data
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

// URL is the request for the series key ("BBSIS/D.I.ZAR…": flow and key)
// from since on, to the end date when it is not zero. The period is written
// in the frequency of the key, its first letter.
func (c *Client) URL(key string, since, to time.Time) string {
	q := url.Values{"format": {"csv"}}
	_, series, _ := strings.Cut(key, "/")
	if !since.IsZero() {
		q.Set("startPeriod", period(series, since))
	}
	if !to.IsZero() {
		q.Set("endPeriod", period(series, to))
	}
	return c.endpoint + "/" + key + "?" + q.Encode()
}

// period writes t in the frequency of a series key: D, M, Q, otherwise the
// year.
func period(series string, t time.Time) string {
	switch {
	case strings.HasPrefix(series, "D."):
		return t.Format(time.DateOnly)
	case strings.HasPrefix(series, "M."):
		return t.Format("2006-01")
	case strings.HasPrefix(series, "Q."):
		return fmt.Sprintf("%d-Q%d", t.Year(), (int(t.Month())+2)/3)
	}
	return t.Format("2006")
}

// FetchSeries implements sources.NumericSource: the observations of key
// from since on. A range without observations is not an error.
func (c *Client) FetchSeries(ctx context.Context, key string, since time.Time) ([]model.Observation, error) {
	body, err := c.http.Get(ctx, c.URL(key, since, time.Time{}))
	if err != nil {
		if errors.Is(err, httpx.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("bundesbank fetch %s: %w", key, err)
	}
	obs, err := Parse(body)
	if err != nil {
		return nil, fmt.Errorf("bundesbank fetch %s: %w", key, err)
	}
	return obs, nil
}

// Parse reads the CSV of one series. The first line names the series; the
// lines after it are metadata until the first one that starts with a
// period. A value "." is a day without a value and is skipped. The German
// file (the default) has semicolons and decimal commas, the English one
// (lang=en) commas and decimal points; the first line tells them apart.
func Parse(body []byte) ([]model.Observation, error) {
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, errors.New("empty csv")
	}
	sep, comma := ";", true
	if !strings.Contains(lines[0], ";") {
		sep, comma = ",", false
	}
	if head := strings.Split(lines[0], sep); len(head) < 2 || strings.Trim(head[0], `"`) != "" {
		return nil, fmt.Errorf("csv header %q: want an empty first cell and the series key", truncate(lines[0]))
	}
	var out []model.Observation
	for n, line := range lines[1:] {
		f := strings.Split(line, sep)
		if len(f) < 2 {
			continue
		}
		d, err := sources.ParsePeriod(f[0])
		if err != nil {
			if len(out) > 0 {
				return nil, fmt.Errorf("csv line %d: %w", n+2, err)
			}
			continue // metadata before the observations
		}
		raw := strings.TrimSpace(f[1])
		if raw == "." || raw == "" {
			continue
		}
		if comma {
			raw = strings.Replace(raw, ",", ".", 1)
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("csv line %d: value %q", n+2, f[1])
		}
		if len(out) > 0 && !d.After(out[len(out)-1].Date) {
			return nil, fmt.Errorf("csv line %d: period %s is not after the previous one", n+2, f[0])
		}
		out = append(out, model.Observation{Date: d, Value: v})
	}
	return out, nil
}

func truncate(s string) string {
	if len(s) > 80 {
		return s[:80]
	}
	return s
}
