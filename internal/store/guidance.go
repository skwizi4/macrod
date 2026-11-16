package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Guidance candidate statuses (phase 14b, ТЗ §21.6 п. 3).
const (
	GuidancePending  = "pending"
	GuidanceApproved = "approved"
	GuidanceRejected = "rejected"
)

// ErrDecided is returned when a candidate is approved or rejected twice.
var ErrDecided = errors.New("guidance candidate already decided")

// ErrNoCandidate is returned for an unknown candidate id.
var ErrNoCandidate = errors.New("no such guidance candidate")

// GuidanceCandidate is a row of guidance_candidates: a sentence with a
// capex amount and a year, unused until approved.
type GuidanceCandidate struct {
	ID          int64
	Ticker      string
	Year        int
	Low, High   float64 // billions of dollars
	Quote       string
	URL         string
	DocSource   string
	DocRef      string
	DocTitle    string
	Given       time.Time // date of the document (New York)
	Note        string    // differs from / confirms the approved value
	Status      string
	FoundAt     time.Time
	DecidedAt   time.Time // zero while pending
	DecidedVia  string    // cli | telegram
	TGMessageID int64     // 0: no Telegram message
}

// Source is how an approved candidate is cited in the export.
func (c GuidanceCandidate) Source() string {
	return fmt.Sprintf("%s, %s", c.DocTitle, c.Given.Format(dateLayout))
}

// ConfirmedGuidance is a row of capex_guidance: an approved candidate.
type ConfirmedGuidance struct {
	ID          int64
	Ticker      string
	Year        int
	Low, High   float64
	Given       time.Time
	Source      string
	CandidateID int64
	ApprovedAt  time.Time
}

