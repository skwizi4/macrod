package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// IntradayBar is one stored one-minute bar (FIXES_W44 F1).
type IntradayBar struct {
	TS    time.Time // bar start, UTC
	Close float64
}

// UpsertIntradayBars writes bars of symbol inside tx (use it from
// Store.Write) and deletes the bars of every symbol older than keepFrom.
func UpsertIntradayBars(tx *sql.Tx, symbol string, bars []IntradayBar, keepFrom time.Time) error {
	st, err := tx.Prepare(`INSERT INTO intraday_bars (symbol, ts, close) VALUES (?, ?, ?)
		ON CONFLICT (symbol, ts) DO UPDATE SET close = excluded.close`)
	if err != nil {
		return fmt.Errorf("intraday %s prepare: %w", symbol, err)
	}
	defer st.Close()
	for _, b := range bars {
		if _, err := st.Exec(symbol, barTS(b.TS), b.Close); err != nil {
			return fmt.Errorf("intraday %s %s: %w", symbol, barTS(b.TS), err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM intraday_bars WHERE ts < ?`, barTS(keepFrom)); err != nil {
		return fmt.Errorf("intraday prune: %w", err)
	}
	return nil
}

// IntradayBars returns the bars of symbol in [from, to], oldest first.
func (s *Store) IntradayBars(ctx context.Context, symbol string, from, to time.Time) ([]IntradayBar, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT ts, close FROM intraday_bars WHERE symbol = ? AND ts >= ? AND ts <= ? ORDER BY ts`,
		symbol, barTS(from), barTS(to))
	if err != nil {
		return nil, fmt.Errorf("intraday %s: %w", symbol, err)
	}
	defer rows.Close()
	var out []IntradayBar
	for rows.Next() {
		var b IntradayBar
		if err := rows.Scan(&b.TS, &b.Close); err != nil {
			return nil, fmt.Errorf("intraday %s scan: %w", symbol, err)
		}
		b.TS = b.TS.UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

// barTS formats bar times in fixed RFC3339 (whole seconds), so string
// comparisons in ranges follow time order (as releases.scheduled_at).
func barTS(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }
