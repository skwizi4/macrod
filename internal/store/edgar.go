package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"macro-collector/internal/model"
)

// UpsertCompanyFacts writes XBRL facts of ticker inside tx. A fact is keyed
// by its period and filing, so a restated value is a new row; a re-read of
// the same filing changes nothing. Returns the rows inserted or changed.
func UpsertCompanyFacts(tx *sql.Tx, ticker string, facts []model.CompanyFact, now time.Time) (int, error) {
	changed := 0
	for _, f := range facts {
		start := ""
		if !f.Start.IsZero() {
			start = formatDate(f.Start)
		}
		res, err := tx.Exec(`INSERT INTO company_facts
			(ticker, concept, unit, period_start, period_end, value, accn, form, filed, fy, fp, frame, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (ticker, concept, unit, period_start, period_end, accn) DO UPDATE SET
			  value = excluded.value, form = excluded.form, filed = excluded.filed, fy = excluded.fy,
			  fp = excluded.fp, frame = excluded.frame, fetched_at = excluded.fetched_at
			WHERE company_facts.value != excluded.value`,
			ticker, f.Concept, f.Unit, start, formatDate(f.End), f.Value, f.Accession, f.Form, formatDate(f.Filed),
			f.FY, nullString(f.FP), nullString(f.Frame), formatTS(now))
		if err != nil {
			return changed, fmt.Errorf("company fact %s %s %s: %w", ticker, f.Concept, f.Accession, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed++
		}
	}
	return changed, nil
}

// CompanyFacts returns the stored facts of ticker.
func (s *Store) CompanyFacts(ctx context.Context, ticker string) ([]model.CompanyFact, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT concept, unit, CAST(period_start AS TEXT), CAST(period_end AS TEXT), value,
		accn, form, CAST(filed AS TEXT), fy, fp, frame FROM company_facts WHERE ticker = ?
		ORDER BY concept, unit, period_end, period_start, accn`, ticker)
	if err != nil {
		return nil, fmt.Errorf("company facts %s: %w", ticker, err)
	}
	defer rows.Close()
	var out []model.CompanyFact
	for rows.Next() {
		f := model.CompanyFact{Ticker: ticker}
		var start, end, filed string
		var fy sql.NullInt64
		var fp, frame sql.NullString
		if err := rows.Scan(&f.Concept, &f.Unit, &start, &end, &f.Value, &f.Accession, &f.Form, &filed, &fy, &fp, &frame); err != nil {
			return nil, fmt.Errorf("company facts %s: %w", ticker, err)
		}
		if f.End, err = parseDate(end); err == nil && start != "" {
			f.Start, err = parseDate(start)
		}
		if err == nil {
			f.Filed, err = parseDate(filed)
		}
		if err != nil {
			return nil, fmt.Errorf("company facts %s: %w", ticker, err)
		}
		f.FY, f.FP, f.Frame = int(fy.Int64), fp.String, frame.String
		out = append(out, f)
	}
	return out, rows.Err()
}

// HasFactsFrom reports whether facts of a filing (accession) are stored.
func (s *Store) HasFactsFrom(ctx context.Context, ticker, accession string) (bool, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM company_facts WHERE ticker = ? AND accn = ?`, ticker, accession).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("company facts %s %s: %w", ticker, accession, err)
	}
	return n > 0, nil
}

