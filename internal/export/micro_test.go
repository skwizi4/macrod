package export

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/edgar"
	"macro-collector/internal/sources/irtranscript"
	"macro-collector/internal/sources/nasdaq"
	"macro-collector/internal/sources/yahoo"
	"macro-collector/internal/store"
)

// recordedEDGAR serves testdata/edgar (recorded 2026-10-25, LOOKUP E1–E5).
type recordedEDGAR struct{ t *testing.T }

func (r recordedEDGAR) read(name string) []byte {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

func (r recordedEDGAR) ticker(cik string) string {
	for _, f := range edgar.Fixtures {
		if f.CIK == cik {
			return f.Ticker
		}
	}
	r.t.Fatalf("unknown cik %s", cik)
	return ""
}

func (r recordedEDGAR) Submissions(_ context.Context, cik string) (edgar.Submissions, error) {
	return edgar.ParseSubmissions(r.read("edgar/submissions_" + r.ticker(cik) + ".json"))
}

func (r recordedEDGAR) Facts(_ context.Context, cik string, concepts []string) ([]model.CompanyFact, error) {
	return edgar.ParseCompanyFacts(r.read("edgar/companyfacts_"+r.ticker(cik)+".json"), concepts)
}

func (r recordedEDGAR) PressRelease(_ context.Context, cik, accn string) (edgar.PressRelease, error) {
	matches, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "edgar", r.ticker(cik)+"_"+accn+"_*.htm"))
	if len(matches) == 0 {
		return edgar.PressRelease{}, errors.New("not recorded") // older quarters: the report is kept without its release
	}
	text, err := edgar.ReleaseText(r.read(filepath.Join("edgar", filepath.Base(matches[0]))))
	return edgar.PressRelease{Accession: accn, Text: text,
		URL: "https://www.sec.gov/Archives/edgar/data/x/" + strings.ReplaceAll(accn, "-", "") + "/" + filepath.Base(matches[0])}, err
}

// seedMicro loads the recorded EDGAR filings and Yahoo closes of the eight
// companies, as the collector does, plus the calendar of the season.
func seedMicro(t *testing.T, cfg *config.Config, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	s := seeder{t, st}
	ev := func(ticker, at, rt string, estimated bool) model.ReleaseEvent {
		c, _ := cfg.EDGAR.Company(ticker)
		return model.ReleaseEvent{Source: nasdaq.Source, Name: nasdaq.EventName(ticker), ScheduledAt: ts(at), Region: "usa",
			Importance: c.Importance, Related: model.Related{Ticker: ticker, ReportTime: rt, Estimated: estimated}}
	}
	// MSFT, AMZN and AVGO events come with seed (next week's calendar).
	s.events(
		ev("GOOGL", "2026-10-28T20:00:00Z", nasdaq.TimeAfterClose, false),
		ev("AAPL", "2026-11-02T21:00:00Z", nasdaq.TimeAfterClose, false),
		ev("NVDA", "2026-11-18T21:00:00Z", "", true),
		// Past reports, as the calendar had them: they tell the 8-K apart.
		ev("TSLA", "2026-10-21T20:00:00Z", nasdaq.TimeAfterClose, false),
		ev("META", "2026-10-21T20:00:00Z", nasdaq.TimeAfterClose, false),
	)
	e := &collect.EDGAR{Client: recordedEDGAR{t}, Store: st, Config: cfg, Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return ts("2026-10-25T05:00:00Z") }}
	rep := e.Run(ctx, ingest.Backfill)
	if rep.Interrupted || len(rep.Results) != 8 {
		t.Fatalf("edgar seed: %+v", rep)
	}
	for _, f := range edgar.Fixtures {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "yahoo", "chart_"+f.Ticker+".json"))
		if err != nil {
			t.Fatal(err)
		}
		series, err := yahoo.Parse(b, ts("2026-10-25T05:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		s.write(func(tx *sql.Tx) error {
			_, err := store.UpsertObservations(tx, "yahoo", f.Ticker, series.Observations(), ts("2026-10-25T05:00:00Z"))
			return err
		})
	}
	// Older press releases are not recorded: their errors are not this
	// test's subject.
	s.write(func(tx *sql.Tx) error {
		for _, f := range edgar.Fixtures {
			if err := store.SetFetchOK(tx, "edgar:"+f.Ticker, "x", ts("2026-10-25T05:00:00Z")); err != nil {
				return err
			}
		}
		return nil
	})
}

