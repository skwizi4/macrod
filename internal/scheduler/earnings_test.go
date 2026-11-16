package scheduler

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/edgar"
	"macro-collector/internal/sources/nasdaq"
	"macro-collector/internal/store"
)

// fakeTesla serves Tesla's recorded filings up to a cut-off time: the
// report evening (8-K, no 10-Q yet) and the next morning.
type fakeTesla struct {
	t    *testing.T
	upTo time.Time
}

func (f *fakeTesla) read(name string) []byte {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edgar", name))
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func (f *fakeTesla) Submissions(context.Context, string) (edgar.Submissions, error) {
	s, err := edgar.ParseSubmissions(f.read("submissions_TSLA.json"))
	s.Filings = slices.DeleteFunc(s.Filings, func(x edgar.Filing) bool { return x.AcceptedAt.After(f.upTo) })
	return s, err
}

func (f *fakeTesla) Facts(_ context.Context, _ string, concepts []string) ([]model.CompanyFact, error) {
	facts, err := edgar.ParseCompanyFacts(f.read("companyfacts_TSLA.json"), concepts)
	return slices.DeleteFunc(facts, func(x model.CompanyFact) bool { return x.Filed.After(f.upTo) }), err
}

func (f *fakeTesla) PressRelease(_ context.Context, _, accn string) (edgar.PressRelease, error) {
	if accn != "0001628280-26-066738" {
		return edgar.PressRelease{Accession: accn, Text: "Tesla Q2 2026 Update"}, nil // not recorded
	}
	text, err := edgar.ReleaseText(f.read("TSLA_0001628280-26-066738_exhibit9914.htm"))
	return edgar.PressRelease{Accession: accn, Text: text}, err
}

func TestEarningsPoll(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	cfg := testConfig(t)
	cfg.EDGAR = config.EDGAR{Enabled: true, History: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		Companies: []config.Company{{Ticker: "TSLA", CIK: "0001318605", Resolved: config.Concepts{
			Revenue: []string{"Revenues"}, EPS: []string{"EarningsPerShareDiluted"}, Capex: []string{"PaymentsToAcquirePropertyPlantAndEquipment"}}}}}
	client := &fakeTesla{t: t, upTo: time.Date(2026, 10, 21, 21, 0, 0, 0, time.UTC)}
	h := &Earnings{EDGAR: &collect.EDGAR{Client: client, Store: st, Config: cfg, Log: slog.New(slog.DiscardHandler)}}
	ev := model.ReleaseEvent{Source: nasdaq.Source, Name: nasdaq.EventName("TSLA"),
		ScheduledAt: time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC), Related: model.Related{Ticker: "TSLA"}, Status: model.StatusScheduled}
	// The poll runs because the event is in releases; the collector uses it
	// to tell the earnings 8-K from deliveries before the 10-Q is filed.
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := store.SyncReleases(tx, ev.Source, ev.Name, ev.ScheduledAt, ev.ScheduledAt.Add(time.Second), []model.ReleaseEvent{ev})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// 16:00 ET, before the 8-K: nothing yet.
	client.upTo = time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
	if done, err := h.Poll(ctx, ev); done || err != nil {
		t.Fatalf("before the 8-K: done %v, %v", done, err)
	}
	if got := h.Expire(ctx, ev); got != model.StatusTimeout {
		t.Errorf("expire without a report = %s", got)
	}
	// 17:00 ET: the 8-K is stored, the 10-Q is not filed: keep polling.
	client.upTo = time.Date(2026, 10, 21, 21, 0, 0, 0, time.UTC)
	if done, err := h.Poll(ctx, ev); done || err != nil {
		t.Fatalf("8-K without 10-Q: done %v, %v", done, err)
	}
	if got := h.Expire(ctx, ev); got != model.StatusDone {
		t.Errorf("expire with the 8-K = %s", got)
	}
	// Next morning: 10-Q and its facts are there.
	client.upTo = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	if done, err := h.Poll(ctx, ev); !done || err != nil {
		t.Fatalf("after the 10-Q: done %v, %v", done, err)
	}
	r, ok, _ := st.CompanyReport(ctx, "0001628280-26-066738")
	if !ok || r.PeriodEnd.Format(time.DateOnly) != "2026-09-30" || r.DocumentID == 0 {
		t.Errorf("report = %+v", r)
	}
}

func (f *fakeTesla) InstanceFacts(context.Context, string, edgar.Filing, edgar.InstanceSelect) ([]model.CompanyFact, error) {
	return nil, nil
}
