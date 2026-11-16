package status

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestBuild(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	fetchedAt := now.Add(-6 * time.Hour)

	put := func(source, id, freq string, obs []model.Observation) {
		t.Helper()
		err := st.Write(ctx, func(tx *sql.Tx) error {
			if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: source, SeriesID: id, Label: id, Agent: "usa_fiscal", Frequency: freq}); err != nil {
				return err
			}
			if _, err := store.UpsertObservations(tx, source, id, obs, fetchedAt); err != nil {
				return err
			}
			return store.SetFetchOK(tx, source+":"+id, "c", fetchedAt)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Fresh daily series with a future-dated observation (like IORB).
	put("fred", "IORB", "d", []model.Observation{{Date: day("2026-10-23"), Value: 3.9}, {Date: day("2026-10-26"), Value: 3.9}})
	// Quarterly series past its 120-day allowance.
	put("fred", "GFDEGDQ188S", "q", []model.Observation{{Date: day("2026-01-01"), Value: 120}})
	// Series that failed after an earlier success.
	put("fred", "DGS10", "d", []model.Observation{{Date: day("2026-10-23"), Value: 4.1}})
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		return store.SetFetchError(tx, "fred:DGS10", errors.New("fred api status 500"), now)
	}); err != nil {
		t.Fatal(err)
	}
	put("derived", "UST_2S10S", "d", []model.Observation{{Date: day("2026-10-23"), Value: 40}})

	cfg := &config.Config{
		FRED: config.FRED{Series: []config.Series{{ID: "IORB"}, {ID: "GFDEGDQ188S"}, {ID: "DGS10"}, {ID: "PAYEMS"}}},
		Derived: []config.Derived{
			{Name: "UST_2S10S"},
			{Name: "FED_PATH"}, // not implemented yet: not listed
		},
	}
	rep, err := Build(ctx, cfg, st, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SchemaVersion != 15 || rep.DBBytes == 0 {
		t.Errorf("db = v%d, %d bytes", rep.SchemaVersion, rep.DBBytes)
	}
	rows := map[string]Row{}
	for _, r := range rep.Rows {
		rows[r.Source+"/"+r.ID] = r
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %v", rep.Rows)
	}
	if r := rows["fred/IORB"]; !r.OK() || !r.LastObs.Equal(day("2026-10-23")) || !r.LastOK.Equal(fetchedAt) {
		t.Errorf("IORB = %+v (future date must be ignored)", r)
	}
	if r := rows["fred/GFDEGDQ188S"]; !r.Stale || r.OK() {
		t.Errorf("GFDEGDQ188S = %+v, want stale", r)
	}
	if r := rows["fred/DGS10"]; r.Error == "" || !r.LastOK.Equal(fetchedAt) || r.OK() {
		t.Errorf("DGS10 = %+v, want error with last success kept", r)
	}
	if r := rows["fred/PAYEMS"]; r.Fetched() || r.OK() {
		t.Errorf("PAYEMS = %+v, want never fetched", r)
	}
	if r := rows["derived/UST_2S10S"]; !r.OK() {
		t.Errorf("UST_2S10S = %+v", r)
	}

	sums := rep.Summaries()
	if len(sums) != 2 {
		t.Fatalf("summaries = %+v", sums)
	}
	f := sums[0]
	if f.Source != "fred" || f.Series != 4 || f.Errors != 1 || f.Stale != 1 || f.Never != 1 || !f.LastOK.Equal(fetchedAt) {
		t.Errorf("fred summary = %+v", f)
	}
}

