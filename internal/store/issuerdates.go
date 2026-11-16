package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// IssuerReportDate is a row of issuer_report_dates: a report date read
// from an issuer's financial calendar page (phase 14c).
type IssuerReportDate struct {
	Ticker    string
	Period    string
	Date      time.Time
	Title     string
	URL       string
	FetchedAt time.Time
	ChangedAt time.Time
}

// Moved is a period whose date changed on the page.
type Moved struct {
	Period   string
	Old, New time.Time
}

// UpsertIssuerReportDates stores the dates of one page read: new periods
// are added, a changed date replaces the old one (returned in moved),
// unchanged ones only refresh fetched_at.
func UpsertIssuerReportDates(tx *sql.Tx, ds []IssuerReportDate, now time.Time) (added int, moved []Moved, err error) {
	for _, d := range ds {
		var old string
		err := tx.QueryRow(`SELECT date FROM issuer_report_dates WHERE ticker = ? AND period = ?`, d.Ticker, d.Period).Scan(&old)
		switch {
		case err == sql.ErrNoRows:
			if _, err := tx.Exec(`INSERT INTO issuer_report_dates (ticker, period, date, title, url, fetched_at, changed_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				d.Ticker, d.Period, formatDate(d.Date), d.Title, d.URL, formatTS(now), formatTS(now)); err != nil {
				return added, moved, fmt.Errorf("issuer report date %s %s: %w", d.Ticker, d.Period, err)
			}
			added++
			continue
		case err != nil:
			return added, moved, fmt.Errorf("issuer report date %s %s: %w", d.Ticker, d.Period, err)
		}
		prev, err := parseDate(old)
		if err != nil {
			return added, moved, fmt.Errorf("issuer report date %s %s: %w", d.Ticker, d.Period, err)
		}
		q := `UPDATE issuer_report_dates SET title = ?, url = ?, fetched_at = ? WHERE ticker = ? AND period = ?`
		args := []any{d.Title, d.URL, formatTS(now), d.Ticker, d.Period}
		if !prev.Equal(d.Date) {
			q = `UPDATE issuer_report_dates SET date = ?, changed_at = ?, title = ?, url = ?, fetched_at = ? WHERE ticker = ? AND period = ?`
			args = append([]any{formatDate(d.Date), formatTS(now)}, args...)
			moved = append(moved, Moved{Period: d.Period, Old: prev, New: d.Date})
		}
		if _, err := tx.Exec(q, args...); err != nil {
			return added, moved, fmt.Errorf("issuer report date %s %s: %w", d.Ticker, d.Period, err)
		}
	}
	return added, moved, nil
}

// IssuerReportDates are the stored calendar dates of ticker, by date.
func (s *Store) IssuerReportDates(ctx context.Context, ticker string) ([]IssuerReportDate, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT ticker, period, date, title, url, fetched_at, changed_at FROM issuer_report_dates
		WHERE ticker = ? ORDER BY date, period`, ticker)
	if err != nil {
		return nil, fmt.Errorf("issuer report dates %s: %w", ticker, err)
	}
	defer rows.Close()
	var out []IssuerReportDate
	for rows.Next() {
		var d IssuerReportDate
		var date, fetched, changed string
		if err := rows.Scan(&d.Ticker, &d.Period, &date, &d.Title, &d.URL, &fetched, &changed); err != nil {
			return nil, fmt.Errorf("issuer report dates %s: %w", ticker, err)
		}
		if d.Date, err = parseDate(date); err != nil {
			return nil, fmt.Errorf("issuer report dates %s: %w", ticker, err)
		}
		if d.FetchedAt, err = parseTS(fetched); err != nil {
			return nil, err
		}
		if d.ChangedAt, err = parseTS(changed); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
