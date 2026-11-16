package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"macro-collector/internal/model"
)

// scheduled_at is stored with whole seconds in a fixed-width layout, so
// string comparison in range queries matches time order.
func formatSched(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }

// formatSchedBound formats a range bound, rounding up to a whole second: as
// events have whole seconds, ">= bound" and "< bound" then keep their meaning.
func formatSchedBound(t time.Time) string {
	if r := t.Truncate(time.Second); !r.Equal(t) {
		t = r.Add(time.Second)
	}
	return formatSched(t)
}

// SyncResult counts what SyncReleases did.
type SyncResult struct {
	Inserted    int
	Updated     int
	Rescheduled int
}

// SyncReleases stores the current schedule of one event series (source +
// name) for the window [from, to). New events are inserted, known ones get
// fresh region/importance/related, and a scheduled event in the window that
// the source no longer lists is marked rescheduled (ТЗ §17.6). Events that
// are polling, done or timed out are never touched by the sync.
func SyncReleases(tx *sql.Tx, source, name string, from, to time.Time, events []model.ReleaseEvent) (SyncResult, error) {
	var res SyncResult
	keep := make([]any, 0, len(events))
	for _, e := range events {
		if e.Source != source || e.Name != name {
			return res, fmt.Errorf("sync releases %s/%s: got event %s/%s", source, name, e.Source, e.Name)
		}
		related, err := json.Marshal(e.Related)
		if err != nil {
			return res, fmt.Errorf("sync releases %s/%s related: %w", source, name, err)
		}
		at := formatSched(e.ScheduledAt)
		keep = append(keep, at)
		var status string
		err = tx.QueryRow(`SELECT status FROM releases WHERE source = ? AND name = ? AND scheduled_at = ?`,
			source, name, at).Scan(&status)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.Exec(`
				INSERT INTO releases (source, name, scheduled_at, region, importance, related, status)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				source, name, at, e.Region, nullString(e.Importance), string(related), model.StatusScheduled)
			res.Inserted++
		case err != nil:
		default:
			if status == model.StatusRescheduled {
				status = model.StatusScheduled // the source lists the date again
			}
			_, err = tx.Exec(`
				UPDATE releases SET region = ?, importance = ?, related = ?, status = ?
				WHERE source = ? AND name = ? AND scheduled_at = ?`,
				e.Region, nullString(e.Importance), string(related), status, source, name, at)
			res.Updated++
		}
		if err != nil {
			return res, fmt.Errorf("sync releases %s/%s %s: %w", source, name, at, err)
		}
	}

	q := `UPDATE releases SET status = ?
		WHERE source = ? AND name = ? AND status = ? AND scheduled_at >= ? AND scheduled_at < ?`
	args := []any{model.StatusRescheduled, source, name, model.StatusScheduled, formatSchedBound(from), formatSchedBound(to)}
	if len(keep) > 0 {
		q += ` AND scheduled_at NOT IN (?` + strings.Repeat(", ?", len(keep)-1) + `)`
		args = append(args, keep...)
	}
	r, err := tx.Exec(q, args...)
	if err != nil {
		return res, fmt.Errorf("sync releases %s/%s reschedule: %w", source, name, err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return res, fmt.Errorf("sync releases %s/%s reschedule: %w", source, name, err)
	}
	res.Rescheduled = int(n)
	return res, nil
}

// SetReleaseStatus changes the status of event id; done and timeout also
// record done_at.
func SetReleaseStatus(tx *sql.Tx, id int64, status string, now time.Time) error {
	var doneAt any
	if status == model.StatusDone || status == model.StatusTimeout {
		doneAt = formatTS(now)
	}
	r, err := tx.Exec(`UPDATE releases SET status = ?, done_at = ? WHERE id = ?`, status, doneAt, id)
	if err != nil {
		return fmt.Errorf("release %d status %s: %w", id, status, err)
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("release %d status %s: no such release (%v)", id, status, err)
	}
	return nil
}

// Releases returns all events (any status) scheduled in [from, to), in time order.
func (s *Store) Releases(ctx context.Context, from, to time.Time) ([]model.ReleaseEvent, error) {
	return s.queryReleases(ctx, `scheduled_at >= ? AND scheduled_at < ?`,
		formatSchedBound(from), formatSchedBound(to))
}

// ActiveReleases returns scheduled and polling events with scheduled_at in
// [from, to): the ones a restarted scheduler has to pick up.
func (s *Store) ActiveReleases(ctx context.Context, from, to time.Time) ([]model.ReleaseEvent, error) {
	return s.queryReleases(ctx, `status IN (?, ?) AND scheduled_at >= ? AND scheduled_at < ?`,
		model.StatusScheduled, model.StatusPolling, formatSchedBound(from), formatSchedBound(to))
}

// Release returns event id; ok is false if there is none.
func (s *Store) Release(ctx context.Context, id int64) (e model.ReleaseEvent, ok bool, err error) {
	evs, err := s.queryReleases(ctx, `id = ?`, id)
	if err != nil || len(evs) == 0 {
		return model.ReleaseEvent{}, false, err
	}
	return evs[0], true, nil
}

func (s *Store) queryReleases(ctx context.Context, where string, args ...any) ([]model.ReleaseEvent, error) {
	rows, err := s.read.QueryContext(ctx, `
		SELECT id, source, name, scheduled_at, region, importance, related, status, done_at
		FROM releases WHERE `+where+` ORDER BY scheduled_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("releases: %w", err)
	}
	defer rows.Close()
	var out []model.ReleaseEvent
	for rows.Next() {
		var e model.ReleaseEvent
		var importance, related sql.NullString
		var doneAt sql.NullTime
		if err := rows.Scan(&e.ID, &e.Source, &e.Name, &e.ScheduledAt, &e.Region,
			&importance, &related, &e.Status, &doneAt); err != nil {
			return nil, fmt.Errorf("releases scan: %w", err)
		}
		if related.String != "" {
			if err := json.Unmarshal([]byte(related.String), &e.Related); err != nil {
				return nil, fmt.Errorf("release %d related: %w", e.ID, err)
			}
		}
		e.Importance, e.DoneAt = importance.String, doneAt.Time
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
