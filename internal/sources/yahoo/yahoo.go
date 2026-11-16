// Package yahoo reads daily bars from the Yahoo chart API (unofficial, no
// documentation; LOOKUP Y1–Y3, 2026-10-24). Yahoo is non-critical (D7):
// when it fails the export falls back to FRED series where configured.
package yahoo

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
	"macro-collector/internal/sources/httpx"
)

// Source is the source name (observations.source, fetch_state prefix).
const Source = "yahoo"

// VolumeSuffix marks the volume series kept for ZQ contracts: a contract
// without trades has a stale price (LOOKUP Y3).
const VolumeSuffix = "#volume"

// Spike filter: a close that jumps more than spikeJump from the previous
// close and returns within spikeBack of it on the next bar is a bad tick.
// Real one-day moves reverse too (VIX 20.6 → 25.3 → 20.8 on 2025-10-16),
// so the jump threshold is high.
const (
	spikeJump = 0.50
	spikeBack = 0.05
)

// Bar is one daily bar.
type Bar struct {
	Date   time.Time // exchange-local calendar day, as UTC midnight
	Close  float64
	Volume float64
	HasVol bool
}

// Series is the result of one chart request.
type Series struct {
	Symbol   string
	Currency string
	Type     string // instrumentType: INDEX, FUTURE, CRYPTOCURRENCY…
	Bars     []Bar  // ascending, closed sessions only, anomalies removed
	Dropped  []Anomaly
}

// Anomaly is a bar left out.
type Anomaly struct {
	Date   time.Time
	Close  float64
	Reason string
}

// Observations returns the closes as observations.
func (s Series) Observations() []model.Observation {
	out := make([]model.Observation, len(s.Bars))
	for i, b := range s.Bars {
		out[i] = model.Observation{Date: b.Date, Value: b.Close}
	}
	return out
}

// Volumes returns the volumes (bars without a volume are skipped).
func (s Series) Volumes() []model.Observation {
	var out []model.Observation
	for _, b := range s.Bars {
		if b.HasVol {
			out = append(out, model.Observation{Date: b.Date, Value: b.Volume})
		}
	}
	return out
}

// Client reads the chart endpoint.
type Client struct {
	http     *httpx.Client
	endpoint string // with {symbol}
	log      *slog.Logger
	hourly   map[string]bool
}

// HourlyHistory is how far back Yahoo serves hourly bars: "the requested
// range must be within the last 730 days".
const HourlyHistory = 729 * 24 * time.Hour

// SetHourly names the symbols whose history is read by hourly bars, the
// last value of a day standing for the day: the Euronext MTS yields
// (MIT10.AS and the like) have one bar in all at the daily interval and one
// valued bar per day at the hourly one (LOOKUP 2026-11-09). Call it before
// the client is used.
func (c *Client) SetHourly(symbols []string) {
	c.hourly = make(map[string]bool, len(symbols))
	for _, s := range symbols {
		c.hourly[s] = true
	}
}

// New returns a client; endpoint contains {symbol}.
func New(endpoint string, h *httpx.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: h, endpoint: endpoint, log: log}
}

// ChartURL is the request for symbol's bars in [from, to): daily, or hourly
// for the symbols of SetHourly.
func (c *Client) ChartURL(symbol string, from, to time.Time) string {
	return c.chartURL(symbol, from, to, c.hourly[symbol])
}

func (c *Client) chartURL(symbol string, from, to time.Time, hourly bool) string {
	interval := "1d"
	if hourly {
		interval = "1h"
	}
	q := url.Values{
		"period1":  {strconv.FormatInt(from.Unix(), 10)},
		"period2":  {strconv.FormatInt(to.Unix(), 10)},
		"interval": {interval},
		"events":   {"history"},
	}
	return strings.ReplaceAll(c.endpoint, "{symbol}", url.PathEscape(symbol)) + "?" + q.Encode()
}

// ErrNoData is returned when Yahoo knows the symbol but returns no bars.
var ErrNoData = errors.New("no data")

// Fetch returns the daily bars of symbol from since to now. An hourly
// symbol is asked for no more than HourlyHistory back.
func (c *Client) Fetch(ctx context.Context, symbol string, since, now time.Time) (Series, error) {
	if first := now.Add(-HourlyHistory); c.hourly[symbol] && since.Before(first) {
		since = first
	}
	body, err := c.http.Get(ctx, c.ChartURL(symbol, since, now.Add(24*time.Hour)))
	if err != nil {
		var he *httpx.HTTPError
		if errors.As(err, &he) || errors.Is(err, httpx.ErrNotFound) {
			if msg := chartError(he, err); msg != "" {
				return Series{}, fmt.Errorf("yahoo %s: %s", symbol, msg)
			}
		}
		return Series{}, fmt.Errorf("yahoo %s: %w", symbol, err)
	}
	s, err := Parse(body, now)
	if err != nil {
		return Series{}, fmt.Errorf("yahoo %s: %w", symbol, err)
	}
	for _, a := range s.Dropped {
		c.log.Warn("bar dropped", "symbol", symbol, "date", a.Date.Format(time.DateOnly), "close", a.Close, "reason", a.Reason)
	}
	return s, nil
}

