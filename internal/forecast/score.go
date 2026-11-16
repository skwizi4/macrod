package forecast

import (
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

// Prices are the daily closes of a series, in date order.
type Prices struct {
	Series string // source/id
	Closes []model.Observation
}

// Window is the trading week a forecast is for: the report's export period
// ends at From (its last close is the reference), the forecast week ends
// at To (its last close is the result).
type Window struct {
	From, To time.Time
}

// Score scores f on primary, or on fallback when primary has no reference
// or result close; ok is false while the forecast week has not ended or
// neither series has both closes. The rules (решение пользователя
// 2026-11-13): the reference is the last close of the report week, the
// result the last close of the next one; any close in between beyond the
// invalidation level invalidates (up: below, down: above, range: on the
// level's side of the reference); else up hits on a rise, down on a fall,
// range on |change| < rangePct %. The level is not checked on the
// fallback: its prices are on another scale.
func Score(f store.ForecastItem, w Window, primary, fallback Prices, rangePct float64, now time.Time) (store.ForecastScore, bool) {
	if now.Before(w.To) {
		return store.ForecastScore{}, false
	}
	if sc, ok := score(f, w, primary, rangePct, true); ok {
		return sc, true
	}
	if fallback.Series == "" {
		return store.ForecastScore{}, false
	}
	return score(f, w, fallback, rangePct, false)
}

func score(f store.ForecastItem, w Window, p Prices, rangePct float64, checkLevel bool) (store.ForecastScore, bool) {
	// Closes are dated by the exchange day; a week's closes are those after
	// the previous period's end day up to its own end day.
	day := func(t time.Time) string { return t.Format(time.DateOnly) }
	refFrom, refTo := day(w.From.AddDate(0, 0, -7)), day(w.From)
	resTo := day(w.To)
	ref, res := -1, -1
	for i, o := range p.Closes {
		d := o.Date.Format(time.DateOnly)
		if d > refFrom && d <= refTo {
			ref = i
		}
		if d > refTo && d <= resTo {
			res = i
		}
	}
	if ref < 0 || res < 0 {
		return store.ForecastScore{}, false
	}
	refP, resP := p.Closes[ref].Value, p.Closes[res].Value
	sc := store.ForecastScore{
		PriceSeries: p.Series,
		RefDate:     p.Closes[ref].Date, RefPrice: refP,
		ResultDate: p.Closes[res].Date, ResultPrice: resP,
		ChangePct: (resP/refP - 1) * 100,
	}
	if checkLevel {
		below := f.Direction == Up || (f.Direction == Range && f.Invalidation < refP)
		for _, o := range p.Closes[ref+1 : res+1] {
			if (below && o.Value < f.Invalidation) || (!below && o.Value > f.Invalidation) {
				sc.Outcome = store.OutcomeInvalidated
				return sc, true
			}
		}
	}
	hit := false
	switch f.Direction {
	case Up:
		hit = resP > refP
	case Down:
		hit = resP < refP
	case Range:
		hit = sc.ChangePct < rangePct && sc.ChangePct > -rangePct
	}
	sc.Outcome = store.OutcomeMiss
	if hit {
		sc.Outcome = store.OutcomeHit
	}
	return sc, true
}
