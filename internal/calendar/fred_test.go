package calendar

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/fred"
	"macro-collector/internal/store"
)

var fixtureDir = filepath.Join("..", "..", "testdata", "fred")

// fredServer serves recorded fred/series/release and fred/releases/dates
// responses. dropRelease removes one release from the dates (a moved
// release); datesStatus != 0 fails the dates request.
type fredServer struct {
	t           *testing.T
	mu          sync.Mutex
	lookups     []string
	dropRelease int
	datesStatus int
}

func (s *fredServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch r.URL.Path {
	case "/fred/series/release":
		id := q.Get("series_id")
		s.mu.Lock()
		s.lookups = append(s.lookups, id)
		s.mu.Unlock()
		if id == "NOT_A_SERIES_ID" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write(s.fixture("error_bad_series"))
			return
		}
		w.Write(s.fixture("series_release_" + id))
	case "/fred/releases/dates":
		if q.Get("realtime_start") != "2026-10-26" || q.Get("realtime_end") != "2026-11-10" {
			s.t.Errorf("dates window %s..%s, want 2026-10-26..2026-11-10", q.Get("realtime_start"), q.Get("realtime_end"))
		}
		if s.datesStatus != 0 {
			w.WriteHeader(s.datesStatus)
			w.Write([]byte(`{"error_code":500,"error_message":"Internal Server Error"}`))
			return
		}
		var resp map[string]any
		if err := json.Unmarshal(s.fixture("releases_dates"), &resp); err != nil {
			s.t.Fatal(err)
		}
		var kept []any
		for _, d := range resp["release_dates"].([]any) {
			if int(d.(map[string]any)["release_id"].(float64)) != s.dropRelease {
				kept = append(kept, d)
			}
		}
		resp["release_dates"], resp["count"] = kept, len(kept)
		json.NewEncoder(w).Encode(resp)
	default:
		s.t.Errorf("unexpected path %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *fredServer) fixture(name string) []byte {
	b, err := os.ReadFile(filepath.Join(fixtureDir, name+".json"))
	if err != nil {
		s.t.Fatal(err)
	}
	return b
}

type env struct {
	srv   *fredServer
	store *store.Store
	cal   *FRED
}

// 2026-10-26 07:00 ET, a Monday; the window ends 2026-11-10 (horizon 15).
var monday = time.Date(2026, 10, 26, 11, 0, 0, 0, time.UTC)

func newEnv(t *testing.T, series ...string) *env {
	t.Helper()
	srv := &fredServer{t: t}
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	client, err := fred.New("abcdefghijklmnopqrstuvwxyz012345", fred.WithBaseURL(hs.URL+"/fred"),
		fred.WithInterval(0), fred.WithRetries(0, time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	cal := &config.Calendar{
		HorizonDays: 15, ReleaseLocation: ny, DefaultTime: config.ClockTime{Hour: 8, Minute: 30},
		Times:             map[int]config.ClockTime{18: {Hour: 16, Minute: 15}, 20: {Hour: 16, Minute: 30}, 192: {Hour: 10}},
		ReleaseImportance: map[int]string{10: "high", 20: "medium"},
	}
	var cfgSeries []config.Series
	for _, id := range series {
		cfgSeries = append(cfgSeries, config.Series{ID: id, Label: id, Agent: "usa_monetary"})
	}
	return &env{srv: srv, store: st, cal: &FRED{
		RegionOf: func(string) string { return "usa" },
		Client:   client, Store: st, Series: cfgSeries, Calendar: cal,
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return monday },
	}}
}

var allSeries = []string{"CPIAUCSL", "ICSA", "DGS10", "WALCL", "JTSJOL"}

func (e *env) events(t *testing.T) []model.ReleaseEvent {
	t.Helper()
	evs, err := e.store.Releases(context.Background(), monday.AddDate(0, 0, -1), monday.AddDate(0, 0, 30))
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func find(evs []model.ReleaseEvent, releaseID int) []model.ReleaseEvent {
	var out []model.ReleaseEvent
	for _, e := range evs {
		if e.Related.ReleaseID == releaseID {
			out = append(out, e)
		}
	}
	return out
}

func utc(evs []model.ReleaseEvent) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.ScheduledAt.UTC().Format(time.RFC3339))
	}
	return out
}

