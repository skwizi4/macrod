package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/model"
)

func TestCompanyFactsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	facts := []model.CompanyFact{
		{Concept: "PaymentsToAcquirePropertyPlantAndEquipment", Unit: "USD", Start: d("2025-07-01"), End: d("2026-06-30"),
			Value: 115.948e9, Accession: "0001193125-26-323660", Form: "10-K", Filed: d("2026-07-29"), FY: 2026, FP: "FY", Frame: "CY2026"},
		{Concept: "EarningsPerShareDiluted", Unit: "USD/shares", Start: d("2026-04-01"), End: d("2026-06-30"),
			Value: 4.12, Accession: "a", Form: "10-K", Filed: d("2026-07-29")},
		{Concept: "Assets", Unit: "USD", End: d("2026-06-30"), Value: 1, Accession: "a", Form: "10-K", Filed: d("2026-07-29")}, // instant
	}
	write := func(fs []model.CompanyFact) int {
		var n int
		if err := s.Write(ctx, func(tx *sql.Tx) error {
			var err error
			n, err = UpsertCompanyFacts(tx, "MSFT", fs, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := write(facts); n != 3 {
		t.Errorf("first write changed %d", n)
	}
	if n := write(facts); n != 0 {
		t.Errorf("same facts again changed %d", n)
	}
	got, err := s.CompanyFacts(ctx, "MSFT")
	if err != nil || len(got) != 3 {
		t.Fatalf("facts = %d, %v", len(got), err)
	}
	for _, f := range got {
		if f.Concept == "Assets" && !f.Start.IsZero() {
			t.Errorf("instant fact start = %s", f.Start)
		}
		if f.Frame == "CY2026" && (f.Value != 115.948e9 || !f.Start.Equal(d("2025-07-01")) || !f.Filed.Equal(d("2026-07-29")) || f.FY != 2026 || f.Ticker != "MSFT") {
			t.Errorf("capex fact = %+v", f)
		}
	}
	if ok, err := s.HasFactsFrom(ctx, "MSFT", "0001193125-26-323660"); !ok || err != nil {
		t.Errorf("HasFactsFrom = %v, %v", ok, err)
	}
	if ok, _ := s.HasFactsFrom(ctx, "MSFT", "nope"); ok {
		t.Error("HasFactsFrom unknown accession")
	}
}

func TestCompanyReports(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := model.CompanyReport{Ticker: "MSFT", PeriodEnd: d("2026-06-29"), Form: "8-K", ReportDate: d("2026-07-29"),
		Accession: "0001193125-26-323632", AcceptedAt: time.Date(2026, 7, 29, 20, 4, 53, 0, time.UTC), Session: "amc"}
	put := func(r model.CompanyReport) {
		if err := s.Write(ctx, func(tx *sql.Tx) error { return UpsertCompanyReport(tx, r) }); err != nil {
			t.Fatal(err)
		}
	}
	put(r)
	r.PeriodEnd = d("2026-06-30") // 10-K arrived: the estimated period is corrected
	put(r)
	got, ok, err := s.CompanyReport(ctx, r.Accession)
	if !ok || err != nil || !got.PeriodEnd.Equal(d("2026-06-30")) || got.Session != "amc" || !got.AcceptedAt.Equal(r.AcceptedAt) || got.DocumentID != 0 {
		t.Errorf("report = %+v, %v, %v", got, ok, err)
	}
	week, err := s.CompanyReports(ctx, d("2026-07-26"), d("2026-08-02"))
	if err != nil || len(week) != 1 {
		t.Errorf("week reports = %d, %v", len(week), err)
	}
	if out, _ := s.CompanyReports(ctx, d("2026-07-30"), d("2026-08-02")); len(out) != 0 {
		t.Errorf("reports outside window = %d", len(out))
	}
	if of, _ := s.CompanyReportsOf(ctx, "MSFT"); len(of) != 1 {
		t.Errorf("reports of MSFT = %d", len(of))
	}
}

// Instance facts keep the segment member; the same filing again changes
// nothing, a restatement by a later filing is a new row.
func TestInstanceFactsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	facts := []model.CompanyFact{
		{Member: "AmazonWebServicesSegmentMember", Concept: "RevenueFromContractWithCustomerExcludingAssessedTax", Unit: "USD",
			Start: d("2026-07-01"), End: d("2026-09-30"), Value: 46.0e9, Accession: "q3", Form: "10-Q", Filed: d("2026-10-30")},
		{Concept: "RevenueRemainingPerformanceObligation", Unit: "USD", End: d("2026-09-30"), Value: 578e9, Accession: "q3", Form: "10-Q", Filed: d("2026-10-30")},
	}
	write := func(fs []model.CompanyFact) int {
		var n int
		if err := s.Write(ctx, func(tx *sql.Tx) error {
			var err error
			n, err = UpsertInstanceFacts(tx, "AMZN", fs, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := write(facts); n != 2 {
		t.Errorf("first write changed %d", n)
	}
	if n := write(facts); n != 0 {
		t.Errorf("same facts again changed %d", n)
	}
	got, err := s.InstanceFacts(ctx, "AMZN")
	if err != nil || len(got) != 2 {
		t.Fatalf("facts = %+v, %v", got, err)
	}
	if got[0].Member != "" || !got[0].Start.IsZero() || got[1].Member != "AmazonWebServicesSegmentMember" || got[1].Value != 46.0e9 {
		t.Errorf("facts = %+v", got)
	}
}
