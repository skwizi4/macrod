package export

import (
	"context"
	"math"
	"strings"
	"time"

	"macro-collector/internal/transform"
)

// DailyMovesRow is one instrument of section 2: Values by the days of the
// section (null: no close), Best the largest move of the week.
type DailyMovesRow struct {
	Label     string     `json:"label"`
	Values    []Quantity `json:"values"`
	Best      Day        `json:"best"`
	BestValue Quantity   `json:"best_value"`
}

// MovesSection is section 2 of USA Sentiment.
type MovesSection struct {
	Configured bool            `json:"configured"`
	Days       []Day           `json:"days"`
	Rows       []DailyMovesRow `json:"rows"`
}

// sentMoves is G4b: the daily close-to-close moves of the week's trading
// days for NDX, 2Y, DXY and WTI (Europe: GER40, Bund 10Y, EUR/USD, TTF),
// the largest of each instrument in bold; unlike the reaction table it does
// not depend on the calendar. A stored rate series moves in b.p.
func (w *week) sentMoves(ctx context.Context) (MovesSection, []view, error) {
	ms := w.sent().DailyMoves
	out := MovesSection{Configured: len(ms) > 0, Days: []Day{}, Rows: []DailyMovesRow{}}
	if len(ms) == 0 {
		return out, nil, nil
	}
	var days []time.Time
	for d := dayOf(w.from, w.et).AddDate(0, 0, 1); !d.After(dayOf(w.to, w.et)); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			days = append(days, d)
			out.Days = append(out.Days, Day{d})
		}
	}
	if len(days) == 0 {
		return out, nil, nil
	}
	var vs []view
	for _, m := range ms {
		v, err := w.instrument(ctx, m.Symbol, m.Series)
		if err != nil {
			return out, nil, err
		}
		vs = append(vs, v)
		rate := m.Series != "" && v.Kind == kindRate
		unit := unitPercent
		if m.Duration > 0 || rate {
			unit = unitBP
		}
		byDay := map[time.Time]float64{}
		best, bestAbs := time.Time{}, -1.0
		moves := transform.DailyMoves(v.Obs, days[0], days[len(days)-1])
		if rate {
			moves = transform.DailyChanges(v.Obs, days[0], days[len(days)-1], 100)
		}
		for _, mv := range moves {
			x := mv.Pct
			if m.Duration > 0 {
				x = transform.YieldFromPrice(some(mv.Pct), m.Duration).V
			}
			byDay[mv.Date] = x
			if math.Abs(x) > bestAbs {
				best, bestAbs = mv.Date, math.Abs(x)
			}
		}
		row := DailyMovesRow{Label: m.Label, Values: []Quantity{}, Best: Day{best}, BestValue: qty(transform.Value{}, unit)}
		for _, d := range days {
			x, ok := byDay[d]
			row.Values = append(row.Values, qty(transform.Value{V: x, OK: ok}, unit))
		}
		if !best.IsZero() {
			row.BestValue = qty(some(byDay[best]), unit)
		}
		out.Rows = append(out.Rows, row)
	}
	return out, vs, nil
}

// instrument is a Yahoo quote by symbol, or a stored series by name.
func (w *week) instrument(ctx context.Context, symbol, series string) (view, error) {
	if series != "" {
		return w.resolve(ctx, series)
	}
	return w.quote(ctx, symbol)
}

// moveCell is a daily move: "+1.20%", "≈+6" for yields in basis points.
func moveCell(q Quantity) string {
	if q.Unit == unitBP {
		return "≈" + num(q.val().V, 0, true)
	}
	return num(q.val().V, 2, true) + "%"
}

// moveDay is "Пн 10-26".
func moveDay(d Day) string { return ruWeekdays[d.Weekday()] + " " + d.Format("01-02") }

// dailyMovesCells are the day cells of a row, the largest move in bold.
func dailyMovesCells(r DailyMovesRow, days []Day) string {
	var out strings.Builder
	for i, q := range r.Values {
		out.WriteString(" | ")
		switch {
		case q.Value == nil:
			out.WriteString(dash)
		case days[i].Equal(r.Best.Time):
			out.WriteString("**" + moveCell(q) + "**")
		default:
			out.WriteString(moveCell(q))
		}
	}
	return out.String()
}

// dailyMovesTop is the last column of a row.
func dailyMovesTop(r DailyMovesRow) string {
	if r.Best.IsZero() {
		return dash
	}
	return moveDay(r.Best) + ": " + moveCell(r.BestValue)
}

// dayOf is t's calendar day in loc as a UTC midnight date, like bar dates.
func dayOf(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}
