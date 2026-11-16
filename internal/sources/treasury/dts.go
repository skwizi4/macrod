package treasury

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"

	"macro-collector/internal/model"
)

// Series ids stored under source "treasury".
const (
	SeriesTGA         = "TGA_DTS"      // DTS closing TGA balance, millions of $
	SeriesMTSReceipts = "MTS_RECEIPTS" // MTS monthly gross receipts, $
	SeriesMTSOutlays  = "MTS_OUTLAYS"  // MTS monthly gross outlays, $
	SeriesMTSDeficit  = "MTS_DEFICIT"  // MTS monthly deficit (> 0) / surplus (< 0), $

	// DTS public debt transactions of marketable securities by settlement
	// day (cash basis), millions of $; coupons are notes and bonds,
	// TIPS and FRN included.
	SeriesBillsIssued     = "PDT_BILLS_ISSUES"
	SeriesBillsRedeemed   = "PDT_BILLS_REDEMPTIONS"
	SeriesCouponsIssued   = "PDT_COUPONS_ISSUES"
	SeriesCouponsRedeemed = "PDT_COUPONS_REDEMPTIONS"
)

// Units of the treasury series, as stored in series_meta.units.
const (
	UnitsMillionsUSD = "Millions of U.S. Dollars"
	UnitsUSD         = "U.S. Dollars"
)

const (
	endpointDTS        = "v1/accounting/dts/operating_cash_balance"
	endpointPublicDebt = "v1/accounting/dts/public_debt_transactions"
)

// The closing TGA balance changed rows and columns twice (LOOKUP T2).
const (
	tgaUntil2021 = "Federal Reserve Account"                        // close_today_bal, until 2021-09-30
	tgaUntil2022 = "Treasury General Account (TGA)"                 // close_today_bal, 2021-10-01…2022-04-15
	tgaClosing   = "Treasury General Account (TGA) Closing Balance" // open_today_bal, from 2022-04-18
)

// FetchTGA returns the daily closing TGA balance (millions of $) from since on.
func (c *Client) FetchTGA(ctx context.Context, since time.Time) ([]model.Observation, error) {
	rows, err := c.fiscal(ctx, endpointDTS, url.Values{
		"fields": {"record_date,account_type,open_today_bal,close_today_bal"},
		"filter": {"record_date:gte:" + since.Format(time.DateOnly)},
		"sort":   {"record_date"},
	})
	if err != nil {
		return nil, fmt.Errorf("treasury dts: %w", err)
	}
	obs, err := parseTGA(rows)
	if err != nil {
		return nil, fmt.Errorf("treasury dts: %w", err)
	}
	return obs, nil
}

func parseTGA(rows []row) ([]model.Observation, error) {
	byDate := make(map[time.Time]float64)
	for _, r := range rows {
		var field string
		switch r["account_type"] {
		case tgaUntil2021, tgaUntil2022:
			field = "close_today_bal"
		case tgaClosing:
			field = "open_today_bal"
		default:
			continue
		}
		d, err := date(r["record_date"])
		if err != nil {
			return nil, err
		}
		v, ok, err := number(r[field])
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", r["record_date"], r["account_type"], err)
		}
		if !ok {
			continue
		}
		byDate[d] = v
	}
	out := make([]model.Observation, 0, len(byDate))
	for d, v := range byDate {
		out = append(out, model.Observation{Date: d, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

// FetchPublicDebt returns the daily issues and redemptions of marketable
// bills and coupons (millions of $) from since on, by series id.
func (c *Client) FetchPublicDebt(ctx context.Context, since time.Time) (map[string][]model.Observation, error) {
	rows, err := c.fiscal(ctx, endpointPublicDebt, url.Values{
		"fields": {"record_date,transaction_type,security_market,security_type,transaction_today_amt"},
		"filter": {"record_date:gte:" + since.Format(time.DateOnly)},
		"sort":   {"record_date"},
	})
	if err != nil {
		return nil, fmt.Errorf("treasury public debt: %w", err)
	}
	out, err := parsePublicDebt(rows)
	if err != nil {
		return nil, fmt.Errorf("treasury public debt: %w", err)
	}
	return out, nil
}

// parsePublicDebt keeps marketable bills, notes and bonds only:
// nonmarketable debt (Government Account Series, savings bonds, SLGS…),
// Federal Financing Bank and the TIPS inflation increment (an accrual, not
// a sale) are left out.
func parsePublicDebt(rows []row) (map[string][]model.Observation, error) {
	sums := map[string]map[time.Time]float64{}
	for _, r := range rows {
		if r["security_market"] != "Marketable" {
			continue
		}
		var bills bool
		switch r["security_type"] {
		case "Bills":
			bills = true
		case "Notes", "Bonds":
		default:
			continue
		}
		var id string
		switch r["transaction_type"] {
		case "Issues":
			id = SeriesCouponsIssued
			if bills {
				id = SeriesBillsIssued
			}
		case "Redemptions":
			id = SeriesCouponsRedeemed
			if bills {
				id = SeriesBillsRedeemed
			}
		default:
			continue
		}
		d, err := date(r["record_date"])
		if err != nil {
			return nil, err
		}
		v, ok, err := number(r["transaction_today_amt"])
		if err != nil {
			return nil, fmt.Errorf("%s %s %s: %w", r["record_date"], r["transaction_type"], r["security_type"], err)
		}
		if sums[id] == nil {
			sums[id] = map[time.Time]float64{}
		}
		if ok {
			sums[id][d] += v
		} else if _, seen := sums[id][d]; !seen {
			sums[id][d] = 0
		}
	}
	out := map[string][]model.Observation{}
	for id, byDate := range sums {
		for d, v := range byDate {
			out[id] = append(out[id], model.Observation{Date: d, Value: v})
		}
		sort.Slice(out[id], func(i, j int) bool { return out[id][i].Date.Before(out[id][j].Date) })
	}
	return out, nil
}
