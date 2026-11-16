package edgar

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Fixture is one company's recorded responses in testdata/edgar; `macroctl
// record-fixtures --source edgar` refreshes them. The press releases are
// pinned: the golden tests are written against them.
type Fixture struct {
	Ticker, CIK string
	Releases    []string // accession numbers of 8-Ks whose index and Ex-99.1 are recorded
}

// Fixtures: the Q2 2026 earnings 8-K of every company, META's Q3 report and
// Tesla's Q3 deliveries 8-K (item 2.02 that is not earnings).
var Fixtures = []Fixture{
	{"NVDA", "0001045810", []string{"0001045810-26-000073"}},
	{"MSFT", "0000789019", []string{"0001193125-26-323632"}},
	{"AAPL", "0000320193", []string{"0000320193-26-000018"}},
	{"AMZN", "0001018724", []string{"0001018724-26-000024"}},
	{"META", "0001326801", []string{"0001628280-26-050596", "0001628280-26-066700"}},
	{"GOOGL", "0001652044", []string{"0001652044-26-000066"}},
	{"AVGO", "0001730168", []string{"0001730168-26-000076"}},
	{"TSLA", "0001318605", []string{"0001628280-26-066738", "0001628280-26-064366"}},
}

// Trimming keeps the fixtures small: filings since FixtureFilingsSince,
// facts of FixtureConcepts ending since FixtureFactsSince.
const (
	FixtureFilingsSince = "2024-07-01"
	FixtureFactsSince   = "2023-01-01"
)

// FixtureConcepts are every tag of edgar.concepts in config.yaml (LOOKUP E3,
// T1–T5, C1–C3): revenue, EPS, capex and the phase 5b/5c financials.
var FixtureConcepts = []string{"Revenues", "RevenueFromContractWithCustomerExcludingAssessedTax", "EarningsPerShareDiluted",
	"PaymentsToAcquirePropertyPlantAndEquipment", "PaymentsToAcquireProductiveAssets",
	"OperatingIncomeLoss", "NetIncomeLoss", "ProfitLoss", "GrossProfit", "CostOfRevenue",
	"DepreciationDepletionAndAmortization", "Depreciation", "AmortizationOfIntangibleAssets",
	"NetCashProvidedByUsedInOperatingActivities", "PaymentsForRepurchaseOfCommonStock",
	"PaymentsOfDividends", "PaymentsOfDividendsCommonStock", "PaymentsOfOrdinaryDividends",
	"CashAndCashEquivalentsAtCarryingValue", "MarketableSecuritiesCurrent", "DebtSecuritiesCurrent", "ShortTermInvestments",
	"LongTermDebt", "LongTermDebtNoncurrent", "LongTermDebtCurrent", "LongTermDebtAndCapitalLeaseObligations",
	"CommercialPaper", "ShortTermBorrowings", "DebtCurrent", "WeightedAverageNumberOfDilutedSharesOutstanding",
	"IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest",
	"IncomeLossFromContinuingOperationsBeforeIncomeTaxesMinorityInterestAndIncomeLossFromEquityMethodInvestments",
	"IncomeTaxExpenseBenefit", "RevenueRemainingPerformanceObligation"}

