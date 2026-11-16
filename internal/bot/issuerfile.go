package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/export"
	"macro-collector/internal/model"
	"macro-collector/internal/notify"
	"macro-collector/internal/sources/irxlsx"
	"macro-collector/internal/store"
)

// IssuerFile takes the quarterly Excel of an issuer whose file cannot be
// downloaded (Allianz: allianz.com answers 403; phase 14c, D29). The
// caption is "[TICKER] [Q3 2026]": the ticker may be left out when one
// issuer has quarterly source file, the quarter when the file name shows it.
type IssuerFile struct {
	Config *config.Config
	Store  *store.Store
	TG     *notify.Telegram
	Log    *slog.Logger
	Now    func() time.Time // nil: time.Now
}

var tickerRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,9}$`)

// Help is the format line of the bot's help.
func (h *IssuerFile) Help() string {
	return "квартальный Excel эмитента: 📎 → «Файл» → .xlsx, подпись «<ТИКЕР> Q<квартал> <год>», например «ALV Q3 2026»"
}

// Handle takes a document; anything else is not its own.
func (h *IssuerFile) Handle(ctx context.Context, m *notify.Message) (string, bool) {
	d := m.Document
	if d == nil {
		return "", false
	}
	if d.Size > notify.MaxDownload {
		return fmt.Sprintf("❌ %s: больше %d МБ — бот не может скачать. Используйте macroctl issuers file.", d.Name, notify.MaxDownload>>20), true
	}
	ticker, period := splitCaption(m.Text)
	is, f, err := collect.FileIssuer(h.Config, ticker, d.Name, period)
	if err != nil {
		t := ticker
		if t == "" {
			t = "ALV"
		}
		return "❌ " + err.Error() + ". Ничего не записано.\n" + captionHint(strings.ToUpper(t)) + " Пришлите файл ещё раз с подписью.", true
	}
	data, err := h.TG.Download(ctx, d.FileID)
	if err != nil {
		h.Log.Warn("issuer file download", "ticker", is.Ticker, "file", d.Name, "err", err)
		return "❌ Файл не скачан: " + err.Error(), true
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	pairs, n, err := collect.IssuerQuarterlyFile(ctx, h.Store, is, f, data, now)
	if err != nil {
		h.Log.Warn("issuer file rejected", "ticker", is.Ticker, "file", d.Name, "err", err)
		return fmt.Sprintf("❌ %s Q%d %d (%s): %v. Ничего не записано.", is.Name, f.Q, f.FY, d.Name, err), true
	}
	h.Log.Info("issuer file stored", "ticker", is.Ticker, "file", d.Name, "fy", f.FY, "q", f.Q, "facts", n)
	lines := []string{fmt.Sprintf("✅ %s Q%d %d (%s), изменено фактов: %d", is.Name, f.Q, f.FY, d.Name, n)}
	lines = append(lines, collect.QuarterSummary(is, f, pairs)...)
	return strings.Join(lines, "\n"), true
}

// splitCaption reads "[TICKER] [quarter]".
func splitCaption(s string) (ticker, period string) {
	s = strings.TrimSpace(s)
	first, rest, _ := strings.Cut(s, " ")
	if tickerRe.MatchString(first) && !strings.HasPrefix(strings.ToUpper(first), "Q") {
		return first, strings.TrimSpace(rest)
	}
	return "", s
}

// fileReminderKey is the fetch_state row of an issuer's file reminder:
// cursor "<day>|<period>".
func fileReminderKey(ticker string) string { return "notify:issuer_file:" + ticker }

// fileReminderDays is how long after a report day the file is asked for.
const fileReminderDays = 21

// reportQuarter is the fiscal quarter of a report_dates period: "Q3 2026",
// "Q4 FY2026"; "FY 2026" is the fourth quarter.
func reportQuarter(period string) (fy, q int, ok bool) {
	if fy, q, ok = irxlsx.ParseQuarter(period); ok {
		return fy, q, true
	}
	var y int
	if _, err := fmt.Sscanf(strings.TrimSpace(period), "FY %d", &y); err == nil && y > 1900 {
		return y, 4, true
	}
	return 0, 0, false
}

// Remind asks, once a day, for the quarterly file of each issuer whose
// quarterly source is file: from the report day (report_dates merged with
// the calendar page) for 21 days, until the file of that quarter is stored.
// It returns the tickers it asked for.
func (h *IssuerFile) Remind(ctx context.Context) ([]string, error) {
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	loc := h.Config.Location
	if loc == nil {
		loc = time.UTC
	}
	today := model.DayIn(now, loc)
	var asked []string
	var errs []error
	for _, is := range h.Config.Issuers.Companies {
		if is.Quarterly == nil || !is.Quarterly.FromFile() {
			continue
		}
		dates := is.Reports
		if is.Calendar != nil {
			cal, err := h.Store.IssuerReportDates(ctx, is.Ticker)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			dates = export.MergeReportDates(is.Name, is.Reports, cal)
		}
		var due *config.IssuerDate
		for i := range dates {
			d := &dates[i]
			if !d.Day.After(today) && today.Sub(d.Day) <= fileReminderDays*24*time.Hour {
				due = d // the latest report day in the window
			}
		}
		if due == nil {
			continue
		}
		fy, q, ok := reportQuarter(due.Period)
		if !ok {
			h.Log.Warn("issuer file reminder: period not understood", "ticker", is.Ticker, "period", due.Period)
			continue
		}
		have, err := h.hasQuarter(ctx, is.Ticker, fy, q)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		day := today.Format(time.DateOnly)
		st, ok, err := h.Store.FetchStateOf(ctx, fileReminderKey(is.Ticker))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if have || (ok && strings.HasPrefix(st.Cursor, day+"|")) {
			continue
		}
		until := due.Day.AddDate(0, 0, fileReminderDays).Format(time.DateOnly)
		if err := h.TG.Send(ctx, FileRequest(is, fy, q, due.Day, until)); err != nil {
			errs = append(errs, fmt.Errorf("issuer file reminder %s: %w", is.Ticker, err))
			continue
		}
		if err := h.Store.Write(ctx, func(tx *sql.Tx) error {
			return store.SetFetchOK(tx, fileReminderKey(is.Ticker), fmt.Sprintf("%s|Q%d %d", day, q, fy), now)
		}); err != nil {
			errs = append(errs, err)
		}
		h.Log.Info("issuer file reminder sent", "ticker", is.Ticker, "fy", fy, "q", q)
		asked = append(asked, is.Ticker)
	}
	return asked, errors.Join(errs...)
}

func (h *IssuerFile) hasQuarter(ctx context.Context, ticker string, fy, q int) (bool, error) {
	facts, err := h.Store.CompanyFacts(ctx, ticker)
	if err != nil {
		return false, err
	}
	fp := fmt.Sprintf("Q%d", q)
	return slices.ContainsFunc(facts, func(f model.CompanyFact) bool { return f.Form == irxlsx.Form && f.FY == fy && f.FP == fp }), nil
}

// FileRequest is the reminder text: what to send, the caption to type and
// where the file is.
func FileRequest(is config.Issuer, fy, q int, reported time.Time, until string) string {
	caption := fmt.Sprintf("%s Q%d %d", is.Ticker, q, fy)
	var b strings.Builder
	fmt.Fprintf(&b, "%s: отчёт за Q%d %d вышел %s — пришлите квартальный Excel.\n\n", is.Name, q, fy, reported.Format(time.DateOnly))
	b.WriteString("Как отправить: в этом чате 📎 → «Файл» → выберите .xlsx (документом, не фото и не ссылкой).\n")
	b.WriteString("В поле подписи к файлу напишите:\n")
	b.WriteString(caption + "\n")
	b.WriteString("Шаблон подписи: <ТИКЕР> Q<квартал> <год>\n")
	if is.Quarterly != nil && is.Quarterly.Hint != "" {
		b.WriteString("Где взять: " + is.Quarterly.Hint + "\n")
	}
	fmt.Fprintf(&b, "Без Telegram: macroctl issuers file %s <файл.xlsx> --period \"Q%d %d\"\n", is.Ticker, q, fy)
	fmt.Fprintf(&b, "Напоминаю раз в будний день до %s, пока файла нет.", until)
	return b.String()
}

// captionHint is the caption template of the replies.
func captionHint(ticker string) string {
	return fmt.Sprintf("Подпись к файлу: «%s Q3 2026» (шаблон: <ТИКЕР> Q<квартал> <год>).", ticker)
}
