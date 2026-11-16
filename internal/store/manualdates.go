package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AddManualReleaseDates stores publication days of manual series (phase
// 14c); a day stored before is kept. It returns how many were new.
func AddManualReleaseDates(tx *sql.Tx, series []string, days []time.Time, via string, now time.Time) (int, error) {
	n := 0
	for _, id := range series {
		for _, d := range days {
			res, err := tx.Exec(`INSERT OR IGNORE INTO manual_release_dates (series_id, date, added_at, via) VALUES (?, ?, ?, ?)`,
				id, formatDate(d), formatTS(now), via)
			if err != nil {
				return n, fmt.Errorf("manual release date %s %s: %w", id, formatDate(d), err)
			}
			if k, _ := res.RowsAffected(); k > 0 && id == series[0] {
				n++
			}
		}
	}
	return n, nil
}

// DeleteManualReleaseDates removes publication days of the series; it
// returns how many days were removed (per first series).
func DeleteManualReleaseDates(tx *sql.Tx, series []string, days []time.Time) (int, error) {
	n := 0
	for _, id := range series {
		for _, d := range days {
			res, err := tx.Exec(`DELETE FROM manual_release_dates WHERE series_id = ? AND date = ?`, id, formatDate(d))
			if err != nil {
				return n, fmt.Errorf("manual release date %s %s: %w", id, formatDate(d), err)
			}
			if k, _ := res.RowsAffected(); k > 0 && id == series[0] {
				n++
			}
		}
	}
	return n, nil
}

// ManualReleaseDays are the stored publication days by series id, ascending.
func (s *Store) ManualReleaseDays(ctx context.Context) (map[string][]time.Time, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT series_id, date FROM manual_release_dates ORDER BY series_id, date`)
	if err != nil {
		return nil, fmt.Errorf("manual release dates: %w", err)
	}
	defer rows.Close()
	out := map[string][]time.Time{}
	for rows.Next() {
		var id, date string
		if err := rows.Scan(&id, &date); err != nil {
			return nil, fmt.Errorf("manual release dates: %w", err)
		}
		d, err := parseDate(date)
		if err != nil {
			return nil, fmt.Errorf("manual release dates %s: %w", id, err)
		}
		out[id] = append(out[id], d)
	}
	return out, rows.Err()
}
