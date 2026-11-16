package predict

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/sources/httpx"
)

// Kalshi reads https://api.elections.kalshi.com/trade-api/v2 (all markets,
// not only elections); market data needs no authentication.
type Kalshi struct {
	http     *httpx.Client
	endpoint string
}

// NewKalshi returns a client for the API base URL.
func NewKalshi(endpoint string, h *httpx.Client) *Kalshi {
	return &Kalshi{http: h, endpoint: endpoint}
}

type kalshiMarket struct {
	Ticker      string `json:"ticker"`
	EventTicker string `json:"event_ticker"`
	Title       string `json:"title"`
	YesSubTitle string `json:"yes_sub_title"`
	CloseTime   string `json:"close_time"`
	LastPrice   string `json:"last_price_dollars"`
	YesBid      string `json:"yes_bid_dollars"`
	YesAsk      string `json:"yes_ask_dollars"`
}

// Open returns the open markets of a series; event narrows them to one
// event ("CONTROLH-2026"), nearest to the event closing first (the next
// FOMC meeting).
func (k *Kalshi) Open(ctx context.Context, series, event string, nearest bool) ([]Market, error) {
	q := url.Values{"series_ticker": {series}, "status": {"open"}, "limit": {"200"}}
	body, err := k.http.Get(ctx, k.endpoint+"/markets?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("kalshi %s: %w", series, err)
	}
	ms, err := parseKalshiMarkets(body)
	if err != nil {
		return nil, fmt.Errorf("kalshi %s: %w", series, err)
	}
	var out []Market
	for _, m := range ms {
		if event == "" || m.Event == event {
			out = append(out, m)
		}
	}
	if nearest && len(out) > 0 {
		first := out[0].Close
		for _, m := range out {
			if m.Close.Before(first) {
				first = m.Close
			}
		}
		var near []Market
		for _, m := range out {
			if m.Close.Equal(first) {
				near = append(near, m)
			}
		}
		out = near
	}
	k.questions(ctx, out)
	return out, nil
}

func parseKalshiMarkets(body []byte) ([]Market, error) {
	var r struct {
		Markets []kalshiMarket `json:"markets"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("decode markets: %w", err)
	}
	var out []Market
	for _, m := range r.Markets {
		p, ok := price(m.LastPrice)
		if !ok {
			continue
		}
		cl, _ := time.Parse(time.RFC3339, m.CloseTime)
		outcome := m.YesSubTitle
		if outcome == "Yes" {
			outcome = ""
		}
		out = append(out, Market{ID: m.Ticker, Label: label(m.Title, outcome), Event: m.EventTicker,
			Question: m.Title, Outcome: outcome, Close: cl, Price: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// questions sets the question of the markets to their event's title
// (GET /events/{event_ticker}, LOOKUP G4a: "Fed decision in Dec 2026? (On
// Dec 9, 2026)"); a market title stays the question when the event cannot
// be read, the prices matter more.
func (k *Kalshi) questions(ctx context.Context, ms []Market) {
	titles := map[string]string{}
	for i := range ms {
		e := ms[i].Event
		t, seen := titles[e]
		if !seen {
			t, _ = k.eventTitle(ctx, e)
			titles[e] = t
		}
		if t != "" {
			ms[i].Question = t
			ms[i].Label = label(t, ms[i].Outcome)
		}
	}
}

func (k *Kalshi) eventTitle(ctx context.Context, event string) (string, error) {
	body, err := k.http.Get(ctx, k.endpoint+"/events/"+url.PathEscape(event))
	if err != nil {
		return "", fmt.Errorf("kalshi event %s: %w", event, err)
	}
	var r struct {
		Event struct {
			Title    string `json:"title"`
			SubTitle string `json:"sub_title"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("kalshi event %s: decode: %w", event, err)
	}
	t := r.Event.Title
	if r.Event.SubTitle != "" && !strings.Contains(t, r.Event.SubTitle) {
		t += " (" + r.Event.SubTitle + ")"
	}
	return t, nil
}

// History returns the daily closing prices of a market in [from, to]; a
// day without trades closes at the mid of the yes bid and ask.
func (k *Kalshi) History(ctx context.Context, series, ticker string, from, to time.Time) ([]Point, error) {
	q := url.Values{
		"start_ts":        {strconv.FormatInt(from.Unix(), 10)},
		"end_ts":          {strconv.FormatInt(to.Unix(), 10)},
		"period_interval": {"1440"},
	}
	body, err := k.http.Get(ctx, fmt.Sprintf("%s/series/%s/markets/%s/candlesticks?%s", k.endpoint,
		url.PathEscape(series), url.PathEscape(ticker), q.Encode()))
	if err != nil {
		return nil, fmt.Errorf("kalshi %s history: %w", ticker, err)
	}
	return parseKalshiCandles(body)
}

func parseKalshiCandles(body []byte) ([]Point, error) {
	var r struct {
		Candles []struct {
			End   int64 `json:"end_period_ts"`
			Price struct {
				Close string `json:"close_dollars"`
			} `json:"price"`
			Bid struct {
				Close string `json:"close_dollars"`
			} `json:"yes_bid"`
			Ask struct {
				Close string `json:"close_dollars"`
			} `json:"yes_ask"`
		} `json:"candlesticks"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("decode candlesticks: %w", err)
	}
	var out []Point
	for _, c := range r.Candles {
		p, ok := price(c.Price.Close)
		if !ok {
			b, okb := price(c.Bid.Close)
			a, oka := price(c.Ask.Close)
			if !okb || !oka || a == 0 {
				continue
			}
			p = (a + b) / 2
		}
		// The daily candle ends at midnight ET: its day is the one before.
		out = append(out, Point{Date: Day(time.Unix(c.End-1, 0)), Price: p})
	}
	return lastPerDay(out), nil
}
