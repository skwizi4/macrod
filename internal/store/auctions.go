package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"macro-collector/internal/model"
)

// AuctionUpsert counts what UpsertAuctions did.
type AuctionUpsert struct {
	Inserted  int
	Updated   int // existing row changed (results published, amounts revised)
	Unchanged int
	Resulted  int // rows that gained results in this call (inserted with results or updated from none)
}

// auctionCols are the columns UpsertAuctions writes, after the key
// (auction_date, cusip) and before fetched_at.
const auctionCols = `security_type, kind, term, original_term, reopening, closing_time, issue_date, maturity_date,
	offering_amt, high_yield, high_investment_rate, low_yield, median_yield, bid_to_cover,
	total_accepted, comp_accepted, indirect_pct, direct_pct, dealer_pct, updated_at,
	source, region, avg_yield, bids_amt, retention_amt, coupon, issue_process`

func auctionValues(a model.Auction) []any {
	return []any{
		a.SecurityType, a.Kind, a.Term, a.Tenor, a.Reopening, nullString(a.ClosingTime),
		nullDate(a.IssueDate), nullDate(a.MaturityDate),
		nullNum(a.OfferingAmt), nullNum(a.HighYield), nullNum(a.HighRate), nullNum(a.LowYield), nullNum(a.MedianYield),
		nullNum(a.BidToCover), nullNum(a.TotalAccepted), nullNum(a.CompAccepted),
		nullNum(a.IndirectPct), nullNum(a.DirectPct), nullNum(a.DealerPct), nullTS(a.UpdatedAt),
		auctionSource(a), nullString(a.Region), nullNum(a.AvgYield), nullNum(a.BidsAmt), nullNum(a.Retention),
		nullNum(a.Coupon), nullString(a.Process),
	}
}

func auctionSource(a model.Auction) string {
	if a.Source == "" {
		return model.AuctionSourceTreasury
	}
	return a.Source
}

