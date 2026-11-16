package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/store"
)

// ManualResult is what one typed-in value did.
type ManualResult struct {
	Series config.SourceSeries
	Date   time.Time // the observation date of the period
	Old    float64   // the value stored before, when Revised
	store.UpsertResult
}

// periodFormats are the forms of --period by the frequency of the series.
var periodFormats = map[string]string{"d": "YYYY-MM-DD", "w": "YYYY-Www", "m": "YYYY-MM", "q": "YYYY-Qn"}

// ManualPeriod reads the period of a manual value: it must be written in
// the frequency of the series (a month for a monthly one) and must not lie
// in the future.
func ManualPeriod(s config.SourceSeries, period string, now time.Time) (time.Time, error) {
	want := periodFormats[s.Frequency]
	ok := false
	switch s.Frequency {
	case "d":
		ok = len(period) == 10
	case "w":
		ok = strings.Contains(period, "-W")
	case "m":
		ok = len(period) == 7 && !strings.ContainsAny(period, "WQ")
	case "q":
		ok = strings.Contains(period, "-Q")
	}
	date, err := sources.ParsePeriod(period)
	if !ok || err != nil {
		return time.Time{}, fmt.Errorf("period %q: series %s is %s, want %s", period, s.ID, s.Frequency, want)
	}
	if date.After(model.Day(now)) {
		return time.Time{}, fmt.Errorf("period %s is in the future", period)
	}
	return date, nil
}

// Manual stores a value typed by the user (spec 6.1: `macroctl input`) for
// a series of the manual section: source "manual", written like any other
// observation through the store's writer, a changed value kept as a
// revision. The fetch_state row "manual:<id>" records the last input.
func Manual(ctx context.Context, cfg *config.Config, st *store.Store, id string, value float64, period string, now time.Time) (ManualResult, error) {
	s, ok := cfg.ManualSeries(id)
	if !ok {
		var ids []string
		for _, m := range cfg.Manual.Series {
			ids = append(ids, m.ID)
		}
		return ManualResult{}, fmt.Errorf("series %q is not in manual.series of the config (have: %s)", id, strings.Join(ids, ", "))
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return ManualResult{}, fmt.Errorf("value %v is not a number", value)
	}
	date, err := ManualPeriod(s, period, now)
	if err != nil {
		return ManualResult{}, err
	}
	res := ManualResult{Series: s, Date: date}
	before, err := st.Observations(ctx, config.SourceManual, s.ID, date)
	if err != nil {
		return res, err
	}
	if len(before) > 0 && before[0].Date.Equal(date) {
		res.Old = before[0].Value
	}
	err = st.Write(ctx, func(tx *sql.Tx) error {
		if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{
			Source: config.SourceManual, SeriesID: s.ID, Label: s.Label, Agent: s.Agent, Frequency: s.Frequency, Units: s.Units,
		}); err != nil {
			return err
		}
		up, err := store.UpsertObservations(tx, config.SourceManual, s.ID, []model.Observation{{Date: date, Value: value}}, now)
		if err != nil {
			return err
		}
		res.UpsertResult = up
		return store.SetFetchOK(tx, config.SourceManual+":"+s.ID, date.Format(time.DateOnly), now)
	})
	if err != nil {
		return res, fmt.Errorf("manual store %s: %w", s.ID, err)
	}
	return res, nil
}
