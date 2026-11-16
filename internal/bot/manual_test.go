package bot

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/notify"
)

func ifoConfig(releases ...string) *config.Config {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	cfg := &config.Config{Location: time.UTC}
	var rel []time.Time
	for _, d := range releases {
		t, _ := time.Parse(time.DateOnly, d)
		rel = append(rel, t)
	}
	cfg.Manual.Series = []config.SourceSeries{
		{ID: "IFO_CLIMATE", Label: "Ifo Business Climate", Frequency: "m", Units: "Index", Releases: rel},
		{ID: "IFO_EXPECTATIONS", Label: "Ifo Expectations", Frequency: "m", Units: "Index", Releases: rel},
	}
	cfg.Manual.Reminder = &config.ManualReminder{Name: "Ifo", Series: []string{"IFO_CLIMATE", "IFO_EXPECTATIONS"}, FromDay: 20,
		Clock: config.ClockTime{Hour: 10, Minute: 5}, Location: berlin}
	return cfg
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestDue(t *testing.T) {
	for _, tt := range []struct {
		releases []string
		now      string
		want     bool
	}{
		{nil, "2026-11-19T09:05:00Z", false},                    // window starts on the 20th
		{nil, "2026-11-20T09:05:00Z", true},                     // 10:05 Berlin
		{nil, "2026-11-30T09:05:00Z", true},                     // still due until entered
		{[]string{"2026-11-24"}, "2026-11-23T09:05:00Z", false}, // the release day of the month wins over the window
		{[]string{"2026-11-24"}, "2026-11-24T09:05:00Z", true},
		{[]string{"2026-12-17"}, "2026-11-21T09:05:00Z", true}, // no date this month: window
		{[]string{"2026-12-17"}, "2026-12-17T09:05:00Z", true},
		{[]string{"2026-12-17"}, "2026-12-16T09:05:00Z", false},
		{nil, "2026-11-19T23:30:00Z", true}, // already the 20th in Berlin
	} {
		cfg := ifoConfig(tt.releases...)
		if got := Due(cfg.Manual.Reminder, cfg.ConfigReleaseDays(), at(tt.now)); got != tt.want {
			t.Errorf("Due(%v, %s) = %v, want %v", tt.releases, tt.now, got, tt.want)
		}
	}
}

func newManual(t *testing.T, cfg *config.Config, now *time.Time) (*Manual, *fakeTG) {
	t.Helper()
	fake := &fakeTG{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return &Manual{Config: cfg, Store: testStore(t), TG: notify.New(srv.URL, "1:t", "42"), Root: discard(), Log: discard(),
		Now: func() time.Time { return *now }}, fake
}

func TestRemindOncePerDayUntilEntered(t *testing.T) {
	ctx := context.Background()
	now := at("2026-11-19T09:05:00Z")
	m, fake := newManual(t, ifoConfig(), &now)
	steps := []struct{ now, want string }{
		{"2026-11-19T09:05:00Z", "not due"},
		{"2026-11-20T09:05:00Z", "sent"},
		{"2026-11-20T15:00:00Z", "sent today"},
		{"2026-11-23T09:05:00Z", "sent"},
	}
	for _, s := range steps {
		now = at(s.now)
		if got, err := m.Remind(ctx); err != nil || got != s.want {
			t.Fatalf("%s: Remind = %q, %v; want %q", s.now, got, err, s.want)
		}
	}
	calls, _ := fake.snapshot()
	if len(calls) != 2 || !strings.Contains(calls[0], "Ifo за ноябрь 2026") || !strings.Contains(calls[0], `"force_reply":true`) ||
		!strings.Contains(calls[0], `"input_field_placeholder":"88.4 91.2"`) || !strings.Contains(calls[0], `Шаблон: \u003c1\u003e \u003c2\u003e\nПример: 88.4 91.2`) ||
		!strings.Contains(calls[0], `1) Ifo Business Climate\n2) Ifo Expectations`) || !strings.Contains(calls[0], "ifo 88.4 91.2 2026-11") {
		t.Fatalf("calls %v", calls)
	}
	// The answer to the latest reminder (message 502) stores both values.
	reply, ok := m.Handle(ctx, &notify.Message{ID: 9, ChatID: "42", Text: "88,4 91.2", ReplyTo: 502})
	if !ok || !strings.Contains(reply, "✅ Ifo Business Climate = 88.4 (новое)") || !strings.Contains(reply, "✅ Ifo Expectations = 91.2 (новое)") {
		t.Fatalf("reply %v %q", ok, reply)
	}
	now = at("2026-11-24T09:05:00Z")
	if got, _ := m.Remind(ctx); got != "entered" {
		t.Fatalf("after input: %q", got)
	}
	obs, _ := m.Store.Observations(ctx, config.SourceManual, "IFO_EXPECTATIONS", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if len(obs) != 1 || obs[0].Value != 91.2 {
		t.Fatalf("stored %+v", obs)
	}
}

func TestHandleForms(t *testing.T) {
	ctx := context.Background()
	now := at("2026-11-25T12:00:00Z")
	m, _ := newManual(t, ifoConfig(), &now)
	for _, tt := range []struct {
		msg      notify.Message
		ok       bool
		contains string
	}{
		{notify.Message{Text: "привет"}, false, ""},
		{notify.Message{Text: "88 91", ReplyTo: 77}, false, ""}, // not a reply to a reminder
		{notify.Message{Text: "ifo 88.4"}, true, "нужно 2 числа"},
		{notify.Message{Text: "Ifo 88.4 abc"}, true, "«abc» — не число"},
		{notify.Message{Text: "ifo 88.4 91.2 2026-12"}, true, "in the future"},
		{notify.Message{Text: "ifo 87.9 90.5 2026-10"}, true, "Ifo 2026-10:"},
		{notify.Message{Text: "IFO -1,5 90.6 2026-10"}, true, "ревизия, было 87.9"},
		{notify.Message{Text: "ifo 1 2", Document: &notify.Document{FileID: "x"}}, false, ""},
	} {
		reply, ok := m.Handle(ctx, &tt.msg)
		if ok != tt.ok || !strings.Contains(reply, tt.contains) {
			t.Errorf("Handle(%q) = %v %q; want %v, %q", tt.msg.Text, ok, reply, tt.ok, tt.contains)
		}
	}
	obs, _ := m.Store.Observations(ctx, config.SourceManual, "IFO_CLIMATE", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if len(obs) != 1 || obs[0].Value != -1.5 {
		t.Fatalf("stored %+v", obs)
	}
	if obs, _ := m.Store.Observations(ctx, config.SourceManual, "IFO_CLIMATE", time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)); len(obs) != 0 {
		t.Fatalf("future stored %+v", obs)
	}
}

// Release days sent to the bot move the reminder to the day and are listed.
func TestReleaseDatesFromBot(t *testing.T) {
	ctx := context.Background()
	now := at("2026-11-16T10:00:00Z")
	m, fake := newManual(t, ifoConfig(), &now)
	for _, tt := range []struct{ text, want string }{
		{"ifo даты", "впереди: нет (напоминание — с 20-го числа)"},
		{"ifo даты 24.11.2026 2026-12-17 26.01.27", "✅ Добавлено 3, удалено 0. Даты выхода Ifo впереди: 24.11.2026, 17.12.2026, 26.01.2027."},
		{"Ifo даты 24.11.2026", "Добавлено 0"},
		{"ifo даты -26.01.2027", "удалено 1. Даты выхода Ifo впереди: 24.11.2026, 17.12.2026."},
		{"ifo даты завтра", "«завтра» — не дата. Ничего не записано."},
	} {
		reply, ok := m.Handle(ctx, &notify.Message{ChatID: "42", Text: tt.text})
		if !ok || !strings.Contains(reply, tt.want) {
			t.Errorf("%q → %v %q, want %q", tt.text, ok, reply, tt.want)
		}
	}
	// Stored dates count like release_dates: 20th–23rd are not due any more, the 24th is.
	now = at("2026-11-20T09:05:00Z")
	if got, _ := m.Remind(ctx); got != "not due" {
		t.Fatalf("20.11: %q", got)
	}
	now = at("2026-11-24T09:05:00Z")
	if got, _ := m.Remind(ctx); got != "sent" {
		t.Fatalf("24.11: %q", got)
	}
	days, _ := m.Store.ManualReleaseDays(ctx)
	if len(days["IFO_CLIMATE"]) != 2 || len(days["IFO_EXPECTATIONS"]) != 2 {
		t.Fatalf("stored %v", days)
	}
	calls, _ := fake.snapshot()
	if len(calls) != 1 || strings.Contains(calls[0], "Дат выхода Ifo впереди нет") {
		t.Fatalf("reminder with dates ahead asks for dates: %v", calls)
	}
}

// Without dates ahead the reminder asks for them.
func TestReminderAsksForDates(t *testing.T) {
	now := at("2026-11-20T09:05:00Z")
	m, fake := newManual(t, ifoConfig(), &now)
	if got, err := m.Remind(context.Background()); err != nil || got != "sent" {
		t.Fatal(got, err)
	}
	calls, _ := fake.snapshot()
	if !strings.Contains(calls[0], "Дат выхода Ifo впереди нет — пришлите «ifo даты 24.12.2026 26.01.2027»") {
		t.Fatalf("calls %v", calls)
	}
}