// RecordFixtures fetches every fixture into dir.
func (c *Client) RecordFixtures(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range Fixtures {
		sub, err := c.get(ctx, fmt.Sprintf("%s/submissions/CIK%s.json", c.dataURL, f.CIK))
		if err != nil {
			return fmt.Errorf("record %s submissions: %w", f.Ticker, err)
		}
		if sub, err = trimSubmissions(sub); err != nil {
			return fmt.Errorf("record %s submissions: %w", f.Ticker, err)
		}
		facts, err := c.get(ctx, fmt.Sprintf("%s/api/xbrl/companyfacts/CIK%s.json", c.dataURL, f.CIK))
		if err != nil {
			return fmt.Errorf("record %s companyfacts: %w", f.Ticker, err)
		}
		if facts, err = trimFacts(facts); err != nil {
			return fmt.Errorf("record %s companyfacts: %w", f.Ticker, err)
		}
		files := map[string][]byte{"submissions_" + f.Ticker + ".json": sub, "companyfacts_" + f.Ticker + ".json": facts}
		for _, accn := range f.Releases {
			base := c.FilingURL(f.CIK, accn)
			hdr, err := c.get(ctx, base+"/"+accn+"-index-headers.html")
			if err != nil {
				return fmt.Errorf("record %s %s: %w", f.Ticker, accn, err)
			}
			name, ok := ExhibitFile(hdr, "EX-99.1")
			if !ok {
				return fmt.Errorf("record %s %s: no EX-99.1", f.Ticker, accn)
			}
			ex, err := c.get(ctx, base+"/"+name)
			if err != nil {
				return fmt.Errorf("record %s %s: %w", f.Ticker, accn, err)
			}
			files[f.Ticker+"_"+accn+"-index-headers.html"] = hdr
			files[f.Ticker+"_"+accn+"_"+name] = ex
		}
		for name, b := range files {
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
				return err
			}
			c.log.Info("fixture recorded", "file", name, "bytes", len(b))
		}
	}
	return nil
}

func trimSubmissions(body []byte) ([]byte, error) {
	var d map[string]any
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, err
	}
	filings, _ := d["filings"].(map[string]any)
	recent, _ := filings["recent"].(map[string]any)
	dates, _ := recent["filingDate"].([]any)
	var keep []int
	for i, v := range dates {
		if s, _ := v.(string); s >= FixtureFilingsSince {
			keep = append(keep, i)
		}
	}
	for k, v := range recent {
		col, _ := v.([]any)
		out := make([]any, 0, len(keep))
		for _, i := range keep {
			if i < len(col) {
				out = append(out, col[i])
			}
		}
		recent[k] = out
	}
	if files, ok := filings["files"].([]any); ok && len(files) > 1 {
		filings["files"] = files[:1]
	}
	return json.Marshal(d)
}

func trimFacts(body []byte) ([]byte, error) {
	var d map[string]any
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, err
	}
	facts, _ := d["facts"].(map[string]any)
	gaap, _ := facts["us-gaap"].(map[string]any)
	kept := map[string]any{}
	for _, concept := range FixtureConcepts {
		cf, ok := gaap[concept].(map[string]any)
		if !ok {
			continue
		}
		units, _ := cf["units"].(map[string]any)
		for u, v := range units {
			arr, _ := v.([]any)
			var out []any
			for _, it := range arr {
				if m, _ := it.(map[string]any); m != nil && strings.Compare(fmt.Sprint(m["end"]), FixtureFactsSince) >= 0 {
					out = append(out, m)
				}
			}
			if len(out) == 0 {
				delete(units, u)
			} else {
				units[u] = out
			}
		}
		if len(units) > 0 {
			kept[concept] = cf
		}
	}
	d["facts"] = map[string]any{"us-gaap": kept}
	return json.Marshal(d)
}

// InstanceFixtures is how many of a company's latest 10-Q/10-K instances are
// recorded: as many as a backfill reads — the quarter (its YoY is in the
// same filing), a fiscal Q4 = year − 9 months, and the filing a year
// earlier for a balance date's YoY (AMZN's RPO).
const InstanceFixtures = 5

// SegmentSelect builds a company's instance selection from config-like
// values; it keeps fixtures and the collector on one rule.
func SegmentSelect(revenue, profit string, members []string) InstanceSelect {
	return InstanceSelect{Concepts: []string{revenue, profit}, Members: members}
}

