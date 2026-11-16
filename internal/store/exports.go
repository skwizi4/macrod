package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// InsertExport records an export run.
func InsertExport(tx *sql.Tx, isoWeek, kind, region string, now time.Time) error {
	_, err := tx.Exec(`INSERT INTO exports (iso_week, kind, region, created_at) VALUES (?, ?, ?, ?)`,
		isoWeek, kind, region, formatTS(now))
	if err != nil {
		return fmt.Errorf("export %s %s %s: %w", isoWeek, kind, region, err)
	}
	return nil
}

// LastExport returns created_at of the latest export of kind and region;
// ok is false if there was none.
func (s *Store) LastExport(ctx context.Context, kind, region string) (t time.Time, ok bool, err error) {
	err = s.read.QueryRowContext(ctx,
		`SELECT created_at FROM exports WHERE kind = ? AND region = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
		kind, region).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("last export %s %s: %w", kind, region, err)
	}
	return t, true, nil
}