// InsertGuidanceCandidate adds a pending candidate; the same range from the
// same document is kept once (inserted false, id of the stored row).
func InsertGuidanceCandidate(tx *sql.Tx, c GuidanceCandidate, now time.Time) (id int64, inserted bool, err error) {
	res, err := tx.Exec(`INSERT INTO guidance_candidates (ticker, year, low, high, quote, url, doc_source, doc_ref, doc_title, given, note, found_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (ticker, year, low, high, doc_source, doc_ref) DO NOTHING`,
		c.Ticker, c.Year, c.Low, c.High, c.Quote, c.URL, c.DocSource, c.DocRef, c.DocTitle, formatDate(c.Given), nullText(c.Note), formatTS(now))
	if err != nil {
		return 0, false, fmt.Errorf("guidance candidate %s %d: %w", c.Ticker, c.Year, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		id, err = res.LastInsertId()
		return id, true, err
	}
	err = tx.QueryRow(`SELECT id FROM guidance_candidates WHERE ticker = ? AND year = ? AND low = ? AND high = ? AND doc_source = ? AND doc_ref = ?`,
		c.Ticker, c.Year, c.Low, c.High, c.DocSource, c.DocRef).Scan(&id)
	if err != nil {
		return 0, false, fmt.Errorf("guidance candidate %s %d: %w", c.Ticker, c.Year, err)
	}
	return id, false, nil
}

// SetGuidanceMessage records the Telegram message of a candidate.
func SetGuidanceMessage(tx *sql.Tx, id, messageID int64) error {
	if _, err := tx.Exec(`UPDATE guidance_candidates SET tg_message_id = ? WHERE id = ?`, messageID, id); err != nil {
		return fmt.Errorf("guidance candidate %d message: %w", id, err)
	}
	return nil
}

// DecideGuidanceCandidate approves or rejects a pending candidate; an
// approval adds its range to capex_guidance in the same transaction. It
// returns the candidate as decided, or ErrDecided with the stored one.
func DecideGuidanceCandidate(tx *sql.Tx, id int64, approve bool, via string, now time.Time) (GuidanceCandidate, error) {
	c, err := scanCandidate(tx.QueryRow(candidateSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("guidance candidate %d: %w", id, ErrNoCandidate)
	}
	if err != nil {
		return c, fmt.Errorf("guidance candidate %d: %w", id, err)
	}
	if c.Status != GuidancePending {
		return c, fmt.Errorf("guidance candidate %d (%s): %w", id, c.Status, ErrDecided)
	}
	c.Status, c.DecidedAt, c.DecidedVia = GuidanceRejected, now, via
	if approve {
		c.Status = GuidanceApproved
	}
	if _, err := tx.Exec(`UPDATE guidance_candidates SET status = ?, decided_at = ?, decided_via = ? WHERE id = ?`,
		c.Status, formatTS(now), via, id); err != nil {
		return c, fmt.Errorf("guidance candidate %d decide: %w", id, err)
	}
	if approve {
		if _, err := tx.Exec(`INSERT INTO capex_guidance (ticker, year, low, high, given, source, candidate_id, approved_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.Ticker, c.Year, c.Low, c.High, formatDate(c.Given), c.Source(), id, formatTS(now)); err != nil {
			return c, fmt.Errorf("capex guidance from candidate %d: %w", id, err)
		}
	}
	return c, nil
}

// GuidanceCandidates lists candidates with the given statuses (all when
// none), newest document first.
func (s *Store) GuidanceCandidates(ctx context.Context, statuses ...string) ([]GuidanceCandidate, error) {
	q, args := candidateSelect, []any{}
	if len(statuses) > 0 {
		q += ` WHERE status IN (?` + repeatComma(len(statuses)-1) + `)`
		for _, st := range statuses {
			args = append(args, st)
		}
	}
	rows, err := s.read.QueryContext(ctx, q+` ORDER BY given DESC, id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("guidance candidates: %w", err)
	}
	defer rows.Close()
	var out []GuidanceCandidate
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, fmt.Errorf("guidance candidates: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GuidanceCandidate reads one candidate.
func (s *Store) GuidanceCandidate(ctx context.Context, id int64) (GuidanceCandidate, bool, error) {
	c, err := scanCandidate(s.read.QueryRowContext(ctx, candidateSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, fmt.Errorf("guidance candidate %d: %w", id, err)
	}
	return c, true, nil
}

// ConfirmedGuidance lists the approved ranges, oldest approval first.
func (s *Store) ConfirmedGuidance(ctx context.Context) ([]ConfirmedGuidance, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id, ticker, year, low, high, given, source, candidate_id, approved_at FROM capex_guidance ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("capex guidance: %w", err)
	}
	defer rows.Close()
	var out []ConfirmedGuidance
	for rows.Next() {
		var g ConfirmedGuidance
		var given, at string
		if err := rows.Scan(&g.ID, &g.Ticker, &g.Year, &g.Low, &g.High, &given, &g.Source, &g.CandidateID, &at); err != nil {
			return nil, fmt.Errorf("capex guidance: %w", err)
		}
		if g.Given, err = parseDate(given); err != nil {
			return nil, fmt.Errorf("capex guidance %d: %w", g.ID, err)
		}
		if g.ApprovedAt, err = parseTS(at); err != nil {
			return nil, fmt.Errorf("capex guidance %d: %w", g.ID, err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

const candidateSelect = `SELECT id, ticker, year, low, high, quote, url, doc_source, doc_ref, doc_title, given, note, status,
	found_at, decided_at, decided_via, tg_message_id FROM guidance_candidates`

func scanCandidate(row interface{ Scan(...any) error }) (GuidanceCandidate, error) {
	var c GuidanceCandidate
	var given, found string
	var note, decided, via sql.NullString
	var msg sql.NullInt64
	if err := row.Scan(&c.ID, &c.Ticker, &c.Year, &c.Low, &c.High, &c.Quote, &c.URL, &c.DocSource, &c.DocRef, &c.DocTitle,
		&given, &note, &c.Status, &found, &decided, &via, &msg); err != nil {
		return c, err
	}
	var err error
	if c.Given, err = parseDate(given); err != nil {
		return c, err
	}
	if c.FoundAt, err = parseTS(found); err != nil {
		return c, err
	}
	if decided.Valid {
		if c.DecidedAt, err = parseTS(decided.String); err != nil {
			return c, err
		}
	}
	c.Note, c.DecidedVia, c.TGMessageID = note.String, via.String, msg.Int64
	return c, nil
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func repeatComma(n int) string {
	out := ""
	for range n {
		out += ", ?"
	}
	return out
}