// A week without reports still shows capex and the season.
func TestMicroEmptyWeek(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	st, err := store.Open(context.Background(), cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedMicro(t, cfg, st)
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-10-18T14:00:04Z") }}
	end, _ := PeriodEndOfWeek("2026-W42", cfg.Location)
	files, _, err := x.Render(context.Background(), end)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, f := range mdFiles(files) {
		if f.Path == filepath.Join("usa", "micro", "current_week.md") {
			body = f.Body
		}
	}
	for _, want := range []string{"## 1. Отчёты за неделю\n\nОтчётов за неделю не было.", "| **Сумма 4** |", "## 3. Сезон отчётов Q3 2026",
		"Отчитались 0 из 8", "## 4. Guidance и комментарии руководства\n\nОтчётов за неделю не было."} {
		if !strings.Contains(body, want) {
			t.Errorf("W42 micro lacks %q:\n%s", want, body)
		}
	}
	// As of W42 META's Q3 10-Q (filed 10-22) is not there yet: Q3 2026 is not in the capex table.
	if strings.Contains(body, "Q3 2026 | QoQ") {
		t.Errorf("capex shows a quarter filed after the period:\n%s", body)
	}
}

func TestMicroSizeLimit(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	st, err := store.Open(context.Background(), cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedMicro(t, cfg, st)
	cfg.Export.MaxFileChars = 6000
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-10-25T15:00:04Z") }}
	end, _ := PeriodEndOfWeek("2026-W43", cfg.Location)
	files, _, err := x.Render(context.Background(), end)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range mdFiles(files) {
		if f.Path != filepath.Join("usa", "micro", "current_week.md") {
			continue
		}
		if strings.Contains(f.Body, "**Руководство:**") {
			t.Error("quotes kept over the limit")
		}
		if !strings.Contains(f.Body, "[обрезано, полный текст: https://www.sec.gov/") {
			t.Error("Outlook not cut with a link")
		}
		if !strings.Contains(f.Body, "| **Сумма 4** |") || !strings.Contains(f.Body, "| TSLA | Q3 2026") {
			t.Error("tables cut")
		}
	}
}

// W31: MSFT (2026-07-29) gives its outlook only on the call and has a
// stored transcript; AAPL (2026-07-30) has no public transcript.
func TestMicroCallOutlook(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedMicro(t, cfg, st)
	page, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ir", "msft_fy-2027_q1.html"))
	if err != nil {
		t.Fatal(err)
	}
	text, err := irtranscript.Text(page)
	if err != nil {
		t.Fatal(err)
	}
	const url = "https://www.microsoft.com/en-us/investor/events/fy-2026/earnings-fy-2026-q4"
	seeder{t, st}.write(func(tx *sql.Tx) error {
		_, _, err := store.UpsertDocument(tx, model.Document{Source: irtranscript.Source, ExternalID: "0001193125-26-323632", DocType: "transcript",
			Region: "usa", PublishedAt: ts("2026-07-29T20:04:00Z"), Author: "MSFT", Title: "MSFT call", Body: text, URL: url}, ts("2026-07-30T10:00:00Z"))
		return err
	})
	x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-08-02T14:00:04Z") }}
	end, _ := PeriodEndOfWeek("2026-W31", cfg.Location)
	files, _, err := x.Render(ctx, end)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, f := range mdFiles(files) {
		if f.Path == filepath.Join("usa", "micro", "current_week.md") {
			body = f.Body
		}
	}
	for _, want := range []string{
		"**Outlook со звонка (расшифровка, 2026-07-29):**\n• Now, moving to the outlook for our second quarter",
		"• Given our strong business performance, we expect",
		"Расшифровка: " + url,
		"**Outlook:** в пресс-релизе нет; только на звонке, расшифровка в открытом доступе не публикуется.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("W31 micro lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "OPERATOR") {
		t.Error("questions of the call in the export")
	}
	// Macro signals from the call (phase 5c); by topic priority (FIXES_W44
	// D2) demand takes 2 places, hiring 1: the 8-K sentence, added first.
	for _, want := range []string{
		"| MSFT | спрос | Customer demand continues to exceed available supply. | звонок 07-29 |",
		"| MSFT | спрос | That’s a great thing because, as I said in the call, we still have demand that exceeds supply. | звонок 07-29, Q&A |",
		"| MSFT | найм | These include a $3.2 billion gain from our investment in Anthropic",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("W31 micro lacks signal %q", want)
		}
	}
}

// W43 exported on Thursday 2026-10-22 morning: META and TSLA reported on
// Wednesday after the close, Thursday's close is not in yet → "ждём close
// 10-22"; exported on Sunday the same week has the reaction (phase 5c).
func TestMicroWaitingClose(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedMicro(t, cfg, st)
	end, _ := PeriodEndOfWeek("2026-W43", cfg.Location)
	render := func(now string) string {
		x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts(now) }}
		files, _, err := x.Render(ctx, end)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range mdFiles(files) {
			if f.Path == filepath.Join("usa", "micro", "current_week.md") {
				return f.Body
			}
		}
		return ""
	}
	seeder{t, st}.write(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM observations WHERE source = 'yahoo' AND series_id IN ('META', 'TSLA') AND obs_date >= '2026-10-22'`)
		return err
	})
	if body := render("2026-10-22T12:00:00Z"); !strings.Contains(body, "| ждём close 10-22 | ждём close 10-22 |") {
		t.Errorf("no waiting close on Thursday:\n%s", body)
	}
	// On Monday the close is still missing: data lost, not waiting.
	if body := render("2026-10-26T12:00:00Z"); strings.Contains(body, "ждём close 10-22") {
		t.Errorf("still waiting after the session:\n%s", body)
	}
}

// InstanceFacts reads a recorded, trimmed instance; none recorded: no facts.
func (r recordedEDGAR) InstanceFacts(_ context.Context, cik string, f edgar.Filing, sel edgar.InstanceSelect) ([]model.CompanyFact, error) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edgar", "instance_"+r.ticker(cik)+"_"+f.Accession+".xml"))
	if err != nil {
		return nil, nil
	}
	return edgar.ParseInstance(b, sel, f)
}

// Implied rest of the year (ТЗ §19.3a): shown from the week the guidance
// was given; W43 as of 2026-10-25 has GOOGL's capex up to Q2.
func TestMicroCapexGuidance(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedMicro(t, cfg, st)
	render := func(given string) string {
		g := config.CapexGuidance{Ticker: "GOOGL", Year: 2026, Low: 195, High: 205, Given: given, Source: "тест"}
		g.GivenAt, _ = time.Parse(time.DateOnly, given)
		cfg.EDGAR.CapexGuidance = []config.CapexGuidance{g}
		x := &Weekly{Store: st, Config: cfg, Now: func() time.Time { return ts("2026-10-25T15:00:04Z") }}
		end, _ := PeriodEndOfWeek("2026-W43", cfg.Location)
		files, _, err := x.Render(ctx, end)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range mdFiles(files) {
			if f.Path == filepath.Join("usa", "micro", "current_week.md") {
				return f.Body
			}
		}
		return ""
	}
	want := "GOOGL: остаток 2026 года (Q3–Q4) ≈ 114.4–124.4 млрд $ (guidance на 2026 год 195–205 − 80.6 за Q1–Q2; в Q2 — 44.9). " +
		"Guidance внесён вручную (тест); определение совпадает с нашим capex."
	if body := render("2026-07-23"); !strings.Contains(body, want) {
		t.Errorf("no guidance line %q", want)
	}
	if body := render("2026-10-28"); strings.Contains(body, "Guidance внесён вручную") {
		t.Error("guidance given after the period shown")
	}
	// Given after the Q2 8-K (07-22): current. Given before it:
	// the 8-K is newer, the export asks to update the config.
	if body := render("2026-07-23"); strings.Contains(body, "guidance устарел") {
		t.Error("current guidance marked stale")
	}
	stale := "GOOGL: ⚠ guidance устарел, обнови в config.yaml (edgar.capex_guidance): после него вышел отчёт 8-K 2026-07-22; " +
		"в конфиге — 2026 год, 195–205 млрд $, от 2026-04-24."
	if body := render("2026-04-24"); !strings.Contains(body, stale) {
		t.Errorf("no stale warning %q", stale)
	}
}
