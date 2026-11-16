package treasury

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"time"

	"macro-collector/internal/model"
)

// eastern is the zone of TA_WS timestamps.
var eastern = func() *time.Location {
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return l
}()

// issueLag bounds the days from auction to issue (TIPS and FRNs settle up
// to ~8 days later, CMBs sometimes the same day).
const issueLag = 21

// queryTypes maps a security type (Bill, Note, Bond) to the TA_WS "type"
// values that cover it: the API filters by the type field, where TIPS, FRNs
// and CMBs are types of their own.
var queryTypes = map[string][]string{
	model.KindBill: {model.KindBill, model.KindCMB},
	model.KindNote: {model.KindNote, model.KindTIPS, model.KindFRN},
	model.KindBond: {model.KindBond, model.KindTIPS},
}

// FetchAuctions returns the auctions of the given security types (Bill,
// Note, Bond) whose competitive close falls in [from, to] (calendar days),
// from the TreasuryDirect search API. Fiscal Data's auctions_query only
// fills results by the issue date, TA_WS minutes after the close (LOOKUP
// T4). The search filters by issue date, so the range is extended by
// issueLag and filtered by auction date; long ranges go in yearly requests.
func (c *Client) FetchAuctions(ctx context.Context, securityTypes []string, from, to time.Time) ([]model.Auction, error) {
	var types []string
	for _, st := range securityTypes {
		for _, t := range queryTypes[st] {
			if !slices.Contains(types, t) {
				types = append(types, t)
			}
		}
	}
	var out []model.Auction
	last := to.AddDate(0, 0, issueLag)
	for start := from; !start.After(last); start = start.AddDate(1, 0, 0) {
		end := start.AddDate(1, 0, -1)
		if end.After(last) {
			end = last
		}
		for _, typ := range types {
			recs, err := c.directGet(ctx, "search", url.Values{
				"type":      {typ},
				"startDate": {start.Format(time.DateOnly)},
				"endDate":   {end.Format(time.DateOnly)},
			})
			if err != nil {
				return nil, fmt.Errorf("treasury auctions %s %s…%s: %w", typ, start.Format(time.DateOnly), end.Format(time.DateOnly), err)
			}
			as, err := parseAuctions(recs)
			if err != nil {
				return nil, fmt.Errorf("treasury auctions %s %s…%s: %w", typ, start.Format(time.DateOnly), end.Format(time.DateOnly), err)
			}
			for _, a := range as {
				if !a.AuctionDate.Before(from) && !a.AuctionDate.After(to) && slices.Contains(securityTypes, a.SecurityType) {
					out = append(out, a)
				}
			}
		}
	}
	// A TIPS bond comes from both the Bond and the TIPS query.
	return mergeAuctions(out), nil
}

// FetchUpcoming returns announced auctions that have not closed yet.
func (c *Client) FetchUpcoming(ctx context.Context) ([]model.Auction, error) {
	recs, err := c.directGet(ctx, "upcoming", nil)
	if err != nil {
		return nil, fmt.Errorf("treasury upcoming auctions: %w", err)
	}
	as, err := parseAuctions(recs)
	if err != nil {
		return nil, fmt.Errorf("treasury upcoming auctions: %w", err)
	}
	sortAuctions(as)
	return as, nil
}

func sortAuctions(as []model.Auction) {
	sort.SliceStable(as, func(i, j int) bool {
		if !as[i].AuctionDate.Equal(as[j].AuctionDate) {
			return as[i].AuctionDate.Before(as[j].AuctionDate)
		}
		return as[i].CUSIP < as[j].CUSIP
	})
}

func parseAuctions(recs []map[string]string) ([]model.Auction, error) {
	out := make([]model.Auction, 0, len(recs))
	for _, r := range recs {
		a, err := parseAuction(r)
		if err != nil {
			return nil, fmt.Errorf("cusip %s: %w", r["cusip"], err)
		}
		out = append(out, a)
	}
	return out, nil
}

