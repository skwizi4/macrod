package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestIssuerReportDates(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	now := time.Date(2026, 11, 14, 8, 30, 0, 0, time.UTC)
	put := func(ds ...IssuerReportDate) (added int, moved []Moved) {
		t.Helper()
		if err := st.Write(ctx, func(tx *sql.Tx) (err error) {
			added, moved, err = UpsertIssuerReportDates(tx, ds, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	q1 := IssuerReportDate{Ticker: "SIE", Period: "Q1 FY2027", Date: day("2027-02-11"), Title: "1st quarter results", URL: "https://x"}
	fy := IssuerReportDate{Ticker: "SIE", Period: "Q4 FY2026", Date: day("2026-11-12"), Title: "4th quarter results", URL: "https://x"}
	if a, m := put(q1, fy); a != 2 || len(m) != 0 {
		t.Fatalf("first: %d %v", a, m)
	}
	now = now.Add(24 * time.Hour)
	q1.Date = day("2027-02-12")
	if a, m := put(q1, fy); a != 0 || len(m) != 1 || m[0].Period != "Q1 FY2027" || !m[0].Old.Equal(day("2027-02-11")) {
		t.Fatalf("second: %d %v", a, m)
	}
	got, err := st.IssuerReportDates(ctx, "SIE")
	if err != nil || len(got) != 2 || got[0].Period != "Q4 FY2026" || !got[1].Date.Equal(day("2027-02-12")) || !got[1].ChangedAt.Equal(now) || !got[0].FetchedAt.Equal(now) {
		t.Fatalf("stored %+v, %v", got, err)
	}
	if other, _ := st.IssuerReportDates(ctx, "DTE"); len(other) != 0 {
		t.Fatalf("other ticker %v", other)
	}
}
