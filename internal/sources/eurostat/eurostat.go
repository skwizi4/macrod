// Package eurostat reads series from the Eurostat dissemination API
// (JSON-stat 2.0, spec 6.1; LOOKUP 2026-11-09), no API key. A series is a
// dataset with a filter for every dimension but time:
// "namq_10_gdp?geo=EA20&unit=CLV20_MEUR&s_adj=SCA&na_item=B1GQ".
package eurostat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/httpx"
)

// Source is the source name (observations.source, fetch_state prefix).
const Source = "eurostat"

// Client reads the statistics API.
type Client struct {
	http     *httpx.Client
	endpoint string // …/statistics/1.0/data
	log      *slog.Logger
}

// New returns a client of the API at endpoint.
func New(endpoint string, h *httpx.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: h, endpoint: strings.TrimRight(endpoint, "/"), log: log}
}

// Name implements sources.NumericSource.
func (c *Client) Name() string { return Source }

// URL is the request for key (dataset?filters) from the year of since on,
// up to the year of to when it is not zero: a year is a valid period for
// every frequency.
func (c *Client) URL(key string, since, to time.Time) (string, error) {
	dataset, filters, _ := strings.Cut(key, "?")
	q, err := url.ParseQuery(filters)
	if err != nil || dataset == "" || len(q) == 0 {
		return "", fmt.Errorf("key %q: want dataset?dimension=code&…", key)
	}
	q.Set("format", "JSON")
	q.Set("lang", "EN")
	if !since.IsZero() {
		q.Set("sinceTimePeriod", strconv.Itoa(since.Year()))
	}
	if !to.IsZero() {
		q.Set("untilTimePeriod", strconv.Itoa(to.Year()))
	}
	return c.endpoint + "/" + dataset + "?" + q.Encode(), nil
}

// FetchSeries implements sources.NumericSource: the observations of key
// dated since or later.
func (c *Client) FetchSeries(ctx context.Context, key string, since time.Time) ([]model.Observation, error) {
	u, err := c.URL(key, since, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("eurostat fetch: %w", err)
	}
	body, err := c.http.Get(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("eurostat fetch %s: %w", key, err)
	}
	obs, err := Parse(body)
	if err != nil {
		return nil, fmt.Errorf("eurostat fetch %s: %w", key, err)
	}
	// The request is cut by year; the answer is cut by date.
	for len(obs) > 0 && obs[0].Date.Before(since) {
		obs = obs[1:]
	}
	return obs, nil
}

type dataset struct {
	Class     string          `json:"class"`
	ID        []string        `json:"id"`
	Size      []int           `json:"size"`
	Value     json.RawMessage `json:"value"`
	Dimension map[string]struct {
		Category struct {
			Index map[string]int `json:"index"`
		} `json:"category"`
	} `json:"dimension"`
	Error []struct {
		Status int    `json:"status"`
		Label  string `json:"label"`
	} `json:"error"`
}

// Parse reads a JSON-stat dataset holding one series: every dimension but
// "time" must have exactly one category, otherwise the filters of the key
// are incomplete (or select nothing) and the values would be mixed up.
// Periods without a value are skipped.
func Parse(body []byte) ([]model.Observation, error) {
	var d dataset
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("decode json-stat: %w", err)
	}
	if len(d.Error) > 0 {
		return nil, fmt.Errorf("eurostat error %d: %s", d.Error[0].Status, d.Error[0].Label)
	}
	if d.Class != "dataset" || len(d.ID) == 0 || len(d.ID) != len(d.Size) {
		return nil, errors.New("not a json-stat dataset")
	}
	if d.ID[len(d.ID)-1] != "time" {
		return nil, fmt.Errorf("last dimension is %q, want time", d.ID[len(d.ID)-1])
	}
	for i, name := range d.ID[:len(d.ID)-1] {
		if d.Size[i] != 1 {
			return nil, fmt.Errorf("dimension %s has %d categories: the key must select exactly one", name, d.Size[i])
		}
	}
	values, err := decodeValues(d.Value)
	if err != nil {
		return nil, err
	}
	var out []model.Observation
	for period, pos := range d.Dimension["time"].Category.Index {
		v, ok := values[pos]
		if !ok {
			continue
		}
		date, err := sources.ParsePeriod(period)
		if err != nil {
			return nil, err
		}
		out = append(out, model.Observation{Date: date, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

// decodeValues reads "value": an object of position → number (the sparse
// form Eurostat sends) or an array with nulls.
func decodeValues(raw json.RawMessage) (map[int]float64, error) {
	out := map[int]float64{}
	var sparse map[string]*float64
	if err := json.Unmarshal(raw, &sparse); err == nil {
		for k, v := range sparse {
			pos, err := strconv.Atoi(k)
			if err != nil {
				return nil, fmt.Errorf("value index %q", k)
			}
			if v != nil && !math.IsNaN(*v) {
				out[pos] = *v
			}
		}
		return out, nil
	}
	var dense []*float64
	if err := json.Unmarshal(raw, &dense); err != nil {
		return nil, fmt.Errorf("decode value: %w", err)
	}
	for i, v := range dense {
		if v != nil && !math.IsNaN(*v) {
			out[i] = *v
		}
	}
	return out, nil
}
