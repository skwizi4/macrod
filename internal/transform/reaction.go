package transform

import (
	"time"

	"macro-collector/internal/model"
)

// Earnings report sessions (company_reports.session), by the 8-K acceptance
// time in New York.
const (
	SessionBeforeOpen = "bmo"      // before 09:30 ET
	SessionAfterClose = "amc"      // 16:00 ET or later, or a non-trading day
	SessionIntraday   = "intraday" // during trading hours
)

// Session classifies a report accepted at acceptedAt; ny is America/New_York.
// A report on a weekend counts as after the close of the last session.
func Session(acceptedAt time.Time, ny *time.Location) string {
	t := acceptedAt.In(ny)
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return SessionAfterClose
	}
	switch m := t.Hour()*60 + t.Minute(); {
	case m < 9*60+30:
		return SessionBeforeOpen
	case m >= 16*60:
		return SessionAfterClose
	default:
		return SessionIntraday
	}
}

// Reaction is the price move on an earnings report.
type Reaction struct {
	Pct  Value     // % change, close to close
	Date time.Time // the session that reacted
	// Base is the close the move is measured from. While the reacting
	// session has not closed, Pct is !OK and Waiting is the first weekday
	// it can be (holidays are not known here).
	Base    Value
	Waiting time.Time
}

// MarketCapChange is the approximate change of market value on the
// report, in dollars: the move × base close × diluted shares (phase 5c;
// shares are the latest weighted average, so "≈").
func MarketCapChange(r Reaction, shares float64) Value {
	if !r.Pct.OK || !r.Base.OK || shares <= 0 {
		return Value{}
	}
	return some(r.Pct.V / 100 * r.Base.V * shares)
}

// PriceReaction is the close-to-close move of the first session that could
// trade on the report. After the close: the first session after the report
// day against the last close on or before it. Before the open or intraday:
// the first session on or after the report day against the last close
// before it (a pre-market report on a holiday reacts on the next session).
// bars are daily closes, ascending, one per session (exchange calendar
// day); reportDay is the New York date of acceptance. !OK until the
// reacting session has closed.
func PriceReaction(bars []model.Observation, reportDay time.Time, session string) Reaction {
	day := model.Day(reportDay)
	if session != SessionAfterClose {
		day = day.AddDate(0, 0, -1) // the reacting session may be the report day
	}
	base, next := -1, -1
	for i, b := range bars {
		if !model.Day(b.Date).After(day) {
			base = i
		} else if next < 0 {
			next = i
		}
	}
	if base < 0 {
		return Reaction{}
	}
	if next < 0 {
		w := day.AddDate(0, 0, 1)
		for w.Weekday() == time.Saturday || w.Weekday() == time.Sunday {
			w = w.AddDate(0, 0, 1)
		}
		return Reaction{Base: some(bars[base].Value), Waiting: w}
	}
	return Reaction{Pct: Growth(bars[next].Value, bars[base].Value), Date: model.Day(bars[next].Date), Base: some(bars[base].Value)}
}
