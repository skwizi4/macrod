package treasury

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources"
)

// Calendar event names of Fiscal Data releases (releases.source = "treasury").
const (
	NameDTS = "Daily Treasury Statement"
	NameMTS = "Monthly Treasury Statement"
)

// Fiscal Data dataset ids in the release calendar (LOOKUP T7).
const (
	datasetDTS = "015-BFS-2014Q1-03"
	datasetMTS = "015-BFS-2014Q1-13"
)

// Keys are the fixed (releases.source, name) series of the calendar;
// auction events (SourceAuction) are named after the security and found
// dynamically.
var Keys = [][2]string{
	{Source, NameDTS}, {Source, NameMTS},
	{Source, NameFinancingEstimates}, {Source, NameRefundingStatement},
}

// RefundingKeys are the series of Keys taken from the refunding page.
var RefundingKeys = [][2]string{{Source, NameFinancingEstimates}, {Source, NameRefundingStatement}}

// Calendar lists upcoming auctions (TA_WS), DTS and MTS publications
// (Fiscal Data release calendar) and refunding releases (refunding page).
//
// TA_WS "upcoming" drops an auction once it closes, so auctions already
// held in the window come from the search API; otherwise the sync would
// mark them rescheduled.
type Calendar struct {
	Client        *Client
	SecurityTypes []string // Bill | Note | Bond
	ET            *time.Location
	Now           func() time.Time // defaults to time.Now
}

var _ sources.CalendarSource = (*Calendar)(nil)

// Name implements sources.CalendarSource.
func (c *Calendar) Name() string { return Source }

// Upcoming implements sources.CalendarSource.
func (c *Calendar) Upcoming(ctx context.Context, from, to time.Time) ([]model.ReleaseEvent, error) {
	upcoming, err := c.Client.FetchUpcoming(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	var held []model.Auction
	if day := model.DayIn(from, c.ET); !day.After(model.DayIn(now, c.ET)) {
		if held, err = c.Client.FetchAuctions(ctx, c.SecurityTypes, day, model.DayIn(now, c.ET)); err != nil {
			return nil, err
		}
	}
	events := AuctionEvents(mergeAuctions(held, upcoming), c.ET)
	pubs, err := c.Client.FetchReleaseCalendar(ctx)
	if err != nil {
		return nil, err
	}
	events = append(events, pubs...)
	// The refunding page is scraped HTML: when it fails, the auctions and
	// publications are still listed and the refunding series left as stored.
	var partial error
	if secs, err := c.Client.RefundingSections(ctx, c.ET); err != nil {
		partial = &sources.PartialError{Keys: RefundingKeys, Err: err}
	} else {
		events = append(events, RefundingEvents(secs)...)
	}

	var out []model.ReleaseEvent
	for _, e := range events {
		if !e.ScheduledAt.Before(from) && e.ScheduledAt.Before(to) {
			out = append(out, e)
		}
	}
	return out, partial
}

// mergeAuctions joins held and upcoming auctions, one per (date, CUSIP).
func mergeAuctions(lists ...[]model.Auction) []model.Auction {
	type key struct {
		day   time.Time
		cusip string
	}
	seen := make(map[key]bool)
	var out []model.Auction
	for _, l := range lists {
		for _, a := range l {
			k := key{a.AuctionDate, a.CUSIP}
			if !seen[k] {
				seen[k] = true
				out = append(out, a)
			}
		}
	}
	sortAuctions(out)
	return out
}

// AuctionEvents turns announced auctions into calendar events at their
// competitive close (ET); a missing close time defaults to 11:30 for bills
// and 13:00 for coupons, the usual times.
func AuctionEvents(as []model.Auction, et *time.Location) []model.ReleaseEvent {
	out := make([]model.ReleaseEvent, 0, len(as))
	for _, a := range as {
		hh, mm := 13, 0
		if a.SecurityType == model.KindBill {
			hh, mm = 11, 30
		}
		if a.ClosingTime != "" {
			if t, err := time.Parse("15:04", a.ClosingTime); err == nil {
				hh, mm = t.Hour(), t.Minute()
			}
		}
		y, m, d := a.AuctionDate.Date()
		out = append(out, model.ReleaseEvent{
			Source:      SourceAuction,
			Name:        a.Security(),
			ScheduledAt: time.Date(y, m, d, hh, mm, 0, 0, et).UTC(),
			Importance:  AuctionImportance(a),
			Related:     model.Related{CUSIP: a.CUSIP, Security: a.Security()},
		})
	}
	return out
}

// AuctionImportance: 10-, 20- and 30-year nominal coupons are high (DATA_MAP
// calendar_usa), other notes, bonds and TIPS medium, bills, CMBs and FRNs low.
func AuctionImportance(a model.Auction) string {
	switch a.Kind {
	case model.KindNote, model.KindBond:
		switch a.Tenor {
		case "10-Year", "20-Year", "30-Year":
			return "high"
		}
		return "medium"
	case model.KindTIPS:
		return "medium"
	}
	return "low"
}

type releaseDate struct {
	DatasetID string `json:"datasetId"`
	Date      string `json:"date"`
	Time      string `json:"time"` // UTC
}

// FetchReleaseCalendar returns the DTS and MTS publications of the Fiscal
// Data release calendar.
func (c *Client) FetchReleaseCalendar(ctx context.Context) ([]model.ReleaseEvent, error) {
	body, err := c.http.Get(ctx, c.calendarURL)
	if err != nil {
		return nil, fmt.Errorf("treasury release calendar: %w", err)
	}
	var rds []releaseDate
	if err := json.Unmarshal(body, &rds); err != nil {
		return nil, fmt.Errorf("treasury release calendar decode: %w", err)
	}
	var out []model.ReleaseEvent
	for _, rd := range rds {
		var name, dataset, importance string
		switch rd.DatasetID {
		case datasetDTS:
			name, dataset, importance = NameDTS, "dts", "low"
		case datasetMTS:
			name, dataset, importance = NameMTS, "mts", "medium"
		default:
			continue
		}
		at, err := time.Parse("2006-01-02 15:04", rd.Date+" "+strings.TrimSpace(rd.Time))
		if err != nil {
			return nil, fmt.Errorf("treasury release calendar %s %s %s: %w", rd.DatasetID, rd.Date, rd.Time, err)
		}
		out = append(out, model.ReleaseEvent{
			Source: Source, Name: name, ScheduledAt: at.UTC(),
			Importance: importance, Related: model.Related{Dataset: dataset},
		})
	}
	return out, nil
}