// chartError extracts chart.error.description from an error response.
func chartError(he *httpx.HTTPError, err error) string {
	if he == nil {
		return ""
	}
	var r chartResponse
	if json.Unmarshal([]byte(he.Body), &r) == nil && r.Chart.Error != nil {
		return fmt.Sprintf("%s: %s (status %d)", r.Chart.Error.Code, r.Chart.Error.Description, he.Status)
	}
	return ""
}

type chartResponse struct {
	Chart struct {
		Result []struct {
			Meta struct {
				Symbol               string `json:"symbol"`
				Currency             string `json:"currency"`
				InstrumentType       string `json:"instrumentType"`
				ExchangeTimezoneName string `json:"exchangeTimezoneName"`
				RegularMarketTime    int64  `json:"regularMarketTime"`
				CurrentTradingPeriod struct {
					Regular struct {
						Start int64 `json:"start"`
						End   int64 `json:"end"`
					} `json:"regular"`
				} `json:"currentTradingPeriod"`
			} `json:"meta"`
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Close  []*float64 `json:"close"`
					Volume []*float64 `json:"volume"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// Parse decodes a chart response into closed daily bars: dates in the
// exchange time zone, null closes skipped, several bars of one day (an
// hourly response) reduced to the last one, the bar of a session still open
// at now left out (it would be revised by the next poll), zero closes and
// one-bar spikes dropped as anomalies. Futures sessions run to 23:59 ET in
// Yahoo, so a poll in the evening ET picks their close up the next day.
func Parse(body []byte, now time.Time) (Series, error) {
	var r chartResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return Series{}, fmt.Errorf("decode chart: %w", err)
	}
	if e := r.Chart.Error; e != nil {
		return Series{}, fmt.Errorf("%s: %s", e.Code, e.Description)
	}
	if len(r.Chart.Result) == 0 {
		return Series{}, ErrNoData
	}
	res := r.Chart.Result[0]
	m := res.Meta
	loc, err := time.LoadLocation(m.ExchangeTimezoneName)
	if err != nil {
		return Series{}, fmt.Errorf("exchange time zone %q: %w", m.ExchangeTimezoneName, err)
	}
	s := Series{Symbol: m.Symbol, Currency: m.Currency, Type: m.InstrumentType}
	if len(res.Timestamp) == 0 || len(res.Indicators.Quote) == 0 {
		return s, nil
	}
	q := res.Indicators.Quote[0]
	if len(q.Close) != len(res.Timestamp) {
		return Series{}, fmt.Errorf("chart: %d timestamps, %d closes", len(res.Timestamp), len(q.Close))
	}
	// The session in progress: its bar is not final until the close.
	reg := m.CurrentTradingPeriod.Regular
	openDay := time.Time{}
	if reg.Start > 0 && now.Unix() < reg.End {
		openDay = model.DayIn(time.Unix(reg.Start, 0), loc)
	}

	byDate := make(map[time.Time]Bar)
	for i, ts := range res.Timestamp {
		if q.Close[i] == nil || math.IsNaN(*q.Close[i]) {
			continue
		}
		d := model.DayIn(time.Unix(ts, 0), loc)
		if !openDay.IsZero() && !d.Before(openDay) {
			continue
		}
		b := Bar{Date: d, Close: float32Value(*q.Close[i])}
		if i < len(q.Volume) && q.Volume[i] != nil {
			b.Volume, b.HasVol = *q.Volume[i], true
		}
		byDate[d] = b // a repeated day: the later bar wins
	}
	bars := make([]Bar, 0, len(byDate))
	for _, b := range byDate {
		bars = append(bars, b)
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Date.Before(bars[j].Date) })
	s.Bars, s.Dropped = clean(bars)
	return s, nil
}

// clean drops zero closes and one-bar spikes.
func clean(bars []Bar) ([]Bar, []Anomaly) {
	var kept []Bar
	var dropped []Anomaly
	for _, b := range bars {
		if b.Close == 0 {
			dropped = append(dropped, Anomaly{b.Date, b.Close, "zero close"})
			continue
		}
		kept = append(kept, b)
	}
	out := kept[:0:0]
	for i, b := range kept {
		if i > 0 && i+1 < len(kept) {
			prev, next := kept[i-1].Close, kept[i+1].Close
			if prev > 0 && math.Abs(b.Close/prev-1) > spikeJump && math.Abs(next/prev-1) < spikeBack {
				dropped = append(dropped, Anomaly{b.Date, b.Close, fmt.Sprintf("spike: %.4g between %.4g and %.4g", b.Close, prev, next)})
				continue
			}
		}
		out = append(out, b)
	}
	return out, dropped
}

// float32Value undoes Yahoo's single-precision noise: 96.09500122070312
// becomes 96.095, the shortest decimal of the same float32.
func float32Value(v float64) float64 {
	r, err := strconv.ParseFloat(strconv.FormatFloat(v, 'g', -1, 32), 64)
	if err != nil {
		return v
	}
	return r
}
