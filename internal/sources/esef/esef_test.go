package esef

import (
	"os"
	"testing"
	"time"
)

func TestParseFilings(t *testing.T) {
	body, err := os.ReadFile("../../../testdata/esef/airbus_filings.json")
	if err != nil {
		t.Fatal(err)
	}
	fs, err := ParseFilings(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 5 {
		t.Fatalf("filings = %d, want 5", len(fs))
	}
	last := fs[len(fs)-1]
	if last.PeriodEnd.Format(time.DateOnly) != "2025-12-31" || last.Added.Format(time.DateOnly) != "2026-03-04" ||
		last.JSONURL != "/MINO79WLOO247M1IL051/2025-12-31/ESEF/NL/0/MINO79WLOO247M1IL051-2025-12-31-1-en.json" {
		t.Errorf("last filing %+v", last)
	}
}

// Airbus 2025 (LOOKUP 2026-11-11): revenue 73.42 bn €, EBIT (extension
// tag) 6.082 bn €; dimensioned facts and duplicates are dropped.
func TestParseFacts(t *testing.T) {
	body, err := os.ReadFile("../../../testdata/esef/airbus_2025.json")
	if err != nil {
		t.Fatal(err)
	}
	f := Filing{ID: "23733", PeriodEnd: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), Added: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)}
	facts, err := ParseFacts(body, f, "ifrs-full", []string{"Revenue", "airbus:ProfitLossBeforeFinancialResultAndIncomeTaxes",
		"ProfitLossAttributableToOwnersOfParent", "DilutedEarningsLossPerShare"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, x := range facts {
		if x.End.Format(time.DateOnly) == "2025-12-31" {
			if _, dup := got[x.Concept]; dup {
				t.Errorf("%s twice", x.Concept)
			}
			got[x.Concept] = x.Value
			if x.Start.Format(time.DateOnly) != "2025-01-01" || x.Form != Form || x.FY != 2025 || x.FP != "FY" {
				t.Errorf("fact %+v", x)
			}
		}
	}
	want := map[string]float64{"Revenue": 73.42e9, "airbus:ProfitLossBeforeFinancialResultAndIncomeTaxes": 6.082e9,
		"ProfitLossAttributableToOwnersOfParent": 5.221e9, "DilutedEarningsLossPerShare": 6.6}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(facts) != 8 {
		t.Errorf("facts = %d, want 8 (two years of four concepts)", len(facts))
	}
}
