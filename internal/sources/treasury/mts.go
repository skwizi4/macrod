package treasury

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/model"
)

const endpointMTS = "v1/accounting/mts/mts_table_1"

// FetchMTS returns monthly receipts, outlays and deficit (dollars, dated the
// first of the month) from the statements published for since and later.
// Each statement repeats every month of the current and previous fiscal
// year; the latest statement wins, so revisions come through.
func (c *Client) FetchMTS(ctx context.Context, since time.Time) (map[string][]model.Observation, error) {
	rows, err := c.fiscal(ctx, endpointMTS, url.Values{
		"fields": {"record_date,classification_desc,current_month_gross_rcpt_amt,current_month_gross_outly_amt,current_month_dfct_sur_amt,src_line_nbr"},
		"filter": {"record_date:gte:" + since.Format(time.DateOnly)},
		"sort":   {"record_date,src_line_nbr"},
	})
	if err != nil {
		return nil, fmt.Errorf("treasury mts: %w", err)
	}
	out, err := parseMTS(rows)
	if err != nil {
		return nil, fmt.Errorf("treasury mts: %w", err)
	}
	return out, nil
}

type mtsValue struct {
	statement time.Time
	v         float64
}

func parseMTS(rows []row) (map[string][]model.Observation, error) {
	// Rows of one statement in print order: "FY 2025", its months October…
	// September, "Year-to-Date", then "FY 2026" and its months.
	sorted := make([]row, len(rows))
	copy(sorted, rows)
	var parseErr error
	lineOf := func(r row) int {
		n, err := strconv.Atoi(r["src_line_nbr"])
		if err != nil && parseErr == nil {
			parseErr = fmt.Errorf("src_line_nbr %q: %w", r["src_line_nbr"], err)
		}
		return n
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i]["record_date"] != sorted[j]["record_date"] {
			return sorted[i]["record_date"] < sorted[j]["record_date"]
		}
		return lineOf(sorted[i]) < lineOf(sorted[j])
	})
	if parseErr != nil {
		return nil, parseErr
	}

	latest := map[string]map[time.Time]mtsValue{
		SeriesMTSReceipts: {}, SeriesMTSOutlays: {}, SeriesMTSDeficit: {},
	}
	fy, statementDate := 0, ""
	for _, r := range sorted {
		if r["record_date"] != statementDate {
			fy, statementDate = 0, r["record_date"]
		}
		desc := strings.TrimSpace(r["classification_desc"])
		if y, ok := strings.CutPrefix(desc, "FY "); ok {
			n, err := strconv.Atoi(y)
			if err != nil {
				return nil, fmt.Errorf("%s: fiscal year %q: %w", statementDate, desc, err)
			}
			fy = n
			continue
		}
		m, ok := monthByName[desc]
		if !ok {
			continue // Year-to-Date and other summary lines
		}
		if fy == 0 {
			return nil, fmt.Errorf("%s: month %s before a fiscal year header", statementDate, desc)
		}
		year := fy
		if m >= time.October {
			year-- // FY 2026 runs October 2025 – September 2026
		}
		month := time.Date(year, m, 1, 0, 0, 0, 0, time.UTC)
		st, err := date(statementDate)
		if err != nil {
			return nil, err
		}
		for id, field := range map[string]string{
			SeriesMTSReceipts: "current_month_gross_rcpt_amt",
			SeriesMTSOutlays:  "current_month_gross_outly_amt",
			SeriesMTSDeficit:  "current_month_dfct_sur_amt",
		} {
			v, ok, err := number(r[field])
			if err != nil {
				return nil, fmt.Errorf("%s %s %s: %w", statementDate, desc, field, err)
			}
			if !ok {
				continue
			}
			if prev, seen := latest[id][month]; !seen || !st.Before(prev.statement) {
				latest[id][month] = mtsValue{statement: st, v: v}
			}
		}
	}
	out := make(map[string][]model.Observation, len(latest))
	for id, byMonth := range latest {
		obs := make([]model.Observation, 0, len(byMonth))
		for m, v := range byMonth {
			obs = append(obs, model.Observation{Date: m, Value: v.v})
		}
		sort.Slice(obs, func(i, j int) bool { return obs[i].Date.Before(obs[j].Date) })
		out[id] = obs
	}
	return out, nil
}

var monthByName = func() map[string]time.Month {
	m := make(map[string]time.Month, 12)
	for i := time.January; i <= time.December; i++ {
		m[i.String()] = i
	}
	return m
}()
