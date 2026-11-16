package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"macro-collector/internal/model"
)

func auction(day, cusip, tenor string, btc float64) model.Auction {
	a := model.Auction{
		AuctionDate: d(day), SecurityType: model.KindNote, Kind: model.KindNote, Term: tenor, Tenor: tenor,
		CUSIP: cusip, ClosingTime: "13:00", IssueDate: d(day).AddDate(0, 0, 7),
		OfferingAmt: model.Some(42e9),
	}
	if btc > 0 {
		a.HighYield, a.BidToCover = model.Some(4.1), model.Some(btc)
		a.IndirectPct, a.DirectPct, a.DealerPct = model.Some(70), model.Some(20), model.Some(10)
		a.UpdatedAt = time.Date(2026, 10, 7, 17, 4, 2, 0, time.UTC)
	}
	return a
}

func TestUpsertAuctions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)
	write := func(as ...model.Auction) AuctionUpsert {
		t.Helper()
		var res AuctionUpsert
		if err := s.Write(ctx, func(tx *sql.Tx) (err error) {
			res, err = UpsertAuctions(tx, as, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return res
	}

	// Announced, then results, then the same again.
	if res := write(auction("2026-10-07", "C10", "10-Year", 0)); res.Inserted != 1 || res.Resulted != 0 {
		t.Errorf("announce = %+v", res)
	}
	if res := write(auction("2026-10-07", "C10", "10-Year", 2.5)); res.Updated != 1 || res.Resulted != 1 {
		t.Errorf("results = %+v", res)
	}
	if res := write(auction("2026-10-07", "C10", "10-Year", 2.5)); res.Unchanged != 1 || res.Updated != 0 {
		t.Errorf("repeat = %+v", res)
	}
	as, err := s.Auctions(ctx, d("2026-10-01"), d("2026-10-31"))
	if err != nil || len(as) != 1 {
		t.Fatalf("auctions = %v, %v", as, err)
	}
	a := as[0]
	if !a.HasResults() || a.BidToCover.V != 2.5 || a.Tenor != "10-Year" || a.ClosingTime != "13:00" ||
		!a.IssueDate.Equal(d("2026-10-14")) || a.HighRate.OK || !a.UpdatedAt.Equal(time.Date(2026, 10, 7, 17, 4, 2, 0, time.UTC)) {
		t.Errorf("stored = %+v", a)
	}

	// Six-auction history: newest first, same tenor and kind, results only.
	write(auction("2026-07-08", "A", "10-Year", 2.4), auction("2026-08-12", "B", "10-Year", 2.6),
		auction("2026-09-09", "C", "10-Year", 2.3), auction("2026-09-10", "T", "30-Year", 2.2),
		auction("2026-11-12", "N", "10-Year", 0))
	prev, err := s.AuctionsBefore(ctx, "10-Year", model.KindNote, d("2026-10-07"), 6)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range prev {
		got = append(got, p.CUSIP)
	}
	if len(got) != 3 || got[0] != "C" || got[2] != "A" {
		t.Errorf("before = %v", got)
	}
	if last, ok, err := s.LastAuctionDate(ctx); err != nil || !ok || !last.Equal(d("2026-11-12")) {
		t.Errorf("last = %v %v %v", last, ok, err)
	}
}

// Bund auctions share the table: the Treasury readers never see them, the
// source readers see only theirs, and the Bund fields round-trip.
func TestAuctionsBySource(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	bund := model.Auction{
		Source: "finanzagentur", Region: "eu", AuctionDate: d("2026-11-04"), CUSIP: "DE000BU2Z056",
		SecurityType: "Bund", Kind: "Bund", Term: "10-Year", Tenor: "10-Year", ClosingTime: "11:30",
		OfferingAmt: model.Some(5e9), BidsAmt: model.Some(6.2e9), TotalAccepted: model.Some(4.1e9),
		Retention: model.Some(0.9e9), AvgYield: model.Some(2.71), Coupon: model.Some(2.6),
		BidToCover: model.Some(1.5), Process: "Auc",
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := UpsertAuctions(tx, []model.Auction{auction("2026-11-04", "C10", "10-Year", 2.5), bund}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	from, to := d("2026-11-01"), d("2026-11-30")
	if as, err := s.Auctions(ctx, from, to); err != nil || len(as) != 1 || as[0].CUSIP != "C10" || as[0].Source != model.AuctionSourceTreasury {
		t.Errorf("treasury = %+v, %v", as, err)
	}
	as, err := s.AuctionsOf(ctx, "finanzagentur", from, to)
	if err != nil || len(as) != 1 {
		t.Fatalf("bund = %+v, %v", as, err)
	}
	if b := as[0]; b.Region != "eu" || b.AvgYield.V != 2.71 || b.BidsAmt.V != 6.2e9 || b.Retention.V != 0.9e9 ||
		b.Coupon.V != 2.6 || b.Process != "Auc" || b.HighYield.OK {
		t.Errorf("bund stored = %+v", b)
	}
	if prev, err := s.AuctionsBefore(ctx, "10-Year", "Bund", d("2026-12-01"), 6); err != nil || len(prev) != 0 {
		t.Errorf("treasury before = %+v, %v", prev, err)
	}
	if prev, err := s.AuctionsBeforeOf(ctx, "finanzagentur", "10-Year", "Bund", d("2026-12-01"), 6); err != nil || len(prev) != 1 {
		t.Errorf("bund before = %+v, %v", prev, err)
	}
	if last, ok, err := s.LastAuctionDateOf(ctx, "finanzagentur"); err != nil || !ok || !last.Equal(d("2026-11-04")) {
		t.Errorf("bund last = %v %v %v", last, ok, err)
	}
}

func TestUpsertBuybacks(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	b := model.Buyback{OperationDate: d("2026-10-21"), StartTime: "13:40", OperationType: "Liquidity Support",
		SecurityType: "TIPS", MaturityBucket: "1Y to 10Y", ParOffered: model.Some(4.751e9), MaxRedeemed: model.Some(7.5e8)}
	write := func(b model.Buyback) int {
		t.Helper()
		var n int
		if err := s.Write(ctx, func(tx *sql.Tx) (err error) {
			n, err = UpsertBuybacks(tx, []model.Buyback{b}, time.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := write(b); n != 1 {
		t.Errorf("insert changed = %d", n)
	}
	if n := write(b); n != 0 {
		t.Errorf("repeat changed = %d", n)
	}
	b.ParAccepted = model.Some(7.5e8)
	if n := write(b); n != 1 {
		t.Errorf("update changed = %d", n)
	}
	bs, err := s.Buybacks(ctx, d("2026-10-01"), d("2026-10-31"))
	if err != nil || len(bs) != 1 || bs[0].ParAccepted.V != 7.5e8 || bs[0].OperationType != "Liquidity Support" {
		t.Errorf("buybacks = %+v, %v", bs, err)
	}
}
