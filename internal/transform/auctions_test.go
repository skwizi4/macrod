package transform

import (
	"math"
	"testing"
	"time"

	"macro-collector/internal/model"
)

func coupon(tenor, kind string, high, btc, ind, dir, dlr float64) model.Auction {
	return model.Auction{
		SecurityType: model.KindNote, Kind: kind, Tenor: tenor,
		HighYield: model.Some(high), BidToCover: model.Some(btc),
		IndirectPct: model.Some(ind), DirectPct: model.Some(dir), DealerPct: model.Some(dlr),
	}
}

func approx(t *testing.T, name string, v Value, want float64) {
	t.Helper()
	if !v.OK || math.Abs(v.V-want) > 1e-9 {
		t.Errorf("%s = %+v, want %v", name, v, want)
	}
}

// Reference values computed by hand.
func TestCompareAuction(t *testing.T) {
	a := coupon("10-Year", model.KindNote, 4.250, 2.40, 65.0, 20.0, 15.0)
	var prev []model.Auction
	// Seven previous auctions: only the newest six count (the 7th would
	// shift every mean).
	for _, p := range [][5]float64{
		{4.10, 2.50, 70, 18, 12}, {4.00, 2.60, 72, 17, 11}, {3.90, 2.55, 68, 20, 12},
		{3.95, 2.45, 66, 19, 15}, {4.05, 2.65, 71, 18, 11}, {4.20, 2.35, 67, 21, 12},
		{9.99, 9.99, 99, 99, 99},
	} {
		prev = append(prev, coupon("10-Year", model.KindNote, p[0], p[1], p[2], p[3], p[4]))
	}
	m := CompareAuction(a, prev, some(4.231))
	if m.History != 6 {
		t.Errorf("history = %d", m.History)
	}
	approx(t, "tail", m.TailBP, 1.9)                   // (4.250 − 4.231) × 100
	approx(t, "btc", m.BTCDev, 2.40-2.516666666666667) // mean 15.1 / 6
	approx(t, "rate", m.RateDevBP, (4.25-4.033333333333333)*100)
	approx(t, "indirect", m.IndirectDev, 65-69)
	approx(t, "direct", m.DirectDev, 20-18.833333333333332)
	approx(t, "dealer", m.DealerDev, 15-12.166666666666666)
}

func TestCompareAuctionMissing(t *testing.T) {
	// TIPS: no ≈tail even with a CMT value; two previous auctions are too few.
	tips := coupon("5-Year", model.KindTIPS, 2.367, 2.41, 61, 32, 7)
	prev := []model.Auction{coupon("5-Year", model.KindTIPS, 2.1, 2.5, 60, 30, 10), coupon("5-Year", model.KindTIPS, 2.2, 2.4, 62, 28, 10)}
	m := CompareAuction(tips, prev, some(3.6))
	if m.TailBP.OK || m.BTCDev.OK || CMTFor(tips) != "" {
		t.Errorf("tips = %+v", m)
	}
	// No CMT on the auction day (holiday in FRED): no ≈tail.
	note := coupon("20-Year", model.KindBond, 5.469, 2.73, 68, 18, 14)
	if m := CompareAuction(note, nil, Value{}); m.TailBP.OK || CMTFor(note) != "DGS20" {
		t.Errorf("no cmt = %+v", m)
	}
	// Bills compare the investment rate; shares missing on old auctions.
	bill := model.Auction{SecurityType: model.KindBill, Kind: model.KindBill, Tenor: "13-Week",
		HighRate: model.Some(4.185), BidToCover: model.Some(2.68)}
	var bills []model.Auction
	for _, r := range []float64{4.10, 4.15, 4.20} {
		bills = append(bills, model.Auction{SecurityType: model.KindBill, Kind: model.KindBill, HighRate: model.Some(r), BidToCover: model.Some(2.6)})
	}
	m = CompareAuction(bill, bills, some(4.0))
	approx(t, "bill rate", m.RateDevBP, (4.185-4.15)*100)
	approx(t, "bill btc", m.BTCDev, 0.08)
	if m.TailBP.OK || m.IndirectDev.OK {
		t.Errorf("bill = %+v", m)
	}
}

func TestCMTPublished(t *testing.T) {
	et, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ day, want string }{
		{"2026-10-29", "2026-10-30T16:15:00-04:00"}, // Thursday → Friday
		{"2026-10-30", "2026-11-02T16:15:00-05:00"}, // Friday → Monday
	} {
		day, _ := time.Parse(time.DateOnly, tt.day)
		if got := CMTPublished(day, et).Format(time.RFC3339); got != tt.want {
			t.Errorf("CMTPublished(%s) = %s, want %s", tt.day, got, tt.want)
		}
	}
}
