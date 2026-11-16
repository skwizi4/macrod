package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestIntradayBarsUpsertRangeAndPrune(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := func(m int) time.Time { return time.Date(2026, 10, 29, 12, m, 0, 0, time.UTC) }
	old := IntradayBar{TS: at(0).AddDate(0, 0, -90), Close: 1}
	write := func(bars []IntradayBar, keep time.Time) {
		t.Helper()
		if err := s.Write(ctx, func(tx *sql.Tx) error { return UpsertIntradayBars(tx, "NQ=F", bars, keep) }); err != nil {
			t.Fatal(err)
		}
	}
	write([]IntradayBar{old, {at(29), 100}, {at(30), 101}, {at(31), 102}}, time.Time{})
	write([]IntradayBar{{at(30), 101.5}}, at(0).AddDate(0, 0, -60)) // revised bar, old one pruned
	got, err := s.IntradayBars(ctx, "NQ=F", at(0).AddDate(0, 0, -100), at(30))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Close != 101.5 || !got[1].TS.Equal(at(30)) {
		t.Errorf("bars = %+v", got)
	}
}