func parseAuction(r map[string]string) (model.Auction, error) {
	a := model.Auction{
		SecurityType: r["securityType"],
		Term:         r["securityTerm"],
		CUSIP:        r["cusip"],
		Reopening:    r["reopening"] == "Yes",
	}
	a.Kind = kindOf(r)
	// Coupons group by the original term (a 10-year reopening is a
	// "9-Year 11-Month" note); bills by their own term (a 6-week bill can be
	// a reopening of a 52-week one).
	a.Tenor = a.Term
	if a.SecurityType != model.KindBill && r["originalSecurityTerm"] != "" {
		a.Tenor = r["originalSecurityTerm"]
	}
	var err error
	if a.AuctionDate, err = date(r["competitiveAuctionCloseDate"]); err != nil {
		return a, err
	}
	if a.AuctionDate.IsZero() {
		if a.AuctionDate, err = date(r["auctionDate"]); err != nil {
			return a, err
		}
	}
	if a.AuctionDate.IsZero() {
		return a, fmt.Errorf("no auction date")
	}
	if a.IssueDate, err = date(r["issueDate"]); err != nil {
		return a, err
	}
	if a.MaturityDate, err = date(r["maturityDate"]); err != nil {
		return a, err
	}
	if a.ClosingTime, err = clock(r["competitiveAuctionCloseTime"]); err != nil {
		return a, err
	}
	if ts := r["updatedTimestamp"]; ts != "" {
		if a.UpdatedAt, err = time.ParseInLocation("2006-01-02T15:04:05", ts, eastern); err != nil {
			return a, fmt.Errorf("updatedTimestamp %q: %w", ts, err)
		}
	}

	nums := []struct {
		field string
		dst   *model.Num
	}{
		{"offeringAmount", &a.OfferingAmt},
		{"bidToCoverRatio", &a.BidToCover},
		{"grandTotalParAwarded", &a.TotalAccepted},
	}
	if a.SecurityType == model.KindBill {
		nums = append(nums, struct {
			field string
			dst   *model.Num
		}{"investmentRate", &a.HighRate})
	} else {
		nums = append(nums, []struct {
			field string
			dst   *model.Num
		}{{"highYield", &a.HighYield}, {"lowYield", &a.LowYield}, {"medianYield", &a.MedianYield}}...)
	}
	for _, n := range nums {
		v, ok, err := number(r[n.field])
		if err != nil {
			return a, fmt.Errorf("%s: %w", n.field, err)
		}
		if ok {
			*n.dst = model.Some(v)
		}
	}
	if !a.TotalAccepted.OK {
		if v, ok, err := number(r["totalAccepted"]); err != nil {
			return a, fmt.Errorf("totalAccepted: %w", err)
		} else if ok {
			a.TotalAccepted = model.Some(v)
		}
	}

	// Bidder shares of the competitive amount awarded; auctions before the
	// bidder breakdown (early 2000s) leave them absent.
	var parts [3]float64
	for i, f := range []string{"indirectBiddersCompetitiveParAwarded", "directBiddersCompetitiveParAwarded", "primaryDealersCompetitiveParAwarded"} {
		v, ok, err := number(r[f])
		if err != nil {
			return a, fmt.Errorf("%s: %w", f, err)
		}
		if !ok {
			return a, nil
		}
		parts[i] = v
	}
	comp := parts[0] + parts[1] + parts[2]
	if comp <= 0 {
		return a, nil
	}
	a.CompAccepted = model.Some(comp)
	a.IndirectPct = model.Some(parts[0] / comp * 100)
	a.DirectPct = model.Some(parts[1] / comp * 100)
	a.DealerPct = model.Some(parts[2] / comp * 100)
	return a, nil
}

func kindOf(r map[string]string) string {
	switch {
	case r["cashManagementBill"] == "Yes" || r["type"] == "CMB":
		return model.KindCMB
	case r["type"] == "TIPS" || r["inflationProtected"] == "Yes":
		return model.KindTIPS
	case r["type"] == "FRN" || r["floatingRate"] == "Yes":
		return model.KindFRN
	}
	return r["securityType"]
}
