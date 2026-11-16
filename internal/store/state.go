package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// SeriesMeta is a row of series_meta.
type SeriesMeta struct {
	Source    string
	SeriesID  string
	Label     string
	Agent     string
	Frequency string
	Units     string
	// Event and Question group prediction markets by question (0009);
	// empty elsewhere.
	Event, Question string
}

// UpsertSeriesMeta inserts or replaces a series_meta row (release_id is kept).
func UpsertSeriesMeta(tx *sql.Tx, m SeriesMeta) error {
	_, err := tx.Exec(`
		INSERT INTO series_meta (source, series_id, label, agent, frequency, units, event, question)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, series_id) DO UPDATE SET
			label = excluded.label, agent = excluded.agent,
			frequency = excluded.frequency, units = excluded.units,
			event = excluded.event, question = excluded.question`,
		m.Source, m.SeriesID, m.Label, m.Agent, m.Frequency, m.Units, nullString(m.Event), nullString(m.Question))
	if err != nil {
		return fmt.Errorf("series_meta %s/%s: %w", m.Source, m.SeriesID, err)
	}
	return nil
}

// SetSeriesRelease records the FRED release of a series. The row is created
// with label and agent if the series was never fetched.
func SetSeriesRelease(tx *sql.Tx, source, seriesID, label, agent string, releaseID int) error {
	_, err := tx.Exec(`
		INSERT INTO series_meta (source, series_id, label, agent, release_id) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (source, series_id) DO UPDATE SET release_id = excluded.release_id`,
		source, seriesID, label, agent, strconv.Itoa(releaseID))
	if err != nil {
		return fmt.Errorf("series_meta release %s/%s: %w", source, seriesID, err)
	}
	return nil
}

// SeriesReleases maps series_id to its release id for the series of source
// that have one.
func (s *Store) SeriesReleases(ctx context.Context, source string) (map[string]int, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT series_id, release_id FROM series_meta WHERE source = ? AND release_id IS NOT NULL`, source)
	if err != nil {
		return nil, fmt.Errorf("series releases %s: %w", source, err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var id, rel string
		if err := rows.Scan(&id, &rel); err != nil {
			return nil, fmt.Errorf("series releases %s scan: %w", source, err)
		}
		n, err := strconv.Atoi(rel)
		if err != nil {
			return nil, fmt.Errorf("series releases %s/%s release_id %q: %w", source, id, rel, err)
		}
		out[id] = n
	}
	return out, rows.Err()
}

// SeriesFrequencies maps series_id to series_meta.frequency for source.
func (s *Store) SeriesFrequencies(ctx context.Context, source string) (map[string]string, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT series_id, frequency FROM series_meta WHERE source = ? AND frequency IS NOT NULL`, source)
	if err != nil {
		return nil, fmt.Errorf("series frequencies %s: %w", source, err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var id, f string
		if err := rows.Scan(&id, &f); err != nil {
			return nil, fmt.Errorf("series frequencies %s scan: %w", source, err)
		}
		out[id] = f
	}
	return out, rows.Err()
}

// SeriesQuestion is the question of a prediction market (series_meta 0009).
type SeriesQuestion struct{ Label, Event, Question string }

