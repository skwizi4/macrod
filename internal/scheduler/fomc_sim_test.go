package scheduler

import (
	"context"
	"database/sql"
	"flag"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/export"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/store"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fedFixtures = "../../testdata/fed"

// fomcSite serves recorded federalreserve.gov pages; the statement of the
// simulated decision day is 404 until publishAt on the virtual clock.
type fomcSite struct {
	clock     Clock
	statement string
	publishAt time.Time
	mu        sync.Mutex
	hits      []time.Time // requests for the statement
}

func (s *fomcSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := path.Base(r.URL.Path)
	if name == s.statement {
		s.mu.Lock()
		s.hits = append(s.hits, s.clock.Now())
		s.mu.Unlock()
		if s.clock.Now().Before(s.publishAt) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}
	b, err := os.ReadFile(filepath.Join(fedFixtures, name))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Write(b)
}

type recordingSink struct{ got []export.Exported }

func (s *recordingSink) Exported(_ context.Context, e export.Exported) error {
	s.got = append(s.got, e)
	return nil
}

// TestFOMCDaySimulation replays the 2026-09-16 decision day on recorded
// pages: the July statement is in the database, the September one appears
// at 14:00:40 ET. The poller must find it on the next 20-second poll, store
// it, write fomc_diff.md (golden file) and its archive copy, record the
// export and hand it to the sink — once.
func TestFOMCDaySimulation(t *testing.T) {
	ctx := context.Background()
	ny, bts := nyLoc(t), mustLoc(t, "Europe/Bratislava")
	decision := time.Date(2026, 9, 16, 18, 0, 0, 0, time.UTC) // 14:00 EDT
	clock := &fakeClock{now: decision.Add(-90 * time.Second)}
	site := &fomcSite{clock: clock, statement: "monetary20260916a.htm", publishAt: decision.Add(40 * time.Second)}
	srv := httptest.NewServer(site)
	defer srv.Close()

	exchange := t.TempDir()
	cfg := &config.Config{
		Location: bts,
		Export:   config.Export{ExchangeDir: exchange},
		FedTexts: config.FedTexts{BaseURL: srv.URL, PollInterval: 15 * time.Minute, Agent: "usa_monetary",
			Feeds:     []config.Feed{{Name: fedtexts.FeedPressMonetary, URL: srv.URL + "/feeds/press_monetary.xml"}},
			BeigeBook: srv.URL + "/monetarypolicy/publications/beige-book-default.htm"},
		Calendar: config.Calendar{ReleaseLocation: ny, RefreshCron: "0 5 * * *"},
		Regions: []config.Region{{ID: "usa", Agents: []config.Agent{{ID: "monetary", SeriesTags: []string{"usa_monetary"},
			Reports: []config.Report{{Kind: config.ReportFOMCDiff, File: "fomc_diff.md", Title: "USA Monetary"}}}}}},
	}
	st := openStore(t)
	client := fedtexts.New(httpx.WithInterval(0), httpx.WithRetries(0, time.Millisecond))
	sink := &recordingSink{}
	s := New(Deps{Config: cfg, Store: st, FRED: fakeFull{&fakeFRED{}}, Fed: client,
		Root: slog.New(slog.DiscardHandler), FetchCron: "0 6 * * *", Clock: clock, Sink: sink})
	s.Events.spawn = func(f func()) { f() }

	// The previous statement (July) is already stored.
	july, err := fedtexts.FetchStatement(ctx, client, srv.URL, time.Date(2026, 7, 29, 0, 0, 0, 0, ny), ny)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, _, err := store.UpsertDocument(tx, july, decision.AddDate(0, 0, -40))
		if err != nil {
			return err
		}
		_, err = store.SyncReleases(tx, fedtexts.SourceFOMC, fedtexts.NameDecision, decision.AddDate(0, 0, -1), decision.AddDate(0, 0, 1),
			[]model.ReleaseEvent{{Source: fedtexts.SourceFOMC, Name: fedtexts.NameDecision, ScheduledAt: decision,
				Region: "usa", Importance: "high", Related: model.Related{Meeting: "2026-09-15/16", SEP: true}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	s.Events.Dispatch(ctx) // 13:58:30 ET: within the lead, polling starts 13:59:00

	// Polls 13:59:00, :20, :40, 14:00:00, :20 get 404; 14:00:40 finds it.
	var hits []string
	for _, h := range site.hits {
		hits = append(hits, h.In(ny).Format("15:04:05"))
	}
	if strings.Join(hits, " ") != "13:59:00 13:59:20 13:59:40 14:00:00 14:00:20 14:00:40" {
		t.Fatalf("statement requests at %v", hits)
	}
	evs, _ := st.Releases(ctx, decision, decision.Add(time.Second))
	if len(evs) != 1 || evs[0].Status != model.StatusDone || !evs[0].DoneAt.Equal(site.publishAt) {
		t.Fatalf("event = %+v", evs)
	}
	docs, _ := st.LatestDocuments(ctx, fedtexts.Source, fedtexts.DocStatement, 5)
	if len(docs) != 2 || docs[0].ExternalID != "monetary20260916a" || !docs[0].FetchedAt.Equal(site.publishAt) {
		t.Fatalf("statements = %+v", docs)
	}

	// fomc_diff.md (golden), its archive copy, the exports row and the sink.
	got, err := os.ReadFile(filepath.Join(exchange, "usa", "monetary", "fomc_diff.md"))
	if err != nil {
		t.Fatal(err)
	}
	gotText := strings.ReplaceAll(string(got), srv.URL, "https://www.federalreserve.gov")
	golden := filepath.Join("..", "..", "testdata", "export", "fomc_diff_20260916.md")
	if *update {
		if err := os.WriteFile(golden, []byte(gotText), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if gotText != string(want) {
		t.Errorf("fomc_diff.md differs from %s:\n%s", golden, gotText)
	}
	archived, err := os.ReadFile(filepath.Join(exchange, "archive", "2026-W38", "usa", "monetary", "fomc_diff.md"))
	if err != nil || string(archived) != string(got) {
		t.Errorf("archive copy: %v", err)
	}
	var week, kind, region string
	var created time.Time
	if err := st.ReadDB().QueryRow(`SELECT iso_week, kind, region, created_at FROM exports`).Scan(&week, &kind, &region, &created); err != nil ||
		week != "2026-W38" || kind != "event_fomc" || region != "usa" || !created.Equal(site.publishAt) {
		t.Errorf("exports row = %s %s %s %v, %v", week, kind, region, created, err)
	}
	if len(sink.got) != 1 || sink.got[0].Kind != "event_fomc" || len(sink.got[0].Files) != 4 {
		t.Errorf("sink = %+v", sink.got)
	}

	// The RSS job also sees the statement later: no second export.
	h := s.Events.Routes[2].Handler
	if done, err := h.Poll(ctx, evs[0]); !done || err != nil {
		t.Fatalf("repeat poll: %v %v", done, err)
	}
	var n int
	st.ReadDB().QueryRow(`SELECT count(*) FROM exports`).Scan(&n)
	if n != 1 || len(sink.got) != 1 {
		t.Errorf("exports = %d, sink calls %d", n, len(sink.got))
	}
}

func mustLoc(t *testing.T, name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