func TestBuildCalendarAndTexts(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	ev := func(name string, at time.Time, importance string) model.ReleaseEvent {
		return model.ReleaseEvent{Source: "test", Name: name, ScheduledAt: at, Region: "usa", Importance: importance}
	}
	err = st.Write(ctx, func(tx *sql.Tx) error {
		for _, e := range []model.ReleaseEvent{
			ev("Claims", now.Add(-48*time.Hour), ""),
			ev("FOMC decision", now.Add(4*24*time.Hour), "high"),
			ev("H.15", now.Add(2*time.Hour), ""),
			ev("Far", now.Add(10*24*time.Hour), ""),
		} {
			if _, err := store.SyncReleases(tx, e.Source, e.Name, e.ScheduledAt, e.ScheduledAt.Add(time.Second), []model.ReleaseEvent{e}); err != nil {
				return err
			}
		}
		if err := store.SetFetchOK(tx, "calendar:fred", "2026-11-07", now.Add(-7*time.Hour)); err != nil {
			return err
		}
		if err := store.SetFetchError(tx, "fed:speeches", errors.New("503"), now.Add(-time.Hour)); err != nil {
			return err
		}
		_, _, err := store.UpsertDocument(tx, model.Document{Source: "fed", ExternalID: "x", DocType: "speech", PublishedAt: now, Body: "b"}, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := st.Releases(ctx, now.Add(-49*time.Hour), now.Add(-47*time.Hour))
	if err := st.Write(ctx, func(tx *sql.Tx) error { return store.SetReleaseStatus(tx, evs[0].ID, model.StatusTimeout, now) }); err != nil {
		t.Fatal(err)
	}

	rep, err := Build(ctx, &config.Config{}, st, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Calendars) != 1 || rep.Calendars[0].Source != "calendar:fred" || !rep.Calendars[0].LastOK.Equal(now.Add(-7*time.Hour)) {
		t.Errorf("calendars = %+v", rep.Calendars)
	}
	if len(rep.Texts) != 1 || rep.Texts[0].Error != "503" || !rep.Texts[0].LastOK.IsZero() {
		t.Errorf("texts = %+v", rep.Texts)
	}
	if rep.Documents["speech"] != 1 {
		t.Errorf("documents = %v", rep.Documents)
	}
	if len(rep.Upcoming) != 2 || rep.Upcoming[0].Name != "H.15" || rep.Upcoming[1].Name != "FOMC decision" {
		t.Errorf("upcoming = %+v", rep.Upcoming)
	}
	if len(rep.Timeouts) != 1 || rep.Timeouts[0].Name != "Claims" {
		t.Errorf("timeouts = %+v", rep.Timeouts)
	}
}

func TestBuildPhase3Sources(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		for _, s := range []struct{ source, id, freq, key string }{
			{"yahoo", "^NDX", "d", "yahoo:^NDX"},
			{"treasury", "TGA_DTS", "d", "treasury:dts_operating_cash"},
			{"cftc", "209742:oi", "w", "cftc"},
		} {
			if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: s.source, SeriesID: s.id, Label: s.id, Agent: "usa_master", Frequency: s.freq}); err != nil {
				return err
			}
			if _, err := store.UpsertObservations(tx, s.source, s.id, []model.Observation{{Date: day("2026-10-20"), Value: 1}}, at); err != nil {
				return err
			}
			if err := store.SetFetchOK(tx, s.key, "c", at); err != nil {
				return err
			}
		}
		if err := store.SetFetchError(tx, "treasury:auctions", errors.New("no such host"), at); err != nil {
			return err
		}
		if _, err := store.UpsertAuctions(tx, []model.Auction{
			{AuctionDate: day("2026-10-21"), SecurityType: "Bond", Kind: "Bond", Term: "20-Year", Tenor: "20-Year", CUSIP: "A", BidToCover: model.Some(2.7)},
			{AuctionDate: day("2026-10-22"), SecurityType: "Note", Kind: "TIPS", Term: "5-Year", Tenor: "5-Year", CUSIP: "B"},
		}, at); err != nil {
			return err
		}
		return store.InsertExport(tx, "2026-W42", "weekly", "usa", day("2026-10-18").Add(15*time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Regions:  []config.Region{{ID: "usa"}},
		Yahoo:    config.Yahoo{Symbols: []config.YahooSymbol{{Symbol: "^NDX", Agent: "usa_master"}, {Symbol: "^GDAXI", Agent: "eu_master"}}, TagRegion: map[string]string{"usa_master": "usa", "eu_master": "eu"}},
		Treasury: config.Treasury{Datasets: []config.TreasuryDataset{{Name: "dts_operating_cash", Enabled: true}}},
		CFTC:     config.CFTC{Enabled: true, Contracts: []config.CFTCContract{{Name: "NQ", Code: "209742", Agent: "usa_master"}}, TagRegion: map[string]string{"usa_master": "usa"}},
	}
	rep, err := Build(ctx, cfg, st, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Row{}
	for _, r := range rep.Rows {
		got[r.Source+"/"+r.ID] = r
	}
	if len(got) != 3 || !got["yahoo/^NDX"].OK() || !got["treasury/TGA_DTS"].LastOK.Equal(at) || !got["cftc/209742:oi"].OK() {
		t.Errorf("rows = %+v", rep.Rows)
	}
	if len(rep.Datasets) != 3 || rep.Datasets[0].Source != "cftc" || rep.Datasets[1].Error != "no such host" {
		t.Errorf("datasets = %+v", rep.Datasets)
	}
	if rep.AuctionsHeld != 1 || rep.AuctionsPending != 1 {
		t.Errorf("auctions = %d held, %d pending", rep.AuctionsHeld, rep.AuctionsPending)
	}
	if !rep.Exports[0].Weekly.Equal(day("2026-10-18").Add(15*time.Hour)) || !rep.Exports[0].FOMC.IsZero() {
		t.Errorf("exports = %+v", rep.Exports)
	}
}

// §17.7: a series with a calendar is judged by its releases, not by age.
func TestStaleFollowsReleaseCalendar(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	put := func(id, freq, last string, seen time.Time) {
		t.Helper()
		err := st.Write(ctx, func(tx *sql.Tx) error {
			if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: "fred", SeriesID: id, Label: id, Agent: "usa_fiscal", Frequency: freq}); err != nil {
				return err
			}
			_, err := store.UpsertObservations(tx, "fred", id, []model.Observation{{Date: day(last), Value: 1}}, seen)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	at := func(s string) time.Time { return day(s).Add(13 * time.Hour) }
	event := func(id, sched string, series ...string) model.ReleaseEvent {
		return model.ReleaseEvent{
			Source: "fred_release", Name: id, Region: "usa", ScheduledAt: at(sched),
			Status: model.StatusDone, Related: model.Related{Series: series},
		}
	}
	// 16 days old, but the release of 11-05 brought it.
	put("LAGGED", "w", "2026-10-24", at("2026-11-05"))
	// The release of 11-05 passed and nothing new came.
	put("MISSED", "w", "2026-10-24", at("2026-10-29"))
	// The release is still inside its 24-hour poll window: not late yet.
	put("POLLING", "w", "2026-10-24", at("2026-11-05"))
	// No calendar: the fixed table says 16 days is too old.
	put("NOCAL", "w", "2026-10-24", at("2026-11-05"))
	evs := []model.ReleaseEvent{
		event("r1", "2026-11-05", "LAGGED", "MISSED"),
		event("r2", "2026-11-05", "POLLING"),
		event("r3", "2026-11-09", "POLLING"),
	}
	err = st.Write(ctx, func(tx *sql.Tx) error {
		for _, e := range evs {
			if _, err := store.SyncReleases(tx, e.Source, e.Name, e.ScheduledAt, e.ScheduledAt.Add(time.Second), []model.ReleaseEvent{e}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{FRED: config.FRED{Series: []config.Series{{ID: "LAGGED"}, {ID: "MISSED"}, {ID: "POLLING"}, {ID: "NOCAL"}}}}
	rep, err := Build(ctx, cfg, st, now)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"LAGGED": false, "MISSED": true, "POLLING": false, "NOCAL": true}
	for _, r := range rep.Rows {
		if r.Stale != want[r.ID] {
			t.Errorf("%s stale = %v, want %v", r.ID, r.Stale, want[r.ID])
		}
	}
}
