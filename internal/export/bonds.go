package export

import (
	"context"
	"slices"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/finanzagentur"
	"macro-collector/internal/transform"
)

// BondAuction is an auction of government bonds of the period (Bund:
// Finanzagentur). BTCDev and YieldDev are against the mean of the previous
// AuctionWindow auctions of the same security and tenor.
type BondAuction struct {
	Date      Day      `json:"date"`
	Security  string   `json:"security"`
	Term      string   `json:"term"`
	ISIN      string   `json:"isin"`
	Reopening bool     `json:"reopening"`
	MultiISIN bool     `json:"multi_isin"`
	Coupon    Quantity `json:"coupon"`
	Offered   Quantity `json:"offered"`
	Bids      Quantity `json:"bids"`
	Allotted  Quantity `json:"allotted"`
	Retention Quantity `json:"retention"`
	Yield     Quantity `json:"yield"`
	YieldDev  Quantity `json:"yield_dev"`
	BTC       Quantity `json:"btc"`
	BTCDev    Quantity `json:"btc_dev"`
	Previous  int      `json:"previous"` // auctions behind the deviations
}

// BondNext is a planned auction of the next week, its time in the region's
// zone.
type BondNext struct {
	At   time.Time `json:"at"`
	Name string    `json:"name"`
	ISIN string    `json:"isin"`
}

// BondsReport is <region>/fiscal_bonds/current_week.md: the curve, the
// spreads, the monthly yields, debt and deficit, the auctions.
type BondsReport struct {
	Header        Header         `json:"header"`
	Released      []ReleaseRow   `json:"released"`
	Curve         []ChangesRow   `json:"curve"`
	Spreads       []ChangesRow   `json:"spreads"`
	Monthly       []ChangesRow   `json:"monthly"`
	Debt          []ChangesRow   `json:"debt"`
	AuctionWindow int            `json:"auction_window"`
	Auctions      []BondAuction  `json:"auctions"`
	Next          []BondNext     `json:"next"`
	Missing       MissingSection `json:"missing"`
}

// bondsAuctions reports whether the region's bond auctions are loaded here.
func (w *week) bondsAuctions() bool {
	f := w.cfg.Finanzagentur
	return f.Enabled && w.cfg.RegionOfTag(f.Agent) == w.region.ID
}

func (w *week) bonds(ctx context.Context) (string, error) {
	tables := map[string][]view{}
	var ids []string
	for _, t := range []string{"curve", "spreads", "monthly", "debt"} {
		vs, err := w.seriesList(ctx, w.table(t))
		if err != nil {
			return "", err
		}
		tables[t] = vs
		ids = append(ids, w.table(t)...)
	}
	sources := w.seriesSources(ids)
	if w.bondsAuctions() {
		sources = append(sources, finanzagentur.Source)
	}
	sources = w.sourcesWithTexts(sources...)
	all := concat(tables["curve"], tables["spreads"], tables["monthly"], tables["debt"])
	m := BondsReport{Header: w.newHeader("", w.title(), sources, staleIDs(all), w.from, w.to),
		Curve: changesRows(tables["curve"]), Spreads: changesRows(tables["spreads"]), Monthly: changesRows(tables["monthly"]), Debt: changesRows(tables["debt"]),
		AuctionWindow: transform.AuctionWindow, Auctions: []BondAuction{}, Next: []BondNext{}}
	var err error
	if m.Released, err = w.releasedRows(ctx, all); err != nil {
		return "", err
	}
	if w.bondsAuctions() {
		if m.Auctions, err = w.bondAuctions(ctx); err != nil {
			return "", err
		}
		if m.Next, err = w.bondNext(ctx); err != nil {
			return "", err
		}
	}
	if m.Missing, err = w.missingSection(ctx, all, sources); err != nil {
		return "", err
	}
	return w.report(w.path(), config.ReportBonds, &m)
}

// bondAuctions are the auctions with results held in the period (days in
// the region's zone).
func (w *week) bondAuctions(ctx context.Context) ([]BondAuction, error) {
	f, t := w.from.In(w.et), w.to.In(w.et)
	first := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, time.UTC)
	last := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	as, err := w.st.AuctionsOf(ctx, finanzagentur.Source, first, last)
	if err != nil {
		return nil, err
	}
	out := []BondAuction{}
	for _, a := range as {
		if !a.HasResults() {
			continue
		}
		prev, err := w.st.AuctionsBeforeOf(ctx, finanzagentur.Source, a.Tenor, a.Kind, a.AuctionDate, transform.AuctionWindow)
		if err != nil {
			return nil, err
		}
		bn := func(n model.Num) Quantity { return qty(transform.Value{V: n.V / 1e9, OK: n.OK}, unitEURBn) }
		r := BondAuction{Date: Day{a.AuctionDate}, Security: a.Kind, Term: a.Term, ISIN: a.CUSIP, Reopening: a.Reopening,
			MultiISIN: a.Process == finanzagentur.ProcessMultiISIN, Coupon: numQty(a.Coupon, unitPercent),
			Offered: bn(a.OfferingAmt), Bids: bn(a.BidsAmt), Allotted: bn(a.TotalAccepted), Retention: bn(a.Retention),
			Yield: numQty(a.AvgYield, unitPercent), BTC: numQty(a.BidToCover, unitRatio),
			YieldDev: qty(transform.Value{}, unitBP), BTCDev: qty(transform.Value{}, unitRatio), Previous: len(prev)}
		var btc, yld float64
		var nb, ny int
		for _, p := range prev {
			if p.BidToCover.OK {
				btc, nb = btc+p.BidToCover.V, nb+1
			}
			if p.AvgYield.OK {
				yld, ny = yld+p.AvgYield.V, ny+1
			}
		}
		if nb > 0 && a.BidToCover.OK {
			r.BTCDev = qty(some(a.BidToCover.V-btc/float64(nb)), unitRatio)
		}
		if ny > 0 && a.AvgYield.OK {
			r.YieldDev = qty(some((a.AvgYield.V-yld/float64(ny))*100), unitBP)
		}
		out = append(out, r)
	}
	return out, nil
}

// bondNext are the planned auctions of the next seven days.
func (w *week) bondNext(ctx context.Context) ([]BondNext, error) {
	evs, err := w.releasesBetween(ctx, w.to, w.to.AddDate(0, 0, 7))
	if err != nil {
		return nil, err
	}
	out := []BondNext{}
	for _, e := range evs {
		if e.Source == finanzagentur.SourceAuction && e.Status != model.StatusRescheduled {
			out = append(out, BondNext{At: e.ScheduledAt.In(w.et), Name: e.Name, ISIN: e.Related.CUSIP})
		}
	}
	slices.SortStableFunc(out, func(a, b BondNext) int { return a.At.Compare(b.At) })
	return out, nil
}

// eurBillions is an amount in billions of euros, as billions writes dollars.
func eurBillions(v transform.Value) string {
	if !v.OK {
		return dash
	}
	return billionsNum(v, false) + " млрд €"
}

// bondKind words how an auction was placed: "новый", "доразм.", and
// "несколько ISIN" for a multi-ISIN auction.
func bondKind(a BondAuction) string {
	s := "новый"
	if a.Reopening {
		s = "доразм."
	}
	if a.MultiISIN {
		s += ", несколько ISIN"
	}
	return s
}
