package ingest

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/fred"
	"macro-collector/internal/store"
)

type fakeSeries struct {
	meta    fred.Meta
	obs     []model.Observation
	metaErr error
	obsErr  error
}

// fakeClient serves in-memory series and records FetchSeries calls.
type fakeClient struct {
	series map[string]*fakeSeries
	calls  map[string][]time.Time // id → since of each FetchSeries
	onMeta func(id string)
}

func (c *fakeClient) FetchMeta(ctx context.Context, id string) (fred.Meta, error) {
	if c.onMeta != nil {
		c.onMeta(id)
	}
	if err := ctx.Err(); err != nil {
		return fred.Meta{}, err
	}
	s := c.series[id]
	return s.meta, s.metaErr
}

func (c *fakeClient) FetchSeries(_ context.Context, id string, since time.Time) ([]model.Observation, error) {
	c.calls[id] = append(c.calls[id], since)
	s := c.series[id]
	if s.obsErr != nil {
		return nil, s.obsErr
	}
	var out []model.Observation
	for _, o := range s.obs {
		if !o.Date.Before(since) {
			out = append(out, o)
		}
	}
	return out, nil
}

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

var updated1 = time.Date(2026, 10, 2, 8, 24, 33, 0, time.FixedZone("", -5*3600))

func monthly(from string, n int, base float64) []model.Observation {
	var out []model.Observation
	d := day(from)
	for i := range n {
		out = append(out, model.Observation{Date: d.AddDate(0, i, 0), Value: base + float64(i)})
	}
	return out
}

func daily(from string, n int, base float64) []model.Observation {
	var out []model.Observation
	d := day(from)
	for i := range n {
		out = append(out, model.Observation{Date: d.AddDate(0, 0, i), Value: base + float64(i)/100})
	}
	return out
}

type fixture struct {
	t      *testing.T
	store  *store.Store
	client *fakeClient
	ing    *FRED
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	client := &fakeClient{
		series: map[string]*fakeSeries{
			"PAYEMS": {
				meta: fred.Meta{ID: "PAYEMS", Frequency: "m", Units: "Thousands of Persons", LastUpdated: updated1},
				obs:  monthly("2000-01-01", 321, 130000), // 2000-01 … 2026-09
			},
			"DGS10": {
				meta: fred.Meta{ID: "DGS10", Frequency: "d", Units: "Percent", LastUpdated: updated1},
				obs:  daily("2025-01-01", 600, 4),
			},
		},
		calls: map[string][]time.Time{},
	}
	ing := &FRED{
		Client: client,
		Store:  st,
		Series: []config.Series{
			{ID: "PAYEMS", Agent: "usa_monetary", Label: "Nonfarm Payrolls"},
			{ID: "DGS10", Agent: "usa_fiscal", Label: "UST 10Y"},
		},
		History: day("2000-01-01"),
		Log:     slog.New(slog.DiscardHandler),
		Now:     func() time.Time { return time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC) },
	}
	return &fixture{t: t, store: st, client: client, ing: ing}
}

func (f *fixture) count(query string) int {
	f.t.Helper()
	var n int
	if err := f.store.ReadDB().QueryRow(query).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) result(rep Report, id string) SeriesResult {
	f.t.Helper()
	for _, r := range rep.Results {
		if r.ID == id {
			return r
		}
	}
	f.t.Fatalf("no result for %s", id)
	return SeriesResult{}
}

func TestBackfillTwiceNoDuplicates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	rep := f.ing.Run(ctx, Backfill, time.Time{})
	if n := len(rep.Failed()); n != 0 {
		t.Fatalf("failed: %+v", rep.Failed())
	}
	if r := f.result(rep, "PAYEMS"); r.Inserted != 321 || !r.Since.Equal(day("2000-01-01")) {
		t.Errorf("PAYEMS = %+v", r)
	}
	total := f.count(`SELECT count(*) FROM observations`)
	if total != 321+600 {
		t.Errorf("rows = %d", total)
	}
	if n := f.count(`SELECT count(*) FROM series_meta WHERE units IS NOT NULL AND frequency IS NOT NULL`); n != 2 {
		t.Errorf("series_meta rows = %d", n)
	}
	st, ok, _ := f.store.FetchStateOf(ctx, "fred:PAYEMS")
	if !ok || st.Cursor != "2026-10-02 08:24:33-05" {
		t.Errorf("cursor = %+v", st)
	}

	rep = f.ing.Run(ctx, Backfill, time.Time{})
	if r := f.result(rep, "PAYEMS"); r.Inserted != 0 || r.Revised != 0 || r.Unchanged != 321 {
		t.Errorf("second backfill PAYEMS = %+v", r)
	}
	if n := f.count(`SELECT count(*) FROM observations`); n != total {
		t.Errorf("rows after repeat = %d, want %d", n, total)
	}
	if n := f.count(`SELECT count(*) FROM revisions`); n != 0 {
		t.Errorf("revisions = %d", n)
	}
}

func TestBackfillSinceOverride(t *testing.T) {
	f := newFixture(t)
	rep := f.ing.Run(context.Background(), Backfill, day("2026-01-01"))
	if r := f.result(rep, "PAYEMS"); r.Inserted != 9 {
		t.Errorf("PAYEMS since 2026 = %+v", r)
	}
}

func TestIncrementalSkipsUnchanged(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.ing.Run(ctx, Backfill, time.Time{})
	f.client.calls = map[string][]time.Time{}

	rep := f.ing.Run(ctx, Incremental, time.Time{})
	if rep.Count(OutcomeSkipped) != 2 || len(f.client.calls) != 0 {
		t.Errorf("skipped = %d, FetchSeries calls = %v", rep.Count(OutcomeSkipped), f.client.calls)
	}
}

