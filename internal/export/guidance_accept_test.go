package export

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/guidance"
	"macro-collector/internal/sources/irtranscript"
	"macro-collector/internal/store"
)

// microBody renders usa/micro/current_week.md of week.
func microBody(t *testing.T, st *store.Store, cfg *config.Config, week, now string) string {
	t.Helper()
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts(now) }}
	end, _ := PeriodEndOfWeek(week, cfg.Location)
	files, _, err := x.Render(context.Background(), end)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range mdFiles(files) {
		if f.Path == filepath.Join("usa", "micro", "current_week.md") {
			return f.Body
		}
	}
	t.Fatal("no micro file")
	return ""
}

// Acceptance of ТЗ §21.6 п. 3: the candidate from the recorded GOOGL call
// shows no number until approved; approve → the implied rest of the year
// in Micro; reject → the export is as without the candidate.
func TestGuidanceCandidateApproveReject(t *testing.T) {
	t.Parallel()
	for _, approve := range []bool{true, false} {
		cfg := testConfig(t)
		cfg.EDGAR.CapexGuidance = nil // the hand-entered value would hide the candidate (same range, same day)
		cfg.EDGAR.GuidanceTickers = []string{"GOOGL"}
		ctx := context.Background()
		st, err := store.Open(ctx, cfg.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		seedMicro(t, cfg, st)
		const week, now = "2026-W43", "2026-10-25T15:00:04Z"
		before := microBody(t, st, cfg, week, now)
		if strings.Contains(before, "остаток 2026") || strings.Contains(before, "implied Q4") {
			t.Fatalf("guidance line without guidance:\n%s", before)
		}

		page, err := os.ReadFile("../../testdata/ir/googl_2026-Q3-Earnings-Call.html")
		if err != nil {
			t.Fatal(err)
		}
		text, err := irtranscript.Text(page)
		if err != nil {
			t.Fatal(err)
		}
		// Given before the week's end, so the week may show it (no hindsight).
		doc := guidance.Doc{Source: irtranscript.Source, Ref: "0001652044-26-000090", Title: "GOOGL earnings call transcript, FY2026 Q3",
			URL: "https://abc.xyz/investor/events/event-details/2026/2026-Q3-Earnings-Call/default.aspx", Given: d("2026-07-23"), Text: text}
		svc := &guidance.Service{Store: st, Config: cfg, Log: discardLog()}
		var cands []store.GuidanceCandidate
		if err := st.Write(ctx, func(tx *sql.Tx) (err error) {
			cands, err = guidance.Insert(tx, guidance.Candidates("GOOGL", doc, nil), ts(now))
			return err
		}); err != nil || len(cands) != 1 {
			t.Fatalf("candidates %+v, %v", cands, err)
		}
		if body := microBody(t, st, cfg, week, now); body != before {
			t.Fatal("a pending candidate changed the export")
		}
		if _, err := svc.Decide(ctx, cands[0].ID, approve, "cli"); err != nil {
			t.Fatal(err)
		}
		body := microBody(t, st, cfg, week, now)
		want := "GOOGL: остаток 2026 года (Q3–Q4) ≈ 114.4–124.4 млрд $ (guidance на 2026 год 195–205 − 80.6 за Q1–Q2; в Q2 — 44.9). " +
			"Guidance подтверждён пользователем (#1, GOOGL earnings call transcript, FY2026 Q3, 2026-07-23); определение совпадает с нашим capex."
		if approve && !strings.Contains(body, want) {
			t.Errorf("approved: no line %q in\n%s", want, body)
		}
		if !approve && body != before {
			t.Error("rejected candidate changed the export")
		}
		st.Close()
	}
}

// The approved value and config.yaml merge: the newer given wins; a value
// given after the period end is not shown; a stale approved value points
// to macroctl guidance, not to config.yaml.
func TestMicroGuidanceMerge(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedMicro(t, cfg, st)
	approve := func(low float64, given string) {
		t.Helper()
		c := store.GuidanceCandidate{Ticker: "GOOGL", Year: 2026, Low: low, High: low + 10, Quote: "q", URL: "u", DocSource: "transcript",
			DocRef: given, DocTitle: "call", Given: d(given)}
		if err := st.Write(ctx, func(tx *sql.Tx) error {
			id, _, err := store.InsertGuidanceCandidate(tx, c, ts("2026-10-25T00:00:00Z"))
			if err != nil {
				return err
			}
			_, err = store.DecideGuidanceCandidate(tx, id, true, "cli", ts("2026-10-25T00:00:00Z"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	setCfg := func(low float64, given string) {
		g := config.CapexGuidance{Ticker: "GOOGL", Year: 2026, Low: low, High: low + 10, Given: given, Source: "конфиг"}
		g.GivenAt = d(given)
		cfg.EDGAR.CapexGuidance = []config.CapexGuidance{g}
	}
	const week, now = "2026-W43", "2026-10-25T15:00:04Z"

	setCfg(195, "2026-07-23")
	approve(175, "2026-04-24") // older than config: config wins, and it is the hand-entered line
	if body := microBody(t, st, cfg, week, now); !strings.Contains(body, "195–205 − 80.6") || !strings.Contains(body, "Guidance внесён вручную (конфиг)") {
		t.Errorf("config should win:\n%s", body)
	}
	approve(185, "2026-07-24") // newer than config
	if body := microBody(t, st, cfg, week, now); !strings.Contains(body, "185–195 − 80.6") || !strings.Contains(body, "подтверждён пользователем (#2, call, 2026-07-24)") {
		t.Errorf("newer approval should win:\n%s", body)
	}
	approve(300, "2026-10-28") // after the period end: not shown
	if body := microBody(t, st, cfg, week, now); strings.Contains(body, "300–310") || !strings.Contains(body, "185–195") {
		t.Errorf("future approval shown:\n%s", body)
	}

	cfg.EDGAR.CapexGuidance = nil
	cfg2 := testConfig(t)
	st2, err := store.Open(ctx, cfg2.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	seedMicro(t, cfg2, st2)
	cfg2.EDGAR.CapexGuidance = nil
	st = st2
	approve(195, "2026-04-24") // before the Q2 8-K of 07-22: stale
	stale := "GOOGL: ⚠ guidance устарел: после него вышел отчёт 8-K 2026-07-22; подтверждён #1 — 2026 год, 195–205 млрд $, от 2026-04-24. Проверь кандидатов: macroctl guidance list."
	if body := microBody(t, st2, cfg2, week, now); !strings.Contains(body, stale) {
		t.Errorf("no stale line %q", stale)
	}
}

func d(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }
