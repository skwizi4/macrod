package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/store"
)

const (
	ecbDecisionSep = "The Governing Council today decided to raise the three key ECB interest rates by 25 basis points. " +
		"Inflation is set to remain well above target for an extended period.\n\n" +
		"Accordingly, the interest rates on the deposit facility, the main refinancing operations and the marginal lending facility " +
		"will be increased to 2.50%, 2.65% and 2.90% respectively, with effect from 16 September 2026."
	ecbDecisionOct = "The Governing Council today decided to keep the three key ECB interest rates unchanged. " +
		"Energy prices have continued to rise.\n\n" +
		"The interest rates on the deposit facility, the main refinancing operations and the marginal lending facility " +
		"will remain unchanged at 2.50%, 2.65% and 2.90% respectively."
)

func euConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv(config.EnvDataDir, t.TempDir())
	cfg, err := presetConfigErr(t, "eu-equities", "eu", "")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func ecbDoc(id, docType string, at time.Time, body string) model.Document {
	title := map[string]string{ecbtexts.DocDecision: "Monetary policy decisions", ecbtexts.DocStatement: "Monetary policy statement, " + at.Format("2 January 2006"),
		ecbtexts.DocPressConference: "Q&A: Monetary policy statement, " + at.Format("2 January 2006")}[docType]
	return model.Document{Source: ecbtexts.Source, ExternalID: id, DocType: docType, Region: "eu", PublishedAt: at, Title: title,
		Body: body, URL: "https://www.ecb.europa.eu/press/" + id + ".en.html"}
}

