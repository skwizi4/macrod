package export

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

func TestFOMCExportWithoutPreviousAndAfterWeekly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bts, _ := time.LoadLocation("Europe/Bratislava")
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 10, 28, 18, 0, 40, 0, time.UTC)
	dir := t.TempDir()
	x := &Events{Store: st, Config: testConfig(t), ExchangeDir: dir, Location: bts, ReleaseLoc: ny, Now: func() time.Time { return now }}

	// A weekly export on Sunday 2026-10-25 16:00 starts the period.
	weekly := time.Date(2026, 10, 25, 15, 0, 3, 0, time.UTC)
	if err := st.Write(ctx, func(tx *sql.Tx) error { return store.InsertExport(tx, "2026-W43", KindWeekly, "usa", weekly) }); err != nil {
		t.Fatal(err)
	}
	stmt := model.Document{ExternalID: "monetary20261028a", Title: "Federal Reserve issues FOMC statement",
		PublishedAt: time.Date(2026, 10, 28, 18, 0, 0, 0, time.UTC), Body: "The Committee decided."}
	if done, _ := x.Exported(ctx, stmt); done {
		t.Fatal("exported before export")
	}
	e, err := x.FOMC(ctx, stmt, model.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if e.ISOWeek != "2026-W44" || len(e.Files) != 4 {
		t.Fatalf("export = %+v", e)
	}
	b, err := os.ReadFile(filepath.Join(dir, "usa", "monetary", "fomc_diff.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"period: 2026-10-25 16:00 → 2026-10-28 19:00 (Europe/Bratislava)",
		"generated_at: 2026-10-28T19:00:40+01:00",
		"previous: —",
		"## Текст\n\nThe Committee decided.",
	} {
		if !strings.Contains(string(b), s) {
			t.Errorf("missing %q in\n%s", s, b)
		}
	}
	if done, _ := x.Exported(ctx, stmt); !done {
		t.Error("not marked exported")
	}
	// No temp files are left next to the export.
	entries, _ := os.ReadDir(filepath.Join(dir, "usa", "monetary"))
	if len(entries) != 1 {
		t.Errorf("files = %v", entries)
	}
}