func TestRefreshBuildsEvents(t *testing.T) {
	e := newEnv(t, allSeries...)
	rep, err := e.cal.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Lookups != 5 || len(rep.LookupsFailed) != 0 || rep.Releases != 5 || rep.Events != 18 || rep.Inserted != 18 {
		t.Fatalf("report = %+v", rep)
	}
	evs := e.events(t)
	if len(evs) != 18 {
		t.Fatalf("stored events = %d", len(evs))
	}

	// Release times in ET, converted to UTC across the 2026-11-01 DST switch.
	want := map[int][]string{
		10:  {"2026-11-10T13:30:00Z"},                         // CPI, default 08:30 EST
		180: {"2026-10-29T12:30:00Z", "2026-11-05T13:30:00Z"}, // claims, 08:30 EDT → EST
		20:  {"2026-10-29T20:30:00Z", "2026-11-05T21:30:00Z"}, // H.4.1, 16:30
		192: {"2026-11-03T15:00:00Z"},                         // JOLTS, 10:00 EST
	}
	for rel, w := range want {
		if got := utc(find(evs, rel)); !slices.Equal(got, w) {
			t.Errorf("release %d: %v, want %v", rel, got, w)
		}
	}
	h15 := find(evs, 18)
	if len(h15) != 12 || h15[0].ScheduledAt.UTC().Format(time.RFC3339) != "2026-10-26T20:15:00Z" {
		t.Errorf("H.15: %d events, first %v", len(h15), utc(h15[:1]))
	}
	cpi := find(evs, 10)[0]
	if cpi.Source != SourceFRED || cpi.Name != "Consumer Price Index" || cpi.Region != "usa" ||
		cpi.Status != model.StatusScheduled || !slices.Equal(cpi.Related.Series, []string{"CPIAUCSL"}) {
		t.Errorf("CPI event = %+v", cpi)
	}
	// Importance from the config; releases not listed are low.
	for rel, w := range map[int]string{10: "high", 20: "medium", 18: "low"} {
		if got := find(evs, rel)[0].Importance; got != w {
			t.Errorf("release %d importance = %q, want %q", rel, got, w)
		}
	}

	// Release ids are stored: the next refresh looks nothing up and adds nothing.
	e.srv.lookups = nil
	rep, err = e.cal.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(e.srv.lookups) != 0 || rep.Lookups != 0 || rep.Inserted != 0 || rep.Updated != 18 || rep.Rescheduled != 0 {
		t.Fatalf("second refresh: lookups %v, report %+v", e.srv.lookups, rep)
	}
	st, ok, err := e.store.FetchStateOf(context.Background(), "calendar:fred")
	if err != nil || !ok || st.Cursor != "2026-11-10" || st.LastError != "" {
		t.Errorf("fetch_state = %+v ok=%v err=%v", st, ok, err)
	}
}

func TestRefreshMarksMovedReleaseRescheduled(t *testing.T) {
	e := newEnv(t, allSeries...)
	if _, err := e.cal.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	// CPI disappears from the window: the stored event keeps its name and
	// is marked rescheduled.
	e.srv.dropRelease = 10
	rep, err := e.cal.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rescheduled != 1 || rep.Releases != 4 {
		t.Fatalf("report = %+v", rep)
	}
	if cpi := find(e.events(t), 10); len(cpi) != 1 || cpi[0].Status != model.StatusRescheduled {
		t.Fatalf("CPI = %+v", cpi)
	}
}

func TestRefreshSkipsFailedLookup(t *testing.T) {
	e := newEnv(t, "ICSA", "NOT_A_SERIES_ID")
	rep, err := e.cal.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.LookupsFailed, []string{"NOT_A_SERIES_ID"}) || rep.Events != 2 {
		t.Fatalf("report = %+v", rep)
	}
	// The failed series is looked up again next time; ICSA is not.
	e.srv.lookups = nil
	if _, err := e.cal.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.srv.lookups, []string{"NOT_A_SERIES_ID"}) {
		t.Errorf("lookups = %v", e.srv.lookups)
	}
}

func TestRefreshRecordsDatesError(t *testing.T) {
	e := newEnv(t, "ICSA")
	e.srv.datesStatus = http.StatusInternalServerError
	if _, err := e.cal.Refresh(context.Background()); err == nil {
		t.Fatal("want error")
	}
	st, ok, err := e.store.FetchStateOf(context.Background(), "calendar:fred")
	if err != nil || !ok || !strings.Contains(st.LastError, "500") {
		t.Fatalf("fetch_state = %+v ok=%v err=%v", st, ok, err)
	}
	if n := len(e.events(t)); n != 0 {
		t.Errorf("events = %d", n)
	}
}
