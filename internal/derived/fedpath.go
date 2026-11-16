package derived

import (
	"math"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/transform"
)

// FedPathName is the config.derived entry of the rate path.
const FedPathName = "FED_PATH"

// Rate path from 30-day fed funds futures (ZQ), the FedWatch substitute
// (ТЗ §6 derived FED_PATH; decision 2026-10-24): one row per FOMC meeting
// within the horizon, read from the contract of the month after the
// meeting, whose average rate is the post-meeting rate.
const (
	cutBP        = 25 // one cut or hike
	noTradesBars = 5  // bars without volume that flag a stale contract
	priceMaxAge  = 7  // days: an older price is not used
)

// FedPathInput is what ComputeFedPath reads.
type FedPathInput struct {
	AsOf        time.Time   // calendar day of the data (UTC midnight)
	MonthsAhead int         // contracts up to this many months after AsOf's month
	Meetings    []time.Time // FOMC decision days
	Symbol      func(month time.Time) string
	Prices      map[string][]model.Observation // by symbol, ascending
	Volumes     map[string][]model.Observation // by symbol, ascending; may be missing
	DFF         []model.Observation
}

// FedPathRow is one contract of the path.
type FedPathRow struct {
	Month   time.Time // contract month, first day
	Symbol  string
	Meeting time.Time       // decision day the contract follows
	Implied transform.Value // 100 − price at PriceDate, %
	VsDFFBP transform.Value // (implied − DFF) × 100
	WeekAgo transform.Value // implied at PriceDate − 7 days
	// MeetingBP is what the meeting itself is priced to do: implied minus
	// the previous row's implied (DFF for the first row), in basis points.
	MeetingBP transform.Value
	NoTrades  bool // no volume over the last noTradesBars bars: the price may be stale
	// Thin: traded, but its average volume over the last noTradesBars bars
	// is under thinShare of the previous row's; left out of the totals.
	Thin bool
	// New: the contract entered the window this week (it was not in the
	// table a week ago); left out of the totals, whose weekly change
	// compares the same contract.
	New bool
}

// FedPath is the rate path as of a day.
type FedPath struct {
	AsOf time.Time
	// PriceDate is the one day all rows are read at: the latest price of
	// the nearest contract (zero if none). Contracts fetched at different
	// times would otherwise mix days.
	PriceDate       time.Time
	DFF, DFFWeekAgo transform.Value
	Rows            []FedPathRow
	// Path to the horizon: the last row with a price now and a week ago
	// that was in the table a week ago and trades normally; implied − DFF
	// in basis points (negative = cuts priced) and in moves of 25 b.p.
	// (positive = cuts).
	Horizon               time.Time // month of that row, zero if none
	HorizonWeekAgo        time.Time // the horizon of the path a week ago
	PathBP, PathBPWeekAgo transform.Value
	Cuts, CutsWeekAgo     transform.Value
	// NearBP is the change priced over the next NearMeetings meetings (the
	// most liquid contracts): implied of the last of them − DFF, b.p.
	NearMeeting           time.Time // decision day of the last of them
	NearBP, NearBPWeekAgo transform.Value
}

// NearMeetings and thinShare: see FedPath.NearBP and FedPathRow.Thin.
const (
	NearMeetings = 3
	thinShare    = 0.25
)

// ComputeFedPath builds the path as of in.AsOf.
func ComputeFedPath(in FedPathInput) FedPath {
	p := computeFedPath(in)
	if p.PriceDate.IsZero() {
		return p
	}
	wa := in
	wa.AsOf = p.PriceDate.AddDate(0, 0, -7)
	p.HorizonWeekAgo = computeFedPath(wa).Horizon
	return p
}

