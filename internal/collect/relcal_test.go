package collect

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/sources/finanzagentur"
	"macro-collector/internal/sources/relcal"
)

func calendarOf(t *testing.T, cfg *config.Config, name string) config.ReleaseCalendar {
	t.Helper()
	i := slices.IndexFunc(cfg.ReleaseCalendars, func(c config.ReleaseCalendar) bool { return c.Name == name })
	if i < 0 {
		t.Fatalf("no calendar %s in the preset", name)
	}
	return cfg.ReleaseCalendars[i]
}

func eventAt(evs []model.ReleaseEvent, name, utc string) (model.ReleaseEvent, bool) {
	i := slices.IndexFunc(evs, func(e model.ReleaseEvent) bool {
		return e.Name == name && e.ScheduledAt.Format("2006-01-02 15:04") == utc
	})
	if i < 0 {
		return model.ReleaseEvent{}, false
	}
	return evs[i], true
}

var (
	novFrom = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	janTo   = time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
)

// The events of the preset's calendars from the recorded files: times in
// the publisher's zone (CET 11:00 → 10:00 UTC), series of the release.
func TestReleaseCalendarEventsPreset(t *testing.T) {
	cfg := euPreset(t)
	region := cfg.RegionOfTag("eu_monetary")

	data, err := os.ReadFile("../../testdata/relcal/eurostat.ics")
	if err != nil {
		t.Fatal(err)
	}
	ics, err := relcal.ParseICS(data)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := ReleaseCalendarEvents(cfg, calendarOf(t, cfg, "eurostat"), ics, novFrom, janTo)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := eventAt(evs, "Флеш HICP еврозоны", "2026-12-01 10:00")
	if !ok || e.Source != "relcal:eurostat" || e.Region != region || e.Importance != "high" || !slices.Equal(e.Related.Series, []string{"HICP", "HICP_CORE"}) {
		t.Errorf("HICP flash = %+v, %v", e, ok)
	}
	if e, ok := eventAt(evs, "Госдолг стран ЕС (квартал)", "2027-01-22 10:00"); !ok || !slices.Equal(e.Related.Series, []string{"DE_DEBT_GDP", "FR_DEBT_GDP", "ES_DEBT_GDP", "IT_DEBT_GDP", "EA_DEBT_GDP"}) {
		t.Errorf("debt = %+v, %v", e, ok)
	}
	if _, ok := eventAt(evs, "Флеш HICP еврозоны", "2026-11-04 10:00"); ok {
		t.Error("event before the window kept")
	}
	for _, e := range evs {
		if e.Name == "" || e.Importance == "" {
			t.Errorf("unnamed event %+v", e)
		}
	}

	page, err := os.ReadFile("../../testdata/relcal/ecb_statscal.html")
	if err != nil {
		t.Fatal(err)
	}
	stats, err := relcal.ParseECBStats(page)
	if err != nil {
		t.Fatal(err)
	}
	evs, err = ReleaseCalendarEvents(cfg, calendarOf(t, cfg, "ecb_statscal"), stats, novFrom, janTo)
	if err != nil {
		t.Fatal(err)
	}
	// Frankfurt 10:00 CET = 09:00 UTC; one event although the page lists the dataset twice that day.
	if e, ok := eventAt(evs, "Денежные агрегаты еврозоны (M3, ЕЦБ)", "2026-11-26 09:00"); !ok || !slices.Equal(e.Related.Series, []string{"M3_YOY", "LOANS_NFC_YOY", "LOANS_HH_YOY"}) {
		t.Errorf("M3 = %+v, %v", e, ok)
	}

	// Dates from the config: ZEW at 11:05 Berlin; the EC consumer flash at its own time.
	evs, err = ReleaseCalendarEvents(cfg, calendarOf(t, cfg, "zew"), nil, novFrom, janTo)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := eventAt(evs, "ZEW (Германия)", "2026-11-17 10:05"); !ok || !slices.Equal(e.Related.Series, []string{"ZEW_SENTIMENT", "ZEW_SITUATION"}) {
		t.Errorf("ZEW = %+v, %v", e, ok)
	}
	evs, err = ReleaseCalendarEvents(cfg, calendarOf(t, cfg, "ec_survey"), nil, novFrom, janTo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eventAt(evs, "Флеш потребительского доверия еврозоны (ЕК)", "2026-11-20 15:00"); !ok {
		t.Errorf("EC consumer flash missing: %+v", evs)
	}
	if keys := ReleaseCalendarKeys(calendarOf(t, cfg, "ec_survey")); len(keys) != 2 || keys[0][0] != "relcal:ec_survey" {
		t.Errorf("keys = %v", keys)
	}
}

// Planned Bund auctions: only the securities the config lists, at the
// close of bidding (11:30 Berlin).
func TestBundAuctionEvents(t *testing.T) {
	cfg := euPreset(t)
	data, err := os.ReadFile("../../testdata/finanzagentur/Issuance_outlook_2026_Update_Q4_en.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	ps, err := finanzagentur.ParseOutlook(data)
	if err != nil {
		t.Fatal(err)
	}
	from, to := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	evs := BundAuctionEvents(cfg, ps, from, to)
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	for _, e := range evs {
		if e.Name[:6] == "Bubill" {
			t.Errorf("Bubill in the calendar: %+v", e)
		}
	}
	e, ok := eventAt(evs, "Bund 10 years", "2026-11-25 10:30")
	if !ok || e.Source != finanzagentur.SourceAuction || e.Related.CUSIP != "DE000BU2Z072" || e.Related.Tenor != "10Y" ||
		e.Related.Security != "Bund" || e.Importance != "medium" || e.Region != cfg.RegionOfTag(cfg.Finanzagentur.Agent) {
		t.Errorf("Bund 10Y = %+v, %v (events %+v)", e, ok, evs)
	}
}

type fakeGetter map[string][]byte

func (g fakeGetter) Get(_ context.Context, url string) ([]byte, error) {
	if b, ok := g[url]; ok {
		return b, nil
	}
	return nil, os.ErrNotExist
}

// A calendar that cannot be fetched fails alone; the others are synced.
func TestRefreshReleaseCalendars(t *testing.T) {
	cfg := euPreset(t)
	cfg.Calendar.HorizonDays = 30
	st := openStore(t)
	ics, _ := os.ReadFile("../../testdata/relcal/eurostat.ics")
	get := fakeGetter{calendarOf(t, cfg, "eurostat").URL: ics}
	sum := RefreshReleaseCalendars(context.Background(), cfg, st, get, nil, slog.New(slog.DiscardHandler))
	if sum.Errors["relcal:ecb_statscal"] == nil || sum.Errors["relcal:eurostat"] != nil {
		t.Errorf("errors = %v", sum.Errors)
	}
	// The window follows the clock: only that every calendar was tried.
	if len(sum.Calendars) != len(cfg.ReleaseCalendars) {
		t.Errorf("calendars = %+v", sum.Calendars)
	}
}