// RecordInstanceFixtures records, trimmed, the instances of the latest
// InstanceFixtures 10-Q/10-K filings in the recorded submissions of each
// fixture company with a selection (phase 5d). Files:
// instance_<ticker>_<accession>.xml.
func (c *Client) RecordInstanceFixtures(ctx context.Context, dir string, sels map[string]InstanceSelect) error {
	for _, fx := range Fixtures {
		sel, ok := sels[fx.Ticker]
		if !ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "submissions_"+fx.Ticker+".json"))
		if err != nil {
			return fmt.Errorf("record %s instances: %w", fx.Ticker, err)
		}
		subs, err := ParseSubmissions(b)
		if err != nil {
			return fmt.Errorf("record %s instances: %w", fx.Ticker, err)
		}
		for _, f := range LatestPeriodic(subs, InstanceFixtures) {
			body, err := c.get(ctx, c.InstanceURL(fx.CIK, f))
			if err != nil {
				return fmt.Errorf("record %s instance %s: %w", fx.Ticker, f.Accession, err)
			}
			trimmed, err := TrimInstance(body, sel)
			if err != nil {
				return fmt.Errorf("record %s instance %s: %w", fx.Ticker, f.Accession, err)
			}
			name := "instance_" + fx.Ticker + "_" + f.Accession + ".xml"
			if err := os.WriteFile(filepath.Join(dir, name), trimmed, 0o644); err != nil {
				return err
			}
			c.log.Info("fixture recorded", "file", name, "bytes", len(trimmed), "of", len(body))
		}
	}
	return nil
}

// LatestPeriodic returns the newest n 10-Q/10-K filings, newest first.
func LatestPeriodic(subs Submissions, n int) []Filing {
	var out []Filing
	for _, f := range subs.Filings {
		if f.Form == "10-Q" || f.Form == "10-K" {
			out = append(out, f)
		}
	}
	slices.SortFunc(out, func(a, b Filing) int { return b.AcceptedAt.Compare(a.AcceptedAt) })
	return out[:min(n, len(out))]
}

// TrimInstance keeps of an instance only what ParseInstance may use: the
// root element, the units, the RPO facts, the segment-axis facts of the
// selected concepts and their contexts — as the original XML, byte for
// byte, so tests parse the real format.
func TrimInstance(body []byte, sel InstanceSelect) ([]byte, error) {
	contexts, _, raw, err := scan(body, append(slices.Clone(sel.Concepts), RPOConcept))
	if err != nil {
		return nil, err
	}
	keepFact := func(concept, ctx string) bool {
		if concept == RPOConcept {
			return true
		}
		_, seg := contexts[ctx].explicit[segmentAxis]
		return seg && slices.Contains(sel.Concepts, concept)
	}
	keepCtx := map[string]bool{}
	for _, r := range raw {
		if keepFact(r.concept, r.ctx) {
			keepCtx[r.ctx] = true
		}
	}
	var out bytes.Buffer
	var root string
	d := xml.NewDecoder(bytes.NewReader(body))
	for {
		off := d.InputOffset()
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if root == "" {
			start := bytes.TrimLeft(body[off:d.InputOffset()], " \t\r\n")
			out.Write(start)
			out.WriteByte('\n')
			name := start[1:]
			if i := bytes.IndexAny(name, " \t\r\n>"); i >= 0 {
				name = name[:i]
			}
			root = string(name)
			continue
		}
		keep := false
		switch {
		case se.Name.Local == "unit":
			keep = true
		case se.Name.Local == "context":
			for _, a := range se.Attr {
				keep = keep || a.Name.Local == "id" && keepCtx[a.Value]
			}
		case strings.Contains(se.Name.Space, "us-gaap"):
			for _, a := range se.Attr {
				keep = keep || a.Name.Local == "contextRef" && keepCtx[a.Value] && keepFact(se.Name.Local, a.Value)
			}
		}
		if err := d.Skip(); err != nil {
			return nil, err
		}
		if keep {
			out.Write(bytes.TrimLeft(body[off:d.InputOffset()], " \t\r\n"))
			out.WriteByte('\n')
		}
	}
	out.WriteString("</" + root + ">\n")
	return out.Bytes(), nil
}