func putDocs(t *testing.T, st *store.Store, docs ...model.Document) {
	t.Helper()
	if err := st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, d := range docs {
			if _, _, err := store.UpsertDocument(tx, d, d.PublishedAt); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// ECB decision day (phase 11): the decision and the statement introduction
// are each compared with the previous meeting's, the Q&A comes in full, the
// export is recorded once together with the decision.
func TestECBExport(t *testing.T) {
	cfg := euConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sep := time.Date(2026, 9, 10, 12, 15, 0, 0, time.UTC)
	oct := time.Date(2026, 10, 29, 13, 15, 0, 0, time.UTC)
	decision := ecbDoc("ecb.mp261029~9a493adf17", ecbtexts.DocDecision, oct, ecbDecisionOct)
	putDocs(t, st,
		ecbDoc("ecb.mp260910~314e508016", ecbtexts.DocDecision, sep, ecbDecisionSep),
		ecbDoc("ecb.is260910~6a45359cfc", ecbtexts.DocStatement, sep.Add(30*time.Minute), "Good afternoon.\n\nWe decided to raise rates."),
		decision,
		ecbDoc("ecb.is261029~5c5ffe99e5", ecbtexts.DocStatement, oct.Add(30*time.Minute), "Good afternoon.\n\nWe decided to keep rates unchanged."),
		ecbDoc("ecb.is261029~5c5ffe99e5#qa", ecbtexts.DocPressConference, oct.Add(30*time.Minute), "Will you hike in December?\n\nWe are not pre-committing."),
	)
	dir := t.TempDir()
	now := oct.Add(35 * time.Minute)
	x := &Events{Store: st, Config: cfg, ExchangeDir: dir, Location: cfg.Location, ReleaseLoc: cfg.Calendar.ReleaseLocation, Now: func() time.Time { return now }}
	if done, _ := x.ECBExported(ctx, decision); done {
		t.Fatal("exported before export")
	}
	e, err := x.ECB(ctx, decision)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != KindEventECB || e.Region != "eu" || e.ISOWeek != "2026-W44" || len(e.Files) != 4 {
		t.Fatalf("export = %+v", e)
	}
	b, err := os.ReadFile(filepath.Join(dir, "eu", "monetary_macro", "ecb_diff.md"))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join(repoRoot, "testdata", "export", "ecb_diff_20261029.md")
	if *update {
		if err := os.WriteFile(golden, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(want) {
		t.Errorf("ecb_diff.md differs from %s (run with -update):\n%s", golden, b)
	}
	if done, _ := x.ECBExported(ctx, decision); !done {
		t.Error("not marked exported")
	}
	last, ok, err := st.LastExport(ctx, KindEventECB, "eu")
	if err != nil || !ok || !last.Equal(now) {
		t.Errorf("exports row: %v %v %v", last, ok, err)
	}
	var m ECBDiffReport
	js, err := os.ReadFile(filepath.Join(dir, "json", "eu", "monetary_macro", "ecb_diff.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(js, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Decisions) != 2 || !m.Decisions[0].Parsed || m.Decisions[1].MoveBP != 25 || m.StatementDiff == nil || len(m.Documents) != 1 {
		t.Errorf("json model = %+v", m)
	}
}

// Without the statement by 15:30 the file says so; a decision whose wording
// is not understood shows no numbers.
func TestECBExportWithoutStatementUnparsed(t *testing.T) {
	cfg := euConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 12, 17, 13, 15, 0, 0, time.UTC)
	decision := ecbDoc("ecb.mp261217~0000000000", ecbtexts.DocDecision, at, "The Governing Council today decided to narrow the corridor.")
	putDocs(t, st, decision)
	x := &Events{Store: st, Config: cfg, ExchangeDir: t.TempDir(), Location: cfg.Location, ReleaseLoc: cfg.Calendar.ReleaseLocation,
		Now: func() time.Time { return at.Add(75 * time.Minute) }}
	if _, err := x.ECB(ctx, decision); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(x.ExchangeDir, "eu", "monetary_macro", "ecb_diff.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"statement: —",
		"- Заявление: не получено к 15:30",
		"| Решение | не распознано — см. текст решения |",
		"| Deposit facility (DFR) | не распознано |",
		"## Решение: текст\n\nThe Governing Council today decided to narrow the corridor.",
	} {
		if !strings.Contains(string(b), s) {
			t.Errorf("missing %q in\n%s", s, b)
		}
	}
	if strings.Contains(string(b), "%") {
		t.Errorf("a rate is shown for an unparsed decision:\n%s", b)
	}
}

// The weekly export refreshes ecb_diff.md; in a week without a meeting it
// opens with the age of the last one and the next date.
func TestECBWeeklyRefresh(t *testing.T) {
	cfg := euConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	oct := time.Date(2026, 10, 29, 13, 15, 0, 0, time.UTC)
	putDocs(t, st, ecbDoc("ecb.mp261029~9a493adf17", ecbtexts.DocDecision, oct, ecbDecisionOct))
	next := model.ReleaseEvent{Source: ecbtexts.SourceMeeting, Name: ecbtexts.NameDecision, ScheduledAt: time.Date(2026, 12, 17, 13, 15, 0, 0, time.UTC),
		Region: "eu", Importance: "high", Status: model.StatusScheduled}
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := store.SyncReleases(tx, next.Source, next.Name, oct, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), []model.ReleaseEvent{next})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 15, 15, 0, 0, 0, time.UTC)
	x := &Weekly{Store: st, Config: cfg, Region: &cfg.Regions[0], Now: func() time.Time { return now }}
	end, err := PeriodEndOfWeek("2026-W46", cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	var md string
	for _, f := range files {
		if f.Path == "eu/monetary_macro/ecb_diff.md" {
			md = f.Body
		}
	}
	for _, s := range []string{
		"⚠ Последнее заседание: 2026-10-29 (2 нед. назад). Следующее: 2026-12-17",
		"# EU Monetary & Macro — ECB decision — 2026-10-29",
		"- Заявление: не получено к 15:30",
	} {
		if !strings.Contains(md, s) {
			t.Errorf("missing %q in\n%s", s, md)
		}
	}
}
