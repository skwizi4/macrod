package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"
)

// SeriesOverview is series_meta plus the latest observation date.
type SeriesOverview struct {
	SeriesMeta
	LastObs time.Time // zero if none
}

// SeriesOverviews lists every series in series_meta with its last
// observation on or before asOf (some series publish future dates).
func (s *Store) SeriesOverviews(ctx context.Context, asOf time.Time) ([]SeriesOverview, error) {
	rows, err := s.read.QueryContext(ctx, `
		SELECT m.source, m.series_id, m.label, m.agent, m.frequency, m.units,
		       (SELECT max(o.obs_date) FROM observations o
		         WHERE o.source = m.source AND o.series_id = m.series_id AND o.obs_date <= ?)
		FROM series_meta m ORDER BY m.source, m.series_id`, formatDate(asOf))
	if err != nil {
		return nil, fmt.Errorf("series overview: %w", err)
	}
	defer rows.Close()
	var out []SeriesOverview
	for rows.Next() {
		var o SeriesOverview
		var freq, units, last sql.NullString
		if err := rows.Scan(&o.Source, &o.SeriesID, &o.Label, &o.Agent, &freq, &units, &last); err != nil {
			return nil, fmt.Errorf("series overview scan: %w", err)
		}
		o.Frequency, o.Units = freq.String, units.String
		if last.Valid {
			if o.LastObs, err = time.Parse(dateLayout, last.String); err != nil {
				return nil, fmt.Errorf("series overview %s date %q: %w", o.SeriesID, last.String, err)
			}
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// FetchStates returns every fetch_state row keyed by source.
func (s *Store) FetchStates(ctx context.Context) (map[string]FetchState, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+fetchStateColumns+` FROM fetch_state`)
	if err != nil {
		return nil, fmt.Errorf("fetch states: %w", err)
	}
	defer rows.Close()
	out := make(map[string]FetchState)
	for rows.Next() {
		st, err := scanFetchState(rows)
		if err != nil {
			return nil, fmt.Errorf("fetch states scan: %w", err)
		}
		out[st.Source] = st
	}
	return out, rows.Err()
}

// Size returns the bytes used by the database file and its WAL.
func (s *Store) Size() (int64, error) {
	var total int64
	for _, p := range []string{s.path, s.path + "-wal"} {
		fi, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("store size: %w", err)
		}
		total += fi.Size()
	}
	return total, nil
}

// Path is the database file.
func (s *Store) Path() string { return s.path }
