package edgar

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// SAP files 20-F with ifrs-full facts (phase 11, LOOKUP 2026-11-11): the
// taxonomy is read, the 2017 USD convenience translation stays a USD fact.
func TestParseCompanyFactsIFRS(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(fixtures, "companyfacts_SAP.json"))
	if err != nil {
		t.Fatal(err)
	}
	facts, err := ParseCompanyFactsIn(body, "ifrs-full", []string{"Revenue", "DilutedEarningsLossPerShare", "NoSuchTag"})
	if err != nil {
		t.Fatal(err)
	}
	var fy25 float64
	units := map[string]bool{}
	for _, f := range facts {
		units[f.Unit] = true
		if f.Concept == "Revenue" && f.Unit == "EUR" && f.End.Equal(time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)) {
			fy25 = f.Value
			if f.Form != "20-F" || f.FP != "FY" {
				t.Errorf("form %s fp %s", f.Form, f.FP)
			}
		}
	}
	if fy25 != 36.8e9 {
		t.Errorf("SAP revenue 2025 = %v, want 36.8e9", fy25)
	}
	for _, u := range []string{"EUR", "EUR/shares", "USD"} {
		if !units[u] {
			t.Errorf("no %s facts", u)
		}
	}
	if us, err := ParseCompanyFacts(body, []string{"Revenue"}); err != nil || len(us) != 0 {
		t.Errorf("us-gaap facts of SAP = %d, %v; want none", len(us), err)
	}
}
