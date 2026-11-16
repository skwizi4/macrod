package bot

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/export"
	"macro-collector/internal/ingest"
	"macro-collector/internal/notify"
	"macro-collector/internal/store"
)

// ReminderKey is the fetch_state row of the last manual reminder:
// cursor "<day>|<period>|<message id>".
const ReminderKey = "notify:manual_reminder"

// Manual reminds of the monthly manual series (Ifo) and takes the values
// typed in reply (phase 14c, D29). Values are stored like `macroctl
// input`: source manual, through the store's writer.
type Manual struct {
	Config *config.Config
	Store  *store.Store
	TG     *notify.Telegram
	Root   *slog.Logger
	Log    *slog.Logger
	Now    func() time.Time // nil: time.Now
}

func (m *Manual) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manual) rem() *config.ManualReminder { return m.Config.Manual.Reminder }

type reminderState struct {
	Day, Period string
	MessageID   int64
}

func (m *Manual) state(ctx context.Context) (reminderState, error) {
	st, ok, err := m.Store.FetchStateOf(ctx, ReminderKey)
	if err != nil || !ok {
		return reminderState{}, err
	}
	parts := strings.Split(st.Cursor, "|")
	if len(parts) != 3 {
		return reminderState{}, nil
	}
	id, _ := strconv.ParseInt(parts[2], 10, 64)
	return reminderState{Day: parts[0], Period: parts[1], MessageID: id}, nil
}

// Missing are the reminder series without a value for period (YYYY-MM).
func (m *Manual) Missing(ctx context.Context, period string) ([]config.SourceSeries, error) {
	var out []config.SourceSeries
	for _, id := range m.rem().Series {
		s, _ := m.Config.ManualSeries(id)
		date, err := ingest.ManualPeriod(s, period, m.now())
		if err != nil {
			return nil, err
		}
		obs, err := m.Store.Observations(ctx, config.SourceManual, id, date)
		if err != nil {
			return nil, err
		}
		if len(obs) == 0 || !obs[0].Date.Equal(date) {
			out = append(out, s)
		}
	}
	return out, nil
}

// Due tells whether today (in the reminder timezone) is a reminder day:
// from the first release day of the month in release_dates or, when the
// month has none, from from_day.
// days: the publication days of the config and the bot (export.ManualReleaseDays).
func Due(r *config.ManualReminder, days map[string][]time.Time, now time.Time) bool {
	local := now.In(r.Location)
	y, mo, d := local.Date()
	var first time.Time
	for _, id := range r.Series {
		for _, rd := range days[id] {
			if rd.Year() == y && rd.Month() == mo && (first.IsZero() || rd.Before(first)) {
				first = rd
			}
		}
	}
	if !first.IsZero() {
		return d >= first.Day()
	}
	return d >= r.FromDay
}

// Remind sends the reminder of the current month when the day is due, a
// value is missing and no reminder went out today. It returns what it did.
func (m *Manual) Remind(ctx context.Context) (string, error) {
	r := m.rem()
	now := m.now()
	local := now.In(r.Location)
	day, period := local.Format(time.DateOnly), local.Format("2006-01")
	days, err := export.ManualReleaseDays(ctx, m.Config, m.Store)
	if err != nil {
		return "", err
	}
	if !Due(r, days, now) {
		return "not due", nil
	}
	prev, err := m.state(ctx)
	if err != nil {
		return "", err
	}
	if prev.Day == day {
		return "sent today", nil
	}
	missing, err := m.Missing(ctx, period)
	if err != nil {
		return "", err
	}
	if len(missing) == 0 {
		return "entered", nil
	}
	text := m.reminderText(local)
	if next := nextDays(r, days, local); len(next) == 0 {
		text += fmt.Sprintf("\n\nДат выхода %s впереди нет — пришлите «%s даты 24.12.2026 26.01.2027», и напоминание придёт ровно в день выхода (без дат — с %d-го числа).",
			r.Name, r.Keyword(), r.FromDay)
	}
	id, err := m.TG.SendForceReply(ctx, text, m.example())
	if err != nil {
		return "", fmt.Errorf("manual reminder: %w", err)
	}
	if err := m.Store.Write(ctx, func(tx *sql.Tx) error {
		return store.SetFetchOK(tx, ReminderKey, fmt.Sprintf("%s|%s|%d", day, period, id), now)
	}); err != nil {
		return "", err
	}
	m.Log.Info("manual reminder sent", "period", period, "missing", len(missing), "message_id", id)
	return "sent", nil
}

