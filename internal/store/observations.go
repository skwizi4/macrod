package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"macro-collector/internal/model"
)

// UpsertResult counts what UpsertObservations did.
type UpsertResult struct {
	Inserted  int
	Revised   int
	Unchanged int
}

// UpsertObservations writes obs for one series inside tx (use it from
// Store.Write). New dates are inserted; a changed value overwrites the old
// one, bumps fetched_at and is logged in revisions (§17.3); an unchanged
// value is left alone, so fetched_at keeps the time it was first seen.
func UpsertObservations(tx *sql.Tx, source, seriesID string, obs []model.Observation, now time.Time) (UpsertResult, error) {
	var res UpsertResult
	sel, err := tx.Prepare(`SELECT value FROM observations WHERE source = ? AND series_id = ? AND obs_date = ?`)
	if err != nil {
		return res, fmt.Errorf("upsert %s/%s prepare: %w", source, seriesID, err)
	}
	defer sel.Close()
	ins, err := tx.Prepare(`INSERT INTO observations (source, series_id, obs_date, value, fetched_at, first_fetched_at) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return res, fmt.Errorf("upsert %s/%s prepare: %w", source, seriesID, err)
	}
	defer ins.Close()
	upd, err := tx.Prepare(`UPDATE observations SET value = ?, fetched_at = ? WHERE source = ? AND series_id = ? AND obs_date = ?`)
	if err != nil {
		return res, fmt.Errorf("upsert %s/%s prepare: %w", source, seriesID, err)
	}
	defer upd.Close()
	rev, err := tx.Prepare(`INSERT INTO revisions (source, series_id, obs_date, old_value, new_value, detected_at) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return res, fmt.Errorf("upsert %s/%s prepare: %w", source, seriesID, err)
	}
	defer rev.Close()

	ts := formatTS(now)
	for _, o := range obs {
		date := formatDate(o.Date)
		var old float64
		err := sel.QueryRow(source, seriesID, date).Scan(&old)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := ins.Exec(source, seriesID, date, o.Value, ts, ts); err != nil {
				return res, fmt.Errorf("upsert %s/%s %s insert: %w", source, seriesID, date, err)
			}
			res.Inserted++
		case err != nil:
			return res, fmt.Errorf("upsert %s/%s %s select: %w", source, seriesID, date, err)
		case old == o.Value:
			res.Unchanged++
		default:
			if _, err := upd.Exec(o.Value, ts, source, seriesID, date); err != nil {
				return res, fmt.Errorf("upsert %s/%s %s update: %w", source, seriesID, date, err)
			}
			if _, err := rev.Exec(source, seriesID, date, old, o.Value, ts); err != nil {
				return res, fmt.Errorf("upsert %s/%s %s revision: %w", source, seriesID, date, err)
			}
			res.Revised++
		}
	}
	return res, nil
}

// Observations returns a series from since (inclusive) in date order.
func (s *Store) Observations(ctx context.Context, source, seriesID string, since time.Time) ([]model.Observation, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT obs_date, value FROM observations
		 WHERE source = ? AND series_id = ? AND obs_date >= ? AND value IS NOT NULL
		 ORDER BY obs_date`,
		source, seriesID, formatDate(since))
	if err != nil {
		return nil, fmt.Errorf("observations %s/%s: %w", source, seriesID, err)
	}
	defer rows.Close()
	var out []model.Observation
	for rows.Next() {
		// The driver parses DATE-typed columns into time.Time (UTC).
		var o model.Observation
		if err := rows.Scan(&o.Date, &o.Value); err != nil {
			return nil, fmt.Errorf("observations %s/%s scan: %w", source, seriesID, err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
