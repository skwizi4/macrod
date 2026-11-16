// Package nasdaq reads upcoming earnings dates from the Nasdaq API
// (api.nasdaq.com/api/analyst/<ticker>/earnings-date; unofficial, no key,
// LOOKUP E5, 2026-10-25). Yahoo's calendar needs a crumb, so Nasdaq it is
// (decision 2026-10-25). The response also carries a consensus EPS: it is
// ignored — consensus goes to agents only as text (D11).
package nasdaq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
)

// Source and event name of earnings in the releases table.
const (
	Source = "earnings"
	// NameSuffix follows the ticker: "MSFT earnings".
	NameSuffix = " earnings"
)

// EventName is the releases.name of a company's earnings.
func EventName(ticker string) string { return ticker + NameSuffix }

// Report timing as Nasdaq states it.
const (
	TimeAfterClose = "after_close"
	TimeBeforeOpen = "before_open"
	TimeUnknown    = ""
)

// EarningsDate is the next report of a company. OK is false when Nasdaq has
// no date (the company reported and has not announced the next one).
type EarningsDate struct {
	Ticker    string
	Date      time.Time // calendar day
	Time      string    // TimeAfterClose | TimeBeforeOpen | TimeUnknown
	Estimated bool      // derived by Zacks' algorithm, not announced
	OK        bool
}

// Client fetches earnings dates.
type Client struct {
	http     *httpx.Client
	endpoint string // with {ticker}
	log      *slog.Logger
}

// New returns a client.
func New(endpoint string, h *httpx.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: h, endpoint: endpoint, log: log}
}

// EarningsDate returns the next earnings date of ticker.
func (c *Client) EarningsDate(ctx context.Context, ticker string) (EarningsDate, error) {
	body, err := c.http.Get(ctx, strings.ReplaceAll(c.endpoint, "{ticker}", ticker))
	if err != nil {
		return EarningsDate{}, fmt.Errorf("nasdaq earnings %s: %w", ticker, err)
	}
	d, err := Parse(body, ticker)
	if err != nil {
		return EarningsDate{}, fmt.Errorf("nasdaq earnings %s: %w", ticker, err)
	}
	return d, nil
}

// reportRe reads "… is expected* to report earnings on  10/28/2026 after
// market close." and "… is estimated to report earnings on  11/18/2026."
var reportRe = regexp.MustCompile(`(?i)\b(expected|estimated)\*?\s+to report earnings on\s+(\d{2}/\d{2}/\d{4})\.?\s*(after market close|before market open|prior to market open|pre-market)?`)

// noDate is Nasdaq's text when it has no upcoming date.
var noDate = regexp.MustCompile(`(?i)hasn't provided us with the upcoming earnings report date`)

// Parse decodes an earnings-date response.
func Parse(body []byte, ticker string) (EarningsDate, error) {
	var raw struct {
		Data *struct {
			ReportText string `json:"reportText"`
		} `json:"data"`
		Status struct {
			RCode int `json:"rCode"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return EarningsDate{}, fmt.Errorf("decode: %w", err)
	}
	if raw.Data == nil {
		return EarningsDate{}, fmt.Errorf("no data in response (rCode %d)", raw.Status.RCode)
	}
	out := EarningsDate{Ticker: ticker}
	text := raw.Data.ReportText
	m := reportRe.FindStringSubmatch(text)
	if m == nil {
		if noDate.MatchString(text) {
			return out, nil
		}
		return EarningsDate{}, fmt.Errorf("unrecognised reportText %q", text)
	}
	d, err := time.Parse("01/02/2006", m[2])
	if err != nil {
		return EarningsDate{}, fmt.Errorf("date %q: %w", m[2], err)
	}
	out.Date, out.OK = d, true
	out.Estimated = strings.EqualFold(m[1], "estimated")
	switch strings.ToLower(m[3]) {
	case "after market close":
		out.Time = TimeAfterClose
	case "":
	default:
		out.Time = TimeBeforeOpen
	}
	return out, nil
}

// Event is the releases row of an earnings date: the expected 8-K time
// (earnings_calendar times, in et) as UTC.
func Event(d EarningsDate, c config.Company, ec *config.EarningsCalendar, et *time.Location) model.ReleaseEvent {
	clock := ec.Unknown
	switch d.Time {
	case TimeAfterClose:
		clock = ec.AfterClose
	case TimeBeforeOpen:
		clock = ec.PreMarket
	}
	return model.ReleaseEvent{
		Source: Source, Name: EventName(d.Ticker), ScheduledAt: clock.On(d.Date, et).UTC(),
		Importance: c.Importance, Status: model.StatusScheduled,
		Related: model.Related{Ticker: d.Ticker, ReportTime: d.Time, Estimated: d.Estimated},
	}
}