// UpsertAuctions writes auctions inside tx, keyed by (auction_date, cusip).
// fetched_at changes only when the row does.
func UpsertAuctions(tx *sql.Tx, as []model.Auction, now time.Time) (AuctionUpsert, error) {
	var res AuctionUpsert
	for _, a := range as {
		key := []any{formatDate(a.AuctionDate), a.CUSIP}
		vals := auctionValues(a)
		old, err := scanAuction(tx.QueryRow(`SELECT auction_date, cusip, `+auctionCols+`
			FROM auctions WHERE auction_date = ? AND cusip = ?`, key...))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			args := append(append(slices.Clone(key), vals...), formatTS(now))
			if _, err := tx.Exec(`INSERT INTO auctions (auction_date, cusip, `+auctionCols+`, fetched_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...); err != nil {
				return res, fmt.Errorf("auction %s %s insert: %w", key[0], a.CUSIP, err)
			}
			res.Inserted++
			if a.HasResults() {
				res.Resulted++
			}
		case err != nil:
			return res, fmt.Errorf("auction %s %s select: %w", key[0], a.CUSIP, err)
		case slices.Equal(fmtValues(auctionValues(old)), fmtValues(vals)):
			res.Unchanged++
		default:
			args := append(append(vals, formatTS(now)), key...)
			if _, err := tx.Exec(`UPDATE auctions SET security_type = ?, kind = ?, term = ?, original_term = ?,
				reopening = ?, closing_time = ?, issue_date = ?, maturity_date = ?, offering_amt = ?, high_yield = ?,
				high_investment_rate = ?, low_yield = ?, median_yield = ?, bid_to_cover = ?, total_accepted = ?,
				comp_accepted = ?, indirect_pct = ?, direct_pct = ?, dealer_pct = ?, updated_at = ?,
				source = ?, region = ?, avg_yield = ?, bids_amt = ?, retention_amt = ?, coupon = ?, issue_process = ?, fetched_at = ?
				WHERE auction_date = ? AND cusip = ?`, args...); err != nil {
				return res, fmt.Errorf("auction %s %s update: %w", key[0], a.CUSIP, err)
			}
			res.Updated++
			if a.HasResults() && !old.HasResults() {
				res.Resulted++
			}
		}
	}
	return res, nil
}

// fmtValues renders values for comparison (times and floats as text).
func fmtValues(vs []any) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = fmt.Sprint(v)
	}
	return out
}

// Auctions returns the Treasury auctions with auction_date in [from, to]
// (days), by date and CUSIP.
func (s *Store) Auctions(ctx context.Context, from, to time.Time) ([]model.Auction, error) {
	return s.AuctionsOf(ctx, model.AuctionSourceTreasury, from, to)
}

// AuctionsOf returns the auctions of a source with auction_date in [from,
// to] (days), by date and CUSIP (ISIN).
func (s *Store) AuctionsOf(ctx context.Context, source string, from, to time.Time) ([]model.Auction, error) {
	return s.queryAuctions(ctx, `WHERE source = ? AND auction_date >= ? AND auction_date <= ? ORDER BY auction_date, cusip`,
		source, formatDate(from), formatDate(to))
}

// AuctionsBefore returns up to n Treasury auctions with results of the same
// tenor and kind held before day, newest first: the base of the 6-auction
// averages.
func (s *Store) AuctionsBefore(ctx context.Context, tenor, kind string, day time.Time, n int) ([]model.Auction, error) {
	return s.AuctionsBeforeOf(ctx, model.AuctionSourceTreasury, tenor, kind, day, n)
}

// AuctionsBeforeOf is AuctionsBefore for the auctions of a source.
func (s *Store) AuctionsBeforeOf(ctx context.Context, source, tenor, kind string, day time.Time, n int) ([]model.Auction, error) {
	return s.queryAuctions(ctx, `WHERE source = ? AND original_term = ? AND kind = ? AND auction_date < ? AND bid_to_cover IS NOT NULL
		ORDER BY auction_date DESC, cusip LIMIT ?`, source, tenor, kind, formatDate(day), n)
}

// LastAuctionDate is the latest Treasury auction_date stored; ok is false
// if none.
func (s *Store) LastAuctionDate(ctx context.Context) (time.Time, bool, error) {
	return s.LastAuctionDateOf(ctx, model.AuctionSourceTreasury)
}

// LastAuctionDateOf is the latest auction_date of a source.
func (s *Store) LastAuctionDateOf(ctx context.Context, source string) (time.Time, bool, error) {
	var d sql.NullString
	if err := s.read.QueryRowContext(ctx, `SELECT max(auction_date) FROM auctions WHERE source = ?`, source).Scan(&d); err != nil {
		return time.Time{}, false, fmt.Errorf("last auction date: %w", err)
	}
	if !d.Valid {
		return time.Time{}, false, nil
	}
	// Aggregates lose the column type: the driver returns text.
	t, err := time.Parse(dateLayout, d.String[:min(len(d.String), len(dateLayout))])
	if err != nil {
		return time.Time{}, false, fmt.Errorf("last auction date %q: %w", d.String, err)
	}
	return t, true, nil
}

func (s *Store) queryAuctions(ctx context.Context, where string, args ...any) ([]model.Auction, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT auction_date, cusip, `+auctionCols+` FROM auctions `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("auctions: %w", err)
	}
	defer rows.Close()
	var out []model.Auction
	for rows.Next() {
		a, err := scanAuction(rows)
		if err != nil {
			return nil, fmt.Errorf("auctions scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAuction(row interface{ Scan(...any) error }) (model.Auction, error) {
	var (
		a                               model.Auction
		cusip, closing, region, process sql.NullString
		avg, bids, retention, coupon    sql.NullFloat64
		issue, maturity, updated        sql.NullTime
		offer, high, rate, low, med     sql.NullFloat64
		btc, total, comp, ind, dir, dlr sql.NullFloat64
	)
	err := row.Scan(&a.AuctionDate, &cusip, &a.SecurityType, &a.Kind, &a.Term, &a.Tenor, &a.Reopening, &closing,
		&issue, &maturity, &offer, &high, &rate, &low, &med, &btc, &total, &comp, &ind, &dir, &dlr, &updated,
		&a.Source, &region, &avg, &bids, &retention, &coupon, &process)
	if err != nil {
		return a, err
	}
	a.CUSIP, a.ClosingTime, a.Region, a.Process = cusip.String, closing.String, region.String, process.String
	a.IssueDate, a.MaturityDate, a.UpdatedAt = issue.Time, maturity.Time, updated.Time
	for dst, v := range map[*model.Num]sql.NullFloat64{
		&a.OfferingAmt: offer, &a.HighYield: high, &a.HighRate: rate, &a.LowYield: low, &a.MedianYield: med,
		&a.BidToCover: btc, &a.TotalAccepted: total, &a.CompAccepted: comp,
		&a.IndirectPct: ind, &a.DirectPct: dir, &a.DealerPct: dlr,
		&a.AvgYield: avg, &a.BidsAmt: bids, &a.Retention: retention, &a.Coupon: coupon,
	} {
		*dst = model.Num{V: v.Float64, OK: v.Valid}
	}
	return a, nil
}

// UpsertBuybacks writes buyback operations inside tx; returns how many rows
// were inserted or changed.
func UpsertBuybacks(tx *sql.Tx, bs []model.Buyback, now time.Time) (changed int, err error) {
	for _, b := range bs {
		r, err := tx.Exec(`INSERT INTO buybacks (operation_date, start_time, operation_type, security_type, maturity_bucket,
				par_offered, par_accepted, max_redeemed, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (operation_date, start_time, security_type, maturity_bucket) DO UPDATE SET
				operation_type = excluded.operation_type, par_offered = excluded.par_offered,
				par_accepted = excluded.par_accepted, max_redeemed = excluded.max_redeemed, fetched_at = excluded.fetched_at
			WHERE operation_type IS NOT excluded.operation_type OR par_offered IS NOT excluded.par_offered
				OR par_accepted IS NOT excluded.par_accepted OR max_redeemed IS NOT excluded.max_redeemed`,
			formatDate(b.OperationDate), b.StartTime, nullString(b.OperationType), b.SecurityType, b.MaturityBucket,
			nullNum(b.ParOffered), nullNum(b.ParAccepted), nullNum(b.MaxRedeemed), formatTS(now))
		if err != nil {
			return changed, fmt.Errorf("buyback %s %s: %w", formatDate(b.OperationDate), b.StartTime, err)
		}
		if n, _ := r.RowsAffected(); n > 0 {
			changed++
		}
	}
	return changed, nil
}

// Buybacks returns operations with operation_date in [from, to] (days).
func (s *Store) Buybacks(ctx context.Context, from, to time.Time) ([]model.Buyback, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT operation_date, start_time, operation_type, security_type, maturity_bucket,
			par_offered, par_accepted, max_redeemed
		FROM buybacks WHERE operation_date >= ? AND operation_date <= ? ORDER BY operation_date, start_time`,
		formatDate(from), formatDate(to))
	if err != nil {
		return nil, fmt.Errorf("buybacks: %w", err)
	}
	defer rows.Close()
	var out []model.Buyback
	for rows.Next() {
		var (
			b                    model.Buyback
			opType               sql.NullString
			offered, acc, maxRed sql.NullFloat64
		)
		if err := rows.Scan(&b.OperationDate, &b.StartTime, &opType, &b.SecurityType, &b.MaturityBucket,
			&offered, &acc, &maxRed); err != nil {
			return nil, fmt.Errorf("buybacks scan: %w", err)
		}
		b.OperationType = opType.String
		b.ParOffered = model.Num{V: offered.Float64, OK: offered.Valid}
		b.ParAccepted = model.Num{V: acc.Float64, OK: acc.Valid}
		b.MaxRedeemed = model.Num{V: maxRed.Float64, OK: maxRed.Valid}
		out = append(out, b)
	}
	return out, rows.Err()
}

func nullNum(n model.Num) any {
	if !n.OK {
		return nil
	}
	return n.V
}

func nullDate(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatDate(t)
}

func nullTS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTS(t)
}