func computeFedPath(in FedPathInput) FedPath {
	asOf := model.Day(in.AsOf)
	p := FedPath{AsOf: asOf}
	first := time.Date(asOf.Year(), asOf.Month(), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, in.MonthsAhead, 0)
	seen := map[time.Time]bool{}
	for _, m := range in.Meetings {
		d := model.Day(m)
		if d.Before(asOf) {
			continue // decided: the rate is known
		}
		month := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
		if month.After(last) || seen[month] {
			continue
		}
		seen[month] = true
		p.Rows = append(p.Rows, FedPathRow{Month: month, Symbol: in.Symbol(month), Meeting: d})
	}
	sortRows(p.Rows)
	// One price day for all rows: the nearest contract's latest price,
	// else the latest of any row.
	for i, r := range p.Rows {
		_, d := valueDateAt(in.Prices[r.Symbol], asOf)
		if i == 0 && !d.IsZero() {
			p.PriceDate = d
			break
		}
		if d.After(p.PriceDate) {
			p.PriceDate = d
		}
	}
	day := asOf
	if !p.PriceDate.IsZero() {
		day = p.PriceDate
	}
	weekAgo := day.AddDate(0, 0, -7)
	p.DFF, p.DFFWeekAgo = valueAt(in.DFF, day), valueAt(in.DFF, weekAgo)
	// The table a week ago ended at this month.
	lastWeekAgo := time.Date(weekAgo.Year(), weekAgo.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, in.MonthsAhead, 0)
	vols := make([]float64, len(p.Rows))
	for i := range p.Rows {
		r := &p.Rows[i]
		if v := valueAt(in.Prices[r.Symbol], day); v.OK {
			r.Implied = transform.Value{V: 100 - v.V, OK: true}
		}
		if v := valueAt(in.Prices[r.Symbol], weekAgo); v.OK {
			r.WeekAgo = transform.Value{V: 100 - v.V, OK: true}
		}
		if r.Implied.OK && p.DFF.OK {
			r.VsDFFBP = transform.Value{V: (r.Implied.V - p.DFF.V) * 100, OK: true}
		}
		r.NoTrades = noTrades(in.Volumes[r.Symbol], day)
		vols[i] = avgVolume(in.Volumes[r.Symbol], day)
		r.New = r.Month.After(lastWeekAgo)
	}
	// Far contracts trade less by nature: thin is against the nearer
	// neighbour, not the front month.
	for i := 1; i < len(p.Rows); i++ {
		p.Rows[i].Thin = !p.Rows[i].NoTrades && vols[i] > 0 && vols[i-1] > 0 && vols[i] < thinShare*vols[i-1]
	}
	before := p.DFF
	for i := range p.Rows {
		r := &p.Rows[i]
		if r.Implied.OK && before.OK {
			r.MeetingBP = transform.Value{V: (r.Implied.V - before.V) * 100, OK: true}
		}
		before = r.Implied
	}
	bpOf := func(implied, dff transform.Value) transform.Value {
		if !implied.OK || !dff.OK {
			return transform.Value{}
		}
		return transform.Value{V: (implied.V - dff.V) * 100, OK: true}
	}
	for i := len(p.Rows) - 1; i >= 0; i-- {
		r := p.Rows[i]
		if !r.VsDFFBP.OK || r.NoTrades || r.Thin || r.New {
			continue
		}
		p.Horizon, p.PathBP = r.Month, r.VsDFFBP
		p.Cuts = transform.Value{V: -r.VsDFFBP.V / cutBP, OK: true}
		if wa := bpOf(r.WeekAgo, p.DFFWeekAgo); wa.OK {
			p.PathBPWeekAgo = wa
			p.CutsWeekAgo = transform.Value{V: -wa.V / cutBP, OK: true}
		}
		break
	}
	if len(p.Rows) >= NearMeetings {
		r := p.Rows[NearMeetings-1]
		p.NearMeeting, p.NearBP, p.NearBPWeekAgo = r.Meeting, r.VsDFFBP, bpOf(r.WeekAgo, p.DFFWeekAgo)
	}
	return p
}

// avgVolume is the average volume of the last noTradesBars bars up to day.
func avgVolume(vol []model.Observation, day time.Time) float64 {
	var sum float64
	n := 0
	for i := len(vol) - 1; i >= 0 && n < noTradesBars; i-- {
		if model.Day(vol[i].Date).After(day) {
			continue
		}
		sum += vol[i].Value
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// MoveProbability is the chance of one 25 b.p. move implied by a
// meeting's priced change: |bp|/25, within 0–1.
func MoveProbability(bp float64) float64 {
	return min(math.Abs(bp)/cutBP, 1)
}

// valueAt is the last value on or before day, at most priceMaxAge old.
func valueAt(obs []model.Observation, day time.Time) transform.Value {
	v, _ := valueDateAt(obs, day)
	return v
}

// valueDateAt is valueAt with the date of the value.
func valueDateAt(obs []model.Observation, day time.Time) (transform.Value, time.Time) {
	for i := len(obs) - 1; i >= 0; i-- {
		d := model.Day(obs[i].Date)
		if d.After(day) {
			continue
		}
		if day.Sub(d) > priceMaxAge*24*time.Hour {
			return transform.Value{}, time.Time{}
		}
		return transform.Value{V: obs[i].Value, OK: true}, d
	}
	return transform.Value{}, time.Time{}
}

// noTrades: the last noTradesBars bars up to day all have zero volume.
func noTrades(vol []model.Observation, day time.Time) bool {
	n := 0
	for i := len(vol) - 1; i >= 0 && n < noTradesBars; i-- {
		if model.Day(vol[i].Date).After(day) {
			continue
		}
		if vol[i].Value != 0 {
			return false
		}
		n++
	}
	return n == noTradesBars
}

func sortRows(rows []FedPathRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].Month.Before(rows[j-1].Month); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}