// SeriesQuestions maps series_id to its label, event and question for
// source; event and question are empty for rows stored before 0009.
func (s *Store) SeriesQuestions(ctx context.Context, source string) (map[string]SeriesQuestion, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT series_id, label, coalesce(event, ''), coalesce(question, '') FROM series_meta WHERE source = ?`, source)
	if err != nil {
		return nil, fmt.Errorf("series questions %s: %w", source, err)
	}
	defer rows.Close()
	out := make(map[string]SeriesQuestion)
	for rows.Next() {
		var id string
		var q SeriesQuestion
		if err := rows.Scan(&id, &q.Label, &q.Event, &q.Question); err != nil {
			return nil, fmt.Errorf("series questions %s scan: %w", source, err)
		}
		out[id] = q
	}
	return out, rows.Err()
}

// SeriesUnits returns series_meta.units; ok is false if the series is unknown.
func (s *Store) SeriesUnits(ctx context.Context, source, seriesID string) (u string, ok bool, err error) {
	var v sql.NullString
	err = s.read.QueryRowContext(ctx,
		`SELECT units FROM series_meta WHERE source = ? AND series_id = ?`, source, seriesID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("series units %s/%s: %w", source, seriesID, err)
	}
	return v.String, true, nil
}

// SetFetchOK records a successful fetch: stores the cursor, sets updated_at
// and clears the last error.
func SetFetchOK(tx *sql.Tx, source, cursor string, now time.Time) error {
	_, err := tx.Exec(`
		INSERT INTO fetch_state (source, cursor, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (source) DO UPDATE SET
			cursor = excluded.cursor, updated_at = excluded.updated_at,
			last_error = NULL, last_error_at = NULL`,
		source, cursor, formatTS(now))
	if err != nil {
		return fmt.Errorf("fetch_state ok %s: %w", source, err)
	}
	return nil
}

// SetFetchError records a failed fetch. Cursor and updated_at keep the last
// success; for a source that never succeeded the row has a NULL cursor and
// updated_at is the first failure.
func SetFetchError(tx *sql.Tx, source string, fetchErr error, now time.Time) error {
	ts := formatTS(now)
	_, err := tx.Exec(`
		INSERT INTO fetch_state (source, updated_at, last_error, last_error_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (source) DO UPDATE SET
			last_error = excluded.last_error, last_error_at = excluded.last_error_at`,
		source, ts, fetchErr.Error(), ts)
	if err != nil {
		return fmt.Errorf("fetch_state error %s: %w", source, err)
	}
	return nil
}

// FetchState is a row of fetch_state.
type FetchState struct {
	Source      string
	Cursor      string // empty if the source never succeeded
	UpdatedAt   time.Time
	LastError   string
	LastErrorAt time.Time // zero if no error
}

// FetchStateOf returns the state of source; ok is false if there is none.
func (s *Store) FetchStateOf(ctx context.Context, source string) (st FetchState, ok bool, err error) {
	st, err = scanFetchState(s.read.QueryRowContext(ctx,
		`SELECT `+fetchStateColumns+` FROM fetch_state WHERE source = ?`, source))
	if errors.Is(err, sql.ErrNoRows) {
		return FetchState{}, false, nil
	}
	if err != nil {
		return FetchState{}, false, fmt.Errorf("fetch_state %s: %w", source, err)
	}
	return st, true, nil
}

const fetchStateColumns = `source, cursor, updated_at, last_error, last_error_at`

func scanFetchState(row interface{ Scan(...any) error }) (FetchState, error) {
	var st FetchState
	var cursor, lastErr sql.NullString
	var errAt sql.NullTime
	if err := row.Scan(&st.Source, &cursor, &st.UpdatedAt, &lastErr, &errAt); err != nil {
		return FetchState{}, err
	}
	st.Cursor, st.LastError, st.LastErrorAt = cursor.String, lastErr.String, errAt.Time
	return st, nil
}

// LastObsDate returns the latest obs_date of a series; ok is false if the
// series has no observations.
func (s *Store) LastObsDate(ctx context.Context, source, seriesID string) (date time.Time, ok bool, err error) {
	// max() drops the column's DATE type, so the driver returns plain text.
	var d sql.NullString
	err = s.read.QueryRowContext(ctx,
		`SELECT max(obs_date) FROM observations WHERE source = ? AND series_id = ?`, source, seriesID).Scan(&d)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("last obs %s/%s: %w", source, seriesID, err)
	}
	if !d.Valid {
		return time.Time{}, false, nil
	}
	date, err = time.Parse(dateLayout, d.String)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("last obs %s/%s date %q: %w", source, seriesID, d.String, err)
	}
	return date, true, nil
}

// LastChange is when a row of the series last changed (max fetched_at: an
// insert or a revision); ok is false if the series has no observations.
func (s *Store) LastChange(ctx context.Context, source, seriesID string) (t time.Time, ok bool, err error) {
	var v sql.NullString
	if err := s.read.QueryRowContext(ctx,
		`SELECT max(fetched_at) FROM observations WHERE source = ? AND series_id = ?`, source, seriesID).Scan(&v); err != nil {
		return time.Time{}, false, fmt.Errorf("last change %s/%s: %w", source, seriesID, err)
	}
	if !v.Valid {
		return time.Time{}, false, nil
	}
	// Aggregates lose the column type: the driver returns text.
	if t, err = parseTS(v.String); err != nil {
		return time.Time{}, false, fmt.Errorf("last change %s/%s: %w", source, seriesID, err)
	}
	return t, true, nil
}
