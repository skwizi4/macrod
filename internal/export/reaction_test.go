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
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
)

// seedRecordedBars stores the one-minute bars of W44 recorded in
// testdata/yahoo/intraday.
func seedRecordedBars(t *testing.T, st *store.Store, symbols ...string) {
	t.Helper()
	for _, sym := range symbols {
		name := "chart1m_" + strings.NewReplacer("^", "idx_", "=", "_", ".", "_").Replace(sym) + ".json"
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "yahoo", "intraday", name))
		if err != nil {
			t.Fatal(err)
		}
		bars, err := yahoo.ParseIntraday(b)
		if err != nil {
			t.Fatal(err)
		}
		rows := make([]store.IntradayBar, len(bars))
		for i, x := range bars {
			rows[i] = store.IntradayBar{TS: x.TS, Close: x.Close}
		}
		if err := st.Write(context.Background(), func(tx *sql.Tx) error {
			return store.UpsertIntradayBars(tx, sym, rows, time.Time{})
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// F1 on the recorded W44 bars: FOMC, the 08:30 releases of 29 Oct in one
// row with their surprises, AMZN after the close (its close is Friday's).
func TestReactionOnRecordedBars(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedRecordedBars(t, st, "NQ=F", "ZT=F", "DX-Y.NYB", "CL=F")
	s := seeder{t, st}
	s.events(
		model.ReleaseEvent{Source: "fed", Name: "FOMC decision", ScheduledAt: ts("2026-10-28T18:00:00Z"), Region: "usa", Importance: "high"},
		model.ReleaseEvent{Source: "fred", Name: "Gross Domestic Product", ScheduledAt: ts("2026-10-29T12:30:00Z"), Region: "usa", Importance: "high"},
		model.ReleaseEvent{Source: "fred", Name: "Personal Income and Outlays", ScheduledAt: ts("2026-10-29T12:30:00Z"), Region: "usa", Importance: "high"},
		model.ReleaseEvent{Source: "fred", Name: "Unemployment Insurance Weekly Claims Report", ScheduledAt: ts("2026-10-29T12:30:00Z"), Region: "usa", Importance: "high"},
		model.ReleaseEvent{Source: "earnings", Name: "AMZN earnings", ScheduledAt: ts("2026-10-29T20:00:00Z"), Region: "usa", Importance: "high",
			Related: model.Related{Ticker: "AMZN"}},
		model.ReleaseEvent{Source: "treasury", Name: "Refunding: Policy Statement", ScheduledAt: ts("2026-10-28T12:30:00Z"), Region: "usa", Importance: "high"},
	)
	seedPosts(t, st, []testPost{{source: "telegram:marketnews_ua", id: "500", at: "2026-10-29T12:31:00Z", tags: "usa_monetary:0.95",
		body: "🇺🇸Показники:\n\n▫️GDP Q3 (1st) 3.4% (очік. 3.2%🔼)\n\n▫️IJC 194 тис. (очік. 198 тис.🔽)"}})

	end, _ := PeriodEndOfWeek("2026-W44", cfg.Location)
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-11-01T15:00:04Z") }}
	w, err := x.newWeek(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	section, _, err := w.sentReaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := renderReport("sent_reaction", section)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + out)
	lines := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if cells := strings.Split(l, " | "); len(cells) > 3 {
			lines[strings.TrimPrefix(cells[0], "| ")] = l
		}
	}
	fomc, ok := lines["FOMC"]
	if !ok || !strings.Contains(fomc, "28.10 14:00") || strings.Count(fomc, "—") > 1 {
		t.Errorf("FOMC row: %q", fomc)
	}
	macro, ok := lines["GDP + PCE + Claims"]
	if !ok || !strings.Contains(macro, "29.10 08:30") || !strings.Contains(macro, "GDP Q3 (1st) 3.4% vs 3.2%") || !strings.Contains(macro, "IJC 194 тис. vs 198 тис.") {
		t.Errorf("08:30 row: %q", macro)
	}
	if strings.Contains(out, "Refunding") {
		t.Error("an event outside sentiment.reaction is shown")
	}
	// AMZN reported at 16:00 Thursday: the 30 minutes are recorded, the
	// move to Friday's close as well (bars to 30 Oct).
	amzn := lines["AMZN отчёт"]
	if !strings.Contains(amzn, "29.10 16:00") {
		t.Errorf("AMZN row: %q", amzn)
	}
	for _, row := range []string{fomc, macro, amzn} {
		if !strings.Contains(row, "%") || !strings.Contains(row, "≈") {
			t.Errorf("no NDX %% or 2Y ≈ b.p. in %q", row)
		}
	}
}
