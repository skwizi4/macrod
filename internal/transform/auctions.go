package transform

import (
	"time"

	"macro-collector/internal/model"
)

// Auction comparisons (decision 2026-10-24): deviations from the mean of
// the previous AuctionWindow auctions of the same tenor and kind, and an
// approximate tail against the constant-maturity yield of the auction day.
const (
	AuctionWindow = 6
	// minAuctionHistory is the fewest previous auctions a deviation needs.
	minAuctionHistory = 3
)

// cmtSeries maps a nominal coupon tenor to its FRED constant-maturity series.
var cmtSeries = map[string]string{
	"2-Year": "DGS2", "3-Year": "DGS3", "5-Year": "DGS5", "7-Year": "DGS7",
	"10-Year": "DGS10", "20-Year": "DGS20", "30-Year": "DGS30",
}

// CMTFor is the FRED series behind the ≈tail of an auction, "" when there is
// none (bills, TIPS, FRNs, unusual tenors).
func CMTFor(a model.Auction) string {
	if a.Kind != model.KindNote && a.Kind != model.KindBond {
		return ""
	}
	return cmtSeries[a.Tenor]
}

// CMTPublished is when FRED has the CMT yields of day: H.15 posts them the
// next business day around 16:15 ET (federal holidays are not skipped, so a
// wait after one ends a day early). et is America/New_York.
func CMTPublished(day time.Time, et *time.Location) time.Time {
	d := time.Date(day.Year(), day.Month(), day.Day()+1, 16, 15, 0, 0, et)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// AuctionMetrics are the comparisons shown for one auction.
type AuctionMetrics struct {
	// TailBP ≈ high yield − CMT of the same tenor at the close of the
	// auction day, in basis points. An approximation: the real tail is
	// measured against the when-issued yield at 13:00, which no free source
	// publishes.
	TailBP Value
	// Deviations from the mean of previous auctions: BTC in points, rates
	// in basis points, bidder shares in percentage points.
	BTCDev, RateDevBP, IndirectDev, DirectDev, DealerDev Value
	History                                              int // previous auctions used
}

// CompareAuction computes the metrics of a against prev (previous auctions
// of the same tenor and kind with results, any order; only the first
// AuctionWindow are used) and cmt, the CMT yield on the auction day.
func CompareAuction(a model.Auction, prev []model.Auction, cmt Value) AuctionMetrics {
	if len(prev) > AuctionWindow {
		prev = prev[:AuctionWindow]
	}
	m := AuctionMetrics{History: len(prev)}
	if cmt.OK && a.HighYield.OK && CMTFor(a) != "" {
		m.TailBP = some((a.HighYield.V - cmt.V) * 100)
	}
	rate := func(x model.Auction) model.Num {
		if x.SecurityType == model.KindBill {
			return x.HighRate
		}
		return x.HighYield
	}
	dev := func(get func(model.Auction) model.Num, scale float64) Value {
		cur := get(a)
		if !cur.OK {
			return Value{}
		}
		var sum float64
		n := 0
		for _, p := range prev {
			if v := get(p); v.OK {
				sum += v.V
				n++
			}
		}
		if n < minAuctionHistory {
			return Value{}
		}
		return some((cur.V - sum/float64(n)) * scale)
	}
	m.BTCDev = dev(func(x model.Auction) model.Num { return x.BidToCover }, 1)
	m.RateDevBP = dev(rate, 100)
	m.IndirectDev = dev(func(x model.Auction) model.Num { return x.IndirectPct }, 1)
	m.DirectDev = dev(func(x model.Auction) model.Num { return x.DirectPct }, 1)
	m.DealerDev = dev(func(x model.Auction) model.Num { return x.DealerPct }, 1)
	return m
}
