package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"macro-collector/internal/model"
)

// Revision is a row of revisions (§17.3).
type Revision struct {
	ObsDate    time.Time
	Old, New   float64
	DetectedAt time.Time
}

// Revisions returns the revisions of a series detected in [from, to), by
// detection time.
func (s *Store) Revisions(ctx context.Context, source, seriesID string, from, to time.Time) ([]Revision, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT obs_date, old_value, new_value, detected_at FROM revisions
		WHERE source = ? AND series_id = ? AND detected_at >= ? AND detected_at < ?
		ORDER BY detected_at, obs_date`, source, seriesID, formatTS(from), formatTS(to))
	if err != nil {
		return nil, fmt.Errorf("revisions %s/%s: %w", source, seriesID, err)
	}
	defer rows.Close()
	var out []Revision
	for rows.Next() {
		var r Revision
		if err := rows.Scan(&r.ObsDate, &r.Old, &r.New, &r.DetectedAt); err != nil {
			return nil, fmt.Errorf("revisions %s/%s scan: %w", source, seriesID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InitialLoad bounds the first load of a series (backfill, or the first
// fetch of a new series): rows stored within it of the series' first row
// are its history, not releases.
const InitialLoad = 10 * time.Minute

// KnownObservation is an observation as it was known at some moment.
type KnownObservation struct {
	model.Observation
	// Seen is when the value became known: the first fetch, or the last
	// revision up to that moment.
	Seen time.Time
	// Initial: the value comes from the series' initial history load,
	// unrevised up to that moment.
	Initial bool
}

// ObservationsAsOf returns a series from since (inclusive) in date order
// as it was known at knownAt: rows first fetched later are left out, and a
// value revised later is shown as it was before. The initial history load
// carries no publication times, so its rows count as known at any moment
// (a past week before the load still sees them by obs_date).
func (s *Store) ObservationsAsOf(ctx context.Context, source, seriesID string, since, knownAt time.Time) ([]KnownObservation, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT obs_date, value, fetched_at, first_fetched_at FROM observations
		 WHERE source = ? AND series_id = ? AND obs_date >= ? AND value IS NOT NULL
		 ORDER BY obs_date`,
		source, seriesID, formatDate(since))
	if err != nil {
		return nil, fmt.Errorf("observations as of %s/%s: %w", source, seriesID, err)
	}
	type row struct {
		o              model.Observation
		fetched, first time.Time
	}
	var all []row
	for rows.Next() {
		var r row
		var first sql.NullTime
		if err := rows.Scan(&r.o.Date, &r.o.Value, &r.fetched, &first); err != nil {
			rows.Close()
			return nil, fmt.Errorf("observations as of %s/%s scan: %w", source, seriesID, err)
		}
		r.first = r.fetched // written by a binary before migration 0005
		if first.Valid {
			r.first = first.Time
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("observations as of %s/%s: %w", source, seriesID, err)
	}
	if len(all) == 0 {
		return nil, nil
	}

	// The series' first row may precede since; ask for it separately.
	var seriesFirst time.Time
	{
		var v sql.NullString
		if err := s.read.QueryRowContext(ctx,
			`SELECT min(coalesce(first_fetched_at, fetched_at)) FROM observations WHERE source = ? AND series_id = ?`,
			source, seriesID).Scan(&v); err != nil {
			return nil, fmt.Errorf("observations as of %s/%s first fetch: %w", source, seriesID, err)
		}
		// Aggregates lose the column type: the driver returns text.
		if seriesFirst, err = parseTS(v.String); err != nil {
			return nil, fmt.Errorf("observations as of %s/%s first fetch: %w", source, seriesID, err)
		}
	}

	// Revisions by date: the earliest one after knownAt holds the value
	// known then; the latest one up to knownAt is when it became known.
	revs, err := s.read.QueryContext(ctx,
		`SELECT obs_date, old_value, detected_at FROM revisions
		 WHERE source = ? AND series_id = ? AND obs_date >= ?
		 ORDER BY detected_at, id`,
		source, seriesID, formatDate(since))
	if err != nil {
		return nil, fmt.Errorf("observations as of %s/%s revisions: %w", source, seriesID, err)
	}
	before := map[string]float64{}      // by date: value before the first revision after knownAt
	revisedAt := map[string]time.Time{} // by date: last revision up to knownAt
	for revs.Next() {
		var d, at time.Time
		var old sql.NullFloat64
		if err := revs.Scan(&d, &old, &at); err != nil {
			revs.Close()
			return nil, fmt.Errorf("observations as of %s/%s revisions scan: %w", source, seriesID, err)
		}
		key := formatDate(d)
		if !at.After(knownAt) {
			revisedAt[key] = at
			continue
		}
		if _, ok := before[key]; !ok && old.Valid {
			before[key] = old.Float64
		}
	}
	revs.Close()
	if err := revs.Err(); err != nil {
		return nil, fmt.Errorf("observations as of %s/%s revisions: %w", source, seriesID, err)
	}

	out := make([]KnownObservation, 0, len(all))
	for _, r := range all {
		initial := r.first.Sub(seriesFirst) < InitialLoad
		if r.first.After(knownAt) && !initial {
			continue
		}
		k := KnownObservation{Observation: r.o, Seen: r.first, Initial: initial}
		if v, ok := before[formatDate(r.o.Date)]; ok {
			k.Value = v
		}
		if at, ok := revisedAt[formatDate(r.o.Date)]; ok {
			k.Seen, k.Initial = at, false
		}
		out = append(out, k)
	}
	return out, nil
}

// Meta returns the series_meta row of a series; ok is false if absent.
func (s *Store) Meta(ctx context.Context, source, seriesID string) (SeriesMeta, bool, error) {
	m := SeriesMeta{Source: source, SeriesID: seriesID}
	var freq, units sql.NullString
	err := s.read.QueryRowContext(ctx, `SELECT label, agent, frequency, units FROM series_meta WHERE source = ? AND series_id = ?`,
		source, seriesID).Scan(&m.Label, &m.Agent, &freq, &units)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	if err != nil {
		return m, false, fmt.Errorf("series meta %s/%s: %w", source, seriesID, err)
	}
	m.Frequency, m.Units = freq.String, units.String
	return m, true, nil
}

// DocumentsBetween returns the documents of source published in [from, to),
// oldest first.
func (s *Store) DocumentsBetween(ctx context.Context, source string, from, to time.Time) ([]model.Document, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+documentColumns+` FROM documents
		WHERE source = ? AND published_at >= ? AND published_at < ? ORDER BY published_at, id`,
		source, formatTS(from), formatTS(to))
	if err != nil {
		return nil, fmt.Errorf("documents %s: %w", source, err)
	}
	defer rows.Close()
	var out []model.Document
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("documents %s scan: %w", source, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
