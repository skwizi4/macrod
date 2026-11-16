package yahoo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Yahoo serves one-minute bars of the last 30 days, at most 8 days per
// request (LOOKUP FIXES_W44 F1, 2026-11-02).
const (
	MinuteHistory = 30 * 24 * time.Hour
	minuteSpan    = 7 * 24 * time.Hour
)

// MinuteBar is one one-minute bar.
type MinuteBar struct {
	TS    time.Time // bar start, UTC
	Close float64
}

// IntradayURL is the request for symbol's one-minute bars in [from, to).
func (c *Client) IntradayURL(symbol string, from, to time.Time) string {
	q := url.Values{
		"period1":  {strconv.FormatInt(from.Unix(), 10)},
		"period2":  {strconv.FormatInt(to.Unix(), 10)},
		"interval": {"1m"},
	}
	return strings.ReplaceAll(c.endpoint, "{symbol}", url.PathEscape(symbol)) + "?" + q.Encode()
}

// Intraday returns the one-minute bars of symbol in [from, to), in
// requests of at most seven days.
func (c *Client) Intraday(ctx context.Context, symbol string, from, to time.Time) ([]MinuteBar, error) {
	var out []MinuteBar
	for start := from; start.Before(to); start = start.Add(minuteSpan) {
		end := start.Add(minuteSpan)
		if end.After(to) {
			end = to
		}
		body, err := c.http.Get(ctx, c.IntradayURL(symbol, start, end))
		if err != nil {
			return nil, fmt.Errorf("yahoo %s 1m: %w", symbol, err)
		}
		bars, err := ParseIntraday(body)
		if err != nil {
			return nil, fmt.Errorf("yahoo %s 1m: %w", symbol, err)
		}
		out = append(out, bars...)
	}
	return out, nil
}

// ParseIntraday decodes a one-minute chart response; null and zero closes
// are skipped.
func ParseIntraday(body []byte) ([]MinuteBar, error) {
	var r chartResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("decode chart: %w", err)
	}
	if e := r.Chart.Error; e != nil {
		return nil, fmt.Errorf("%s: %s", e.Code, e.Description)
	}
	if len(r.Chart.Result) == 0 {
		return nil, ErrNoData
	}
	res := r.Chart.Result[0]
	if len(res.Indicators.Quote) == 0 {
		return nil, nil
	}
	q := res.Indicators.Quote[0]
	if len(q.Close) != len(res.Timestamp) {
		return nil, fmt.Errorf("chart: %d timestamps, %d closes", len(res.Timestamp), len(q.Close))
	}
	var out []MinuteBar
	for i, ts := range res.Timestamp {
		if q.Close[i] == nil || math.IsNaN(*q.Close[i]) || *q.Close[i] == 0 {
			continue
		}
		out = append(out, MinuteBar{TS: time.Unix(ts, 0).UTC(), Close: float32Value(*q.Close[i])})
	}
	return out, nil
}