func TestIncrementalRevisionWindow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.ing.Run(ctx, Backfill, time.Time{})
	f.client.calls = map[string][]time.Time{}

	// New release: PAYEMS adds October and revises August; DGS10 adds a day.
	updated2 := updated1.Add(31 * 24 * time.Hour)
	p := f.client.series["PAYEMS"]
	p.meta.LastUpdated = updated2
	p.obs[len(p.obs)-2].Value += 50
	p.obs = append(p.obs, model.Observation{Date: day("2026-10-01"), Value: 999999})
	g := f.client.series["DGS10"]
	g.meta.LastUpdated = updated2
	g.obs = append(g.obs, model.Observation{Date: g.obs[len(g.obs)-1].Date.AddDate(0, 0, 1), Value: 4.5})

	rep := f.ing.Run(ctx, Incremental, time.Time{})
	if len(rep.Failed()) != 0 {
		t.Fatalf("failed: %+v", rep.Failed())
	}
	// Monthly: last obs 2026-09-01 minus 5 years.
	if got := f.client.calls["PAYEMS"]; len(got) != 1 || !got[0].Equal(day("2021-09-01")) {
		t.Errorf("PAYEMS since = %v, want 2021-09-01", got)
	}
	// Daily: last obs 2026-08-23 (2025-01-01 + 599 days) minus 1 year.
	if got := f.client.calls["DGS10"]; len(got) != 1 || !got[0].Equal(day("2025-08-23")) {
		t.Errorf("DGS10 since = %v, want 2025-08-23", got)
	}
	if r := f.result(rep, "PAYEMS"); r.Inserted != 1 || r.Revised != 1 {
		t.Errorf("PAYEMS = %+v", r)
	}
	if n := f.count(`SELECT count(*) FROM revisions WHERE series_id = 'PAYEMS' AND obs_date = '2026-08-01'`); n != 1 {
		t.Errorf("August revision rows = %d", n)
	}
	st, _, _ := f.store.FetchStateOf(ctx, "fred:PAYEMS")
	if st.Cursor != updated2.Format("2006-01-02 15:04:05-07") {
		t.Errorf("cursor = %q", st.Cursor)
	}
}

func TestIncrementalWithoutDataStartsAtHistory(t *testing.T) {
	f := newFixture(t)
	f.ing.Run(context.Background(), Incremental, time.Time{})
	if got := f.client.calls["PAYEMS"]; len(got) != 1 || !got[0].Equal(day("2000-01-01")) {
		t.Errorf("since = %v, want history_start", got)
	}
}

func TestFailureIsolatedAndRecorded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.client.series["PAYEMS"].obsErr = errors.New("fred fetch PAYEMS: fred api status 500: boom")

	rep := f.ing.Run(ctx, Backfill, time.Time{})
	if rep.Count(OutcomeFailed) != 1 || rep.Count(OutcomeUpdated) != 1 {
		t.Fatalf("report = %+v", rep.Results)
	}
	st, ok, _ := f.store.FetchStateOf(ctx, "fred:PAYEMS")
	if !ok || st.Cursor != "" || !strings.Contains(st.LastError, "boom") {
		t.Errorf("state = %+v", st)
	}
	if n := f.count(`SELECT count(*) FROM observations WHERE series_id = 'PAYEMS'`); n != 0 {
		t.Errorf("PAYEMS rows = %d", n)
	}

	// Next run succeeds and clears the error.
	f.client.series["PAYEMS"].obsErr = nil
	f.ing.Run(ctx, Incremental, time.Time{})
	st, _, _ = f.store.FetchStateOf(ctx, "fred:PAYEMS")
	if st.LastError != "" || st.Cursor == "" {
		t.Errorf("state after recovery = %+v", st)
	}
}

func TestCancelStopsWithoutRecordingErrors(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Shutdown arrives while the second series is being fetched.
	f.client.onMeta = func(id string) {
		if id == "DGS10" {
			cancel()
		}
	}
	rep := f.ing.Run(ctx, Backfill, time.Time{})
	if !rep.Interrupted || len(rep.Results) != 1 || rep.Results[0].ID != "PAYEMS" || len(rep.Failed()) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if _, ok, _ := f.store.FetchStateOf(context.Background(), "fred:DGS10"); ok {
		t.Error("cancelled series recorded in fetch_state")
	}
	if st, _, _ := f.store.FetchStateOf(context.Background(), "fred:PAYEMS"); st.Cursor == "" {
		t.Error("series finished before cancel lost its cursor")
	}
}

func TestUnitsMismatchStopsSeries(t *testing.T) {
	f := newFixture(t)
	f.client.series["PAYEMS"].meta.Units = "Millions of Persons"
	rep := f.ing.Run(context.Background(), Backfill, time.Time{})
	r := f.result(rep, "PAYEMS")
	if r.Outcome != OutcomeFailed || r.Err == nil || !strings.Contains(r.Err.Error(), "units mismatch") {
		t.Errorf("PAYEMS = %+v", r)
	}
	if len(f.client.calls["PAYEMS"]) != 0 {
		t.Error("observations fetched despite units mismatch")
	}
	if n := f.count(`SELECT count(*) FROM series_meta WHERE series_id = 'PAYEMS'`); n != 0 {
		t.Errorf("series_meta written despite mismatch")
	}
}