var monthsRU = [...]string{"январь", "февраль", "март", "апрель", "май", "июнь", "июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"}

func (m *Manual) labels() []string {
	var out []string
	for _, id := range m.rem().Series {
		s, _ := m.Config.ManualSeries(id)
		out = append(out, s.Label)
	}
	return out
}

func (m *Manual) example() string {
	ex := []string{"88.4", "91.2", "87.0", "90.1"}
	n := min(len(m.rem().Series), len(ex))
	return strings.Join(ex[:n], " ")
}

func (m *Manual) reminderText(local time.Time) string {
	r := m.rem()
	head := fmt.Sprintf("%s за %s %d — введите значения.", r.Name, monthsRU[local.Month()-1], local.Year())
	return head + "\n\n" + m.howTo(local.Format("2006-01"))
}

// howTo is the answer format: what to send and in which order.
func (m *Manual) howTo(period string) string {
	r := m.rem()
	var b strings.Builder
	b.WriteString("Как ответить: нажмите «Ответить» на это сообщение и пришлите одну строку — числа через пробел, в таком порядке:\n")
	var slots []string
	for i, l := range m.labels() {
		fmt.Fprintf(&b, "%d) %s\n", i+1, l)
		slots = append(slots, fmt.Sprintf("<%d>", i+1))
	}
	fmt.Fprintf(&b, "\nШаблон: %s\nПример: %s\nДесятичный разделитель — точка или запятая.\n\nБез «Ответить» (или за другой месяц): %s %s %s",
		strings.Join(slots, " "), m.example(), r.Keyword(), m.example(), period)
	if r.Hint != "" {
		b.WriteString("\nГде взять: " + r.Hint)
	}
	return b.String()
}

// Help is the format line of the bot's help.
func (m *Manual) Help() string {
	r := m.rem()
	return fmt.Sprintf("%s: «Ответить» на напоминание строкой «%s» (%s) или «%s %s ГГГГ-ММ»; даты выхода — «%s даты 24.11.2026 17.12.2026»",
		r.Name, m.example(), strings.Join(m.labels(), "; "), r.Keyword(), m.example(), r.Keyword())
}

var periodRe = regexp.MustCompile(`^\d{4}-\d{2}$`)

// Handle takes a reply to the current reminder or a message starting with
// the keyword. A wrong count or a non-number is answered and stores
// nothing; every value is checked before the first is written.
func (m *Manual) Handle(ctx context.Context, msg *notify.Message) (string, bool) {
	if msg.Document != nil {
		return "", false
	}
	r := m.rem()
	words := strings.Fields(msg.Text)
	st, err := m.state(ctx)
	if err != nil {
		m.Log.Error("manual reminder state", "err", err)
	}
	if len(words) >= 2 && strings.ToLower(words[0]) == r.Keyword() && datesWords[strings.ToLower(words[1])] {
		return m.dates(ctx, words[2:]), true
	}
	var period string
	switch {
	case msg.ReplyTo != 0 && msg.ReplyTo == st.MessageID:
		period = st.Period
		if len(words) > 0 && strings.ToLower(words[0]) == r.Keyword() {
			words = words[1:]
		}
	case len(words) > 0 && strings.ToLower(words[0]) == r.Keyword():
		words = words[1:]
		period = m.now().In(r.Location).Format("2006-01")
		if n := len(words); n > 0 && periodRe.MatchString(words[n-1]) {
			period, words = words[n-1], words[:n-1]
		}
	default:
		return "", false
	}
	usage := fmt.Sprintf("Ничего не записано: нужно %d числа через пробел.\n\n%s", len(r.Series), m.howTo(period))
	if len(words) != len(r.Series) {
		return usage, true
	}
	values := make([]float64, len(words))
	for i, w := range words {
		v, err := strconv.ParseFloat(strings.Replace(w, ",", ".", 1), 64)
		if err != nil {
			return fmt.Sprintf("«%s» — не число. %s", w, usage), true
		}
		values[i] = v
	}
	for _, id := range r.Series {
		s, _ := m.Config.ManualSeries(id)
		if _, err := ingest.ManualPeriod(s, period, m.now()); err != nil {
			return "Период: " + err.Error() + ". Ничего не записано.", true
		}
	}
	lines := []string{fmt.Sprintf("%s %s:", r.Name, period)}
	ok := true
	for i, id := range r.Series {
		res, err := collect.ManualInput(ctx, m.Config, m.Store, m.Root, id, values[i], period, m.now())
		if err != nil {
			ok = false
			m.Log.Error("manual input from telegram", "series", id, "period", period, "err", err)
			lines = append(lines, fmt.Sprintf("❌ %s — ошибка записи: %v", id, err))
			continue
		}
		what := "новое"
		switch {
		case res.Revised > 0:
			what = fmt.Sprintf("ревизия, было %v", res.Old)
		case res.Unchanged > 0:
			what = "без изменений"
		}
		m.Log.Info("manual input from telegram", "series", id, "period", period, "value", values[i], "result", what)
		lines = append(lines, fmt.Sprintf("✅ %s = %v (%s)", res.Series.Label, values[i], what))
	}
	if !ok {
		lines = append(lines, "Повторите или введите macroctl input.")
	}
	return strings.Join(lines, "\n"), true
}

