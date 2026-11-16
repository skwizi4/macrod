package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Forecast outcomes (ТЗ §7, phase 13).
const (
	OutcomePending     = "pending"
	OutcomeHit         = "hit"
	OutcomeMiss        = "miss"
	OutcomeInvalidated = "invalidated"
)

// ForecastItem is one forecast of a master's report.
type ForecastItem struct {
	Instrument   string
	Direction    string // up | down | range
	Invalidation float64
}

// Forecast is a row of forecasts.
type Forecast struct {
	ID         int64
	Region     string
	ISOWeek    string
	SourceFile string
	ForecastItem
	PriceSeries string // "" while pending
	RefDate     time.Time
	RefPrice    float64
	ResultDate  time.Time
	ResultPrice float64
	ChangePct   float64
	Outcome     string
}

// ForecastScore is the scored part of a row.
type ForecastScore struct {
	PriceSeries string
	RefDate     time.Time
	RefPrice    float64
	ResultDate  time.Time
	ResultPrice float64
	ChangePct   float64
	Outcome     string
}

// ReplaceForecasts makes the forecasts of region and week those of items:
// a row whose forecast changed is reset to pending, an unchanged row keeps
// its score, rows of instruments no longer in items are deleted. It reports
// how many rows were added or changed.
func ReplaceForecasts(tx *sql.Tx, region, week, sourceFile string, items []ForecastItem, now time.Time) (int, error) {
	keep := map[string]bool{}
	changed := 0
	for _, it := range items {
		keep[it.Instrument] = true
		res, err := tx.Exec(`INSERT INTO forecasts (region, iso_week, instrument, direction, invalidation, source_file, imported_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (region, iso_week, instrument) DO UPDATE SET
			  direction = excluded.direction, invalidation = excluded.invalidation,
			  source_file = excluded.source_file, imported_at = excluded.imported_at,
			  price_series = NULL, ref_date = NULL, ref_price = NULL, result_date = NULL,
			  result_price = NULL, change_pct = NULL, outcome = 'pending', scored_at = NULL
			WHERE forecasts.direction <> excluded.direction OR forecasts.invalidation <> excluded.invalidation
			   OR forecasts.source_file <> excluded.source_file`,
			region, week, it.Instrument, it.Direction, it.Invalidation, sourceFile, formatTS(now))
		if err != nil {
			return changed, fmt.Errorf("forecast %s %s %s: %w", region, week, it.Instrument, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed++
		}
	}
	rows, err := tx.Query(`SELECT id, instrument FROM forecasts WHERE region = ? AND iso_week = ?`, region, week)
	if err != nil {
		return changed, fmt.Errorf("forecasts %s %s: %w", region, week, err)
	}
	var drop []int64
	for rows.Next() {
		var id int64
		var inst string
		if err := rows.Scan(&id, &inst); err != nil {
			rows.Close()
			return changed, fmt.Errorf("forecasts %s %s scan: %w", region, week, err)
		}
		if !keep[inst] {
			drop = append(drop, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return changed, fmt.Errorf("forecasts %s %s: %w", region, week, err)
	}
	for _, id := range drop {
		if _, err := tx.Exec(`DELETE FROM forecasts WHERE id = ?`, id); err != nil {
			return changed, fmt.Errorf("forecast delete %d: %w", id, err)
		}
		changed++
	}
	return changed, nil
}

// SetForecastScore stores the score of row id.
func SetForecastScore(tx *sql.Tx, id int64, sc ForecastScore, now time.Time) error {
	_, err := tx.Exec(`UPDATE forecasts SET price_series = ?, ref_date = ?, ref_price = ?, result_date = ?,
		result_price = ?, change_pct = ?, outcome = ?, scored_at = ? WHERE id = ?`,
		sc.PriceSeries, formatDate(sc.RefDate), sc.RefPrice, formatDate(sc.ResultDate),
		sc.ResultPrice, sc.ChangePct, sc.Outcome, formatTS(now), id)
	if err != nil {
		return fmt.Errorf("forecast score %d: %w", id, err)
	}
	return nil
}

// Forecasts returns the forecasts of region by week and instrument.
func (s *Store) Forecasts(ctx context.Context, region string) ([]Forecast, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id, region, iso_week, instrument, direction, invalidation, source_file,
		  price_series, ref_date, ref_price, result_date, result_price, change_pct, outcome
		FROM forecasts WHERE region = ? ORDER BY iso_week, instrument`, region)
	if err != nil {
		return nil, fmt.Errorf("forecasts %s: %w", region, err)
	}
	defer rows.Close()
	var out []Forecast
	for rows.Next() {
		var f Forecast
		var series sql.NullString
		var refDate, resDate sql.NullTime
		var ref, res, chg sql.NullFloat64
		if err := rows.Scan(&f.ID, &f.Region, &f.ISOWeek, &f.Instrument, &f.Direction, &f.Invalidation, &f.SourceFile,
			&series, &refDate, &ref, &resDate, &res, &chg, &f.Outcome); err != nil {
			return nil, fmt.Errorf("forecasts %s scan: %w", region, err)
		}
		f.PriceSeries, f.RefDate, f.ResultDate = series.String, refDate.Time, resDate.Time
		f.RefPrice, f.ResultPrice, f.ChangePct = ref.Float64, res.Float64, chg.Float64
		out = append(out, f)
	}
	return out, rows.Err()
}
