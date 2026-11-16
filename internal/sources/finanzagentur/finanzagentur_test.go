package finanzagentur

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/xlsx"
)

var today = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../testdata/finanzagentur/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func find(as []model.Auction, day, isin string) (model.Auction, bool) {
	i := slices.IndexFunc(as, func(a model.Auction) bool {
		return a.AuctionDate.Format(time.DateOnly) == day && a.CUSIP == isin
	})
	if i < 0 {
		return model.Auction{}, false
	}
	return as[i], true
}

func TestParseResultsRecorded(t *testing.T) {
	as, err := ParseResults(read(t, "emissionsergebnisse_aktuell_en.xlsx"), today)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 172 {
		t.Fatalf("auctions = %d, want 172", len(as))
	}
	// Bund 10Y of 2026-10-28 (LOOKUP 2026-11-11).
	a, ok := find(as, "2026-10-28", "DE000BU2Z072")
	if !ok {
		t.Fatal("Bund 10Y 2026-10-28 not found")
	}
	if a.Source != Source || a.Kind != "Bund" || a.Tenor != "10Y" || a.Term != "10 Y" || !a.Reopening || a.Process != ProcessAuction ||
		a.AvgYield.V != 3.37 || a.BidToCover.V != 1.1 || a.OfferingAmt.V != 5.5e9 || a.TotalAccepted.V != 3.89e9 ||
		a.BidsAmt.V != 4.46e9 || a.Retention.V != 1.61e9 || a.Coupon.V != 3 || a.ClosingTime != ClosingTime ||
		a.MaturityDate.Format(time.DateOnly) != "2036-08-15" {
		t.Errorf("Bund 10Y = %+v", a)
	}
	// A multi-ISIN Bubill auction of 2026-11-09: two rows, no coupon.
	b, ok := find(as, "2026-11-09", "DE000BU0E469")
	if !ok || b.Kind != "Bubill" || b.Process != ProcessMultiISIN || b.Coupon.OK || b.BidToCover.V != 1.7 || b.AvgYield.V != 2.907 {
		t.Errorf("Bubill = %+v, %v", b, ok)
	}
}

func TestParseHistoryRecorded(t *testing.T) {
	as, err := ParseResults(read(t, "emissionshistorie_en.xlsx"), today)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 2177 {
		t.Errorf("auctions = %d, want 2177 (taps into own holdings skipped)", len(as))
	}
	if as[0].AuctionDate.Format(time.DateOnly) != "1999-01-06" || as[0].Kind != "Bund" || as[0].AvgYield.V != 3.71 {
		t.Errorf("first = %+v", as[0])
	}
	for _, a := range as {
		if a.Process != ProcessAuction && a.Process != ProcessMultiISIN {
			t.Fatalf("process %q kept", a.Process)
		}
	}
}

// synthetic is a results sheet: the header split over rows as in the real
// file, then the rows, then the sum.
func synthetic(header [][]string, rows ...[]string) []byte {
	all := [][]string{{"Auction Results"}}
	all = append(all, header...)
	all = append(all, rows...)
	all = append(all, []string{"Sum:", "", "", "", "", "", "", "1000"})
	return xlsx.Build("Seite1", all)
}

var hdr = [][]string{
	{"", "", "ISIN", "", "", "", "", "Issuance", "", "", "", "Allotted", "", "", "Bid-to-"},
	{"No.", "Date", "", "Bond1", "Coupon", "Maturity", "Maturity-", "volume", "Type2", "Issuance", "Bids", "Volumes", "Average", "Retention", "Cover-"},
	{"", "", "", "", "", "", "segment", "(€ m)", "", "process6", "(€ m)", "(€ m)", "Yield (%)4", "Quote (€ m)", "Ratio5"},
}

var row = []string{"1", "46323", "DE000BU2Z072", "Bund", "0.03", "49902", "10 Y", "5500", "R", "Auc", "4460", "3890", "3.37", "1610", "1.1"}

func TestParseSynthetic(t *testing.T) {
	as, err := ParseResults(synthetic(hdr, row), today)
	if err != nil || len(as) != 1 || as[0].AvgYield.V != 3.37 || as[0].BidToCover.V != 1.1 {
		t.Fatalf("= %+v, %v", as, err)
	}

	// Columns found by header, not by position: swap Bids and Allotted.
	h := [][]string{slices.Clone(hdr[0]), slices.Clone(hdr[1]), slices.Clone(hdr[2])}
	r := slices.Clone(row)
	for _, x := range append(h, r) {
		x[10], x[11] = x[11], x[10]
	}
	as, err = ParseResults(synthetic(h, r), today)
	if err != nil || as[0].BidsAmt.V != 4.46e9 || as[0].TotalAccepted.V != 3.89e9 {
		t.Fatalf("swapped = %+v, %v", as, err)
	}

	// Taps into own holdings are skipped.
	oh := slices.Clone(row)
	oh[9] = "OH"
	if as, err = ParseResults(synthetic(hdr, row, oh), today); err != nil || len(as) != 1 {
		t.Errorf("OH = %d, %v", len(as), err)
	}
}

func TestParseRejects(t *testing.T) {
	noBTC := [][]string{hdr[0][:14], hdr[1][:14], hdr[2][:14]}
	badDate, future, badYield, badType := slices.Clone(row), slices.Clone(row), slices.Clone(row), slices.Clone(row)
	badDate[1] = "soon"
	future[1] = "46400"
	badYield[12] = "35"
	badType[8] = "X"
	for name, data := range map[string][]byte{
		"missing column": synthetic(noBTC, row[:14]),
		"bad date":       synthetic(hdr, row, badDate),
		"future date":    synthetic(hdr, future),
		"yield range":    synthetic(hdr, badYield),
		"type":           synthetic(hdr, badType),
		"truncated":      xlsx.Build("Seite1", append(append([][]string{{"x"}}, hdr...), row)),
		"no header":      xlsx.Build("Seite1", [][]string{row}),
	} {
		if as, err := ParseResults(data, today); err == nil {
			t.Errorf("%s: no error, %d auctions", name, len(as))
		} else if strings.TrimSpace(err.Error()) == "" {
			t.Errorf("%s: empty error", name)
		}
	}
}
