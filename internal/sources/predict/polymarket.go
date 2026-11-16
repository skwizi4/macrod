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

// Polymarket reads events from the Gamma API and price history from the
// CLOB API; both are public for market data.
type Polymarket struct {
	http  *httpx.Client
	gamma string // https://gamma-api.polymarket.com
	clob  string // https://clob.polymarket.com
}

// NewPolymarket returns a client for the Gamma and CLOB base URLs.
func NewPolymarket(gamma, clob string, h *httpx.Client) *Polymarket {
	return &Polymarket{http: h, gamma: gamma, clob: clob}
}

type polyEvent struct {
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	EndDate string `json:"endDate"`
	Closed  bool   `json:"closed"`
	Markets []struct {
		Slug          string `json:"slug"`
		Question      string `json:"question"`
		GroupItem     string `json:"groupItemTitle"`
		OutcomePrices string `json:"outcomePrices"` // a JSON array in a string
		ClobTokenIDs  string `json:"clobTokenIds"`  // the same
		Closed        bool   `json:"closed"`
		Active        bool   `json:"active"`
		EndDate       string `json:"endDate"`
	} `json:"markets"`
}

// Event returns the open markets of an event by slug.
func (p *Polymarket) Event(ctx context.Context, slug string) ([]Market, error) {
	body, err := p.http.Get(ctx, p.gamma+"/events?"+url.Values{"slug": {slug}}.Encode())
	if err != nil {
		return nil, fmt.Errorf("polymarket %s: %w", slug, err)
	}
	var evs []polyEvent
	if err := json.Unmarshal(body, &evs); err != nil {
		return nil, fmt.Errorf("polymarket %s: decode events: %w", slug, err)
	}
	if len(evs) == 0 {
		return nil, fmt.Errorf("polymarket %s: no such event", slug)
	}
	return polyMarkets(evs[0]), nil
}

// Search returns the open markets of the active event whose title starts
// with prefix and that ends first ("Fed Decision in" → the next meeting).
func (p *Polymarket) Search(ctx context.Context, prefix string) ([]Market, error) {
	q := url.Values{"q": {prefix}, "events_status": {"active"}, "limit_per_type": {"20"}}
	body, err := p.http.Get(ctx, p.gamma+"/public-search?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("polymarket search %q: %w", prefix, err)
	}
	var r struct {
		Events []polyEvent `json:"events"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("polymarket search %q: decode: %w", prefix, err)
	}
	var best *polyEvent
	var bestEnd time.Time
	for i, e := range r.Events {
		if e.Closed || !strings.HasPrefix(strings.ToLower(e.Title), strings.ToLower(prefix)) {
			continue
		}
		end, err := time.Parse(time.RFC3339, e.EndDate)
		if err != nil {
			continue
		}
		if best == nil || end.Before(bestEnd) {
			best, bestEnd = &r.Events[i], end
		}
	}
	if best == nil {
		return nil, nil
	}
	return polyMarkets(*best), nil
}

func polyMarkets(e polyEvent) []Market {
	var out []Market
	for _, m := range e.Markets {
		if m.Closed || !m.Active {
			continue
		}
		var prices, tokens []string
		if json.Unmarshal([]byte(m.OutcomePrices), &prices) != nil || len(prices) == 0 {
			continue
		}
		pr, ok := price(prices[0])
		if !ok {
			continue
		}
		_ = json.Unmarshal([]byte(m.ClobTokenIDs), &tokens)
		cl, _ := time.Parse(time.RFC3339, m.EndDate)
		mk := Market{ID: m.Slug, Label: label(e.Title, m.GroupItem), Event: e.Slug, Question: e.Title, Outcome: m.GroupItem, Close: cl, Price: pr}
		if len(tokens) > 0 {
			mk.Token = tokens[0]
		}
		out = append(out, mk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// historySpan is the longest window the CLOB accepts for some markets
// ("'startTs' and 'endTs' interval is too long" above ~15 days).
const historySpan = 14 * 24 * time.Hour

// History returns the daily prices of "yes" of a CLOB token in [from, to]
// (the last price of each New York day), in windows of 14 days.
func (p *Polymarket) History(ctx context.Context, token string, from, to time.Time) ([]Point, error) {
	var out []Point
	for start := from; start.Before(to); start = start.Add(historySpan) {
		end := start.Add(historySpan)
		if end.After(to) {
			end = to
		}
		ps, err := p.history(ctx, token, start, end)
		if err != nil {
			return nil, err
		}
		out = append(out, ps...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return lastPerDay(out), nil
}

func (p *Polymarket) history(ctx context.Context, token string, from, to time.Time) ([]Point, error) {
	q := url.Values{
		"market":   {token},
		"startTs":  {strconv.FormatInt(from.Unix(), 10)},
		"endTs":    {strconv.FormatInt(to.Unix(), 10)},
		"fidelity": {"1440"},
	}
	body, err := p.http.Get(ctx, p.clob+"/prices-history?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("polymarket history: %w", err)
	}
	var r struct {
		History []struct {
			T int64   `json:"t"`
			P float64 `json:"p"`
		} `json:"history"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("polymarket history: decode: %w", err)
	}
	out := make([]Point, 0, len(r.History))
	for _, h := range r.History {
		out = append(out, Point{Date: Day(time.Unix(h.T, 0)), Price: h.P})
	}
	return out, nil
}