// UpsertInstanceFacts writes facts read from an XBRL instance (segments,
// totals company facts lack) inside tx; keyed like company facts plus the
// segment member. Returns the rows inserted or changed.
func UpsertInstanceFacts(tx *sql.Tx, ticker string, facts []model.CompanyFact, now time.Time) (int, error) {
	changed := 0
	for _, f := range facts {
		start := ""
		if !f.Start.IsZero() {
			start = formatDate(f.Start)
		}
		res, err := tx.Exec(`INSERT INTO instance_facts
			(ticker, member, concept, unit, period_start, period_end, value, accn, form, filed, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (ticker, member, concept, unit, period_start, period_end, accn) DO UPDATE SET
			  value = excluded.value, form = excluded.form, filed = excluded.filed, fetched_at = excluded.fetched_at
			WHERE instance_facts.value != excluded.value`,
			ticker, f.Member, f.Concept, f.Unit, start, formatDate(f.End), f.Value, f.Accession, f.Form, formatDate(f.Filed), formatTS(now))
		if err != nil {
			return changed, fmt.Errorf("instance fact %s %s %s %s: %w", ticker, f.Member, f.Concept, f.Accession, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed++
		}
	}
	return changed, nil
}

// InstanceFacts returns the stored instance facts of ticker.
func (s *Store) InstanceFacts(ctx context.Context, ticker string) ([]model.CompanyFact, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT member, concept, unit, CAST(period_start AS TEXT), CAST(period_end AS TEXT), value,
		accn, form, CAST(filed AS TEXT) FROM instance_facts WHERE ticker = ?
		ORDER BY member, concept, unit, period_end, period_start, accn`, ticker)
	if err != nil {
		return nil, fmt.Errorf("instance facts %s: %w", ticker, err)
	}
	defer rows.Close()
	var out []model.CompanyFact
	for rows.Next() {
		f := model.CompanyFact{Ticker: ticker}
		var start, end, filed string
		if err := rows.Scan(&f.Member, &f.Concept, &f.Unit, &start, &end, &f.Value, &f.Accession, &f.Form, &filed); err != nil {
			return nil, fmt.Errorf("instance facts %s: %w", ticker, err)
		}
		if f.End, err = parseDate(end); err == nil && start != "" {
			f.Start, err = parseDate(start)
		}
		if err == nil {
			f.Filed, err = parseDate(filed)
		}
		if err != nil {
			return nil, fmt.Errorf("instance facts %s: %w", ticker, err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpsertCompanyReport writes an earnings report keyed by accession; a later
// call updates the period (estimated until the 10-Q/10-K) and the document.
func UpsertCompanyReport(tx *sql.Tx, r model.CompanyReport) error {
	var doc any
	if r.DocumentID != 0 {
		doc = r.DocumentID
	}
	_, err := tx.Exec(`INSERT INTO company_reports (ticker, period_end, form, report_date, accn, accepted_at, session, document_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (accn) DO UPDATE SET period_end = excluded.period_end, report_date = excluded.report_date,
		  accepted_at = excluded.accepted_at, session = excluded.session,
		  document_id = coalesce(excluded.document_id, company_reports.document_id)`,
		r.Ticker, formatDate(r.PeriodEnd), r.Form, formatDate(r.ReportDate), r.Accession, formatTS(r.AcceptedAt), r.Session, doc)
	if err != nil {
		return fmt.Errorf("company report %s %s: %w", r.Ticker, r.Accession, err)
	}
	return nil
}

const reportCols = `id, ticker, CAST(period_end AS TEXT), form, CAST(report_date AS TEXT), accn, accepted_at, session, document_id`

// CompanyReports returns the reports accepted in [from, to), oldest first.
func (s *Store) CompanyReports(ctx context.Context, from, to time.Time) ([]model.CompanyReport, error) {
	return s.queryReports(ctx, `accepted_at >= ? AND accepted_at < ? ORDER BY accepted_at`, formatTS(from), formatTS(to))
}

// CompanyReportsOf returns ticker's reports, newest first.
func (s *Store) CompanyReportsOf(ctx context.Context, ticker string) ([]model.CompanyReport, error) {
	return s.queryReports(ctx, `ticker = ? AND accn IS NOT NULL ORDER BY accepted_at DESC`, ticker)
}

// CompanyReport returns the report of an accession.
func (s *Store) CompanyReport(ctx context.Context, accession string) (model.CompanyReport, bool, error) {
	rs, err := s.queryReports(ctx, `accn = ?`, accession)
	if err != nil || len(rs) == 0 {
		return model.CompanyReport{}, false, err
	}
	return rs[0], true, nil
}

func (s *Store) queryReports(ctx context.Context, where string, args ...any) ([]model.CompanyReport, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+reportCols+` FROM company_reports WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("company reports: %w", err)
	}
	defer rows.Close()
	var out []model.CompanyReport
	for rows.Next() {
		var r model.CompanyReport
		var period, day string
		var form, session sql.NullString
		var doc sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Ticker, &period, &form, &day, &r.Accession, &r.AcceptedAt, &session, &doc); err != nil {
			return nil, fmt.Errorf("company reports: %w", err)
		}
		if r.PeriodEnd, err = parseDate(period); err == nil {
			r.ReportDate, err = parseDate(day)
		}
		if err != nil {
			return nil, fmt.Errorf("company reports: %w", err)
		}
		r.Form, r.Session, r.DocumentID, r.AcceptedAt = form.String, session.String, doc.Int64, r.AcceptedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// parseDate reads a DATE stored by formatDate; the driver may hand back a
// full timestamp for DATE columns, so only the date part is used.
func parseDate(s string) (time.Time, error) {
	if len(s) < len(dateLayout) {
		return time.Time{}, errors.New("date " + s + ": too short")
	}
	t, err := time.Parse(dateLayout, s[:len(dateLayout)])
	if err != nil {
		return time.Time{}, fmt.Errorf("date %q: %w", s, err)
	}
	return t, nil
}

// FactStats counts a company's stored facts.
type FactStats struct {
	Facts     int
	LastFiled time.Time // newest filing date among them
}

// CompanyFactStats returns the fact counts by ticker.
func (s *Store) CompanyFactStats(ctx context.Context) (map[string]FactStats, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT ticker, count(*), CAST(max(filed) AS TEXT) FROM company_facts GROUP BY ticker`)
	if err != nil {
		return nil, fmt.Errorf("company fact stats: %w", err)
	}
	defer rows.Close()
	out := map[string]FactStats{}
	for rows.Next() {
		var ticker, filed string
		var st FactStats
		if err := rows.Scan(&ticker, &st.Facts, &filed); err != nil {
			return nil, fmt.Errorf("company fact stats: %w", err)
		}
		if st.LastFiled, err = parseDate(filed); err != nil {
			return nil, fmt.Errorf("company fact stats: %w", err)
		}
		out[ticker] = st
	}
	return out, rows.Err()
}