// datesWords start the dates command: "ifo даты 24.11.2026 17.12.2026".
var datesWords = map[string]bool{"дата": true, "даты": true, "date": true, "dates": true}

// parseDay reads 24.11.2026, 24.11.26 or 2026-11-24.
func parseDay(w string) (time.Time, bool) {
	for _, layout := range []string{"02.01.2006", "2.1.2006", "02.01.06", "2.1.06", time.DateOnly} {
		if d, err := time.Parse(layout, w); err == nil {
			return d, true
		}
	}
	return time.Time{}, false
}

func nextDays(r *config.ManualReminder, days map[string][]time.Time, local time.Time) []time.Time {
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	var out []time.Time
	if len(r.Series) == 0 {
		return nil
	}
	for _, d := range days[r.Series[0]] {
		if !d.Before(today) {
			out = append(out, d)
		}
	}
	return out
}

func (m *Manual) datesHelp() string {
	r := m.rem()
	return fmt.Sprintf("Шаблон: %[1]s даты <ДД.ММ.ГГГГ> [<ДД.ММ.ГГГГ> …]\nПример: %[1]s даты 24.11.2026 17.12.2026\nУбрать ошибочную: %[1]s даты -24.11.2026\nСписок: %[1]s даты", r.Keyword())
}

// dates adds ("24.11.2026") or removes ("-24.11.2026") publication days of
// the reminder series; without arguments it lists the days ahead.
func (m *Manual) dates(ctx context.Context, words []string) string {
	r := m.rem()
	local := m.now().In(r.Location)
	var add, del []time.Time
	for _, w := range words {
		minus := strings.HasPrefix(w, "-")
		d, ok := parseDay(strings.TrimPrefix(w, "-"))
		if !ok {
			return fmt.Sprintf("«%s» — не дата. Ничего не записано.\n%s", w, m.datesHelp())
		}
		if minus {
			del = append(del, d)
		} else {
			add = append(add, d)
		}
	}
	var added, removed int
	if len(add)+len(del) > 0 {
		if err := m.Store.Write(ctx, func(tx *sql.Tx) (err error) {
			if added, err = store.AddManualReleaseDates(tx, r.Series, add, "telegram", m.now()); err != nil {
				return err
			}
			removed, err = store.DeleteManualReleaseDates(tx, r.Series, del)
			return err
		}); err != nil {
			m.Log.Error("manual release dates from telegram", "err", err)
			return "❌ Ошибка записи: " + err.Error()
		}
		m.Log.Info("manual release dates from telegram", "added", added, "removed", removed)
	}
	days, err := export.ManualReleaseDays(ctx, m.Config, m.Store)
	if err != nil {
		return "❌ " + err.Error()
	}
	var list []string
	for _, d := range nextDays(r, days, local) {
		list = append(list, d.Format("02.01.2006"))
	}
	head := fmt.Sprintf("Даты выхода %s впереди", r.Name)
	if len(add)+len(del) > 0 {
		head = fmt.Sprintf("✅ Добавлено %d, удалено %d. %s", added, removed, head)
	}
	if len(list) == 0 {
		return head + ": нет (напоминание — с " + fmt.Sprint(r.FromDay) + "-го числа).\n" + m.datesHelp()
	}
	return head + ": " + strings.Join(list, ", ") + ". Напоминание придёт в эти дни в " + r.Time + " (" + r.Location.String() + ")."
}
