package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/export"
	"macro-collector/internal/ingest"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/store"
)

// ECBPlan is the ECB decision-day mode of §8.2 with the numbers of
// ecb_texts.day_mode: from Lead before the decision every RSSInterval for
// FastFor, then the standard steps. FOMCPlan is left as it is.
type ECBPlan struct{ Mode config.ECBDayMode }

// Start implements Plan.
func (p ECBPlan) Start(ev model.ReleaseEvent) time.Time { return ev.ScheduledAt.Add(-p.Mode.Lead) }

// Next implements Plan.
func (p ECBPlan) Next(ev model.ReleaseEvent, last time.Time) (time.Time, bool) {
	if next := last.Add(p.Mode.RSSInterval); next.Sub(ev.ScheduledAt) <= p.Mode.FastFor {
		return next, true
	}
	return StandardPlan{}.Next(ev, last)
}

// ECBDay polls a decision day: the press feed on every poll, the yearly
// lists of decisions and statements every ListInterval (in case the feed
// lags behind a cache). Once the decision is stored the export waits for
// the statement until StatementWait and then goes out, with the statement
// or without it, exactly once per decision. A missing decision is alerted
// at AlertDecisionAt, and with no decision and no statement at
// StatementWait; each alert is sent once per event, remembered in
// fetch_state so a restart does not repeat it.
type ECBDay struct {
	Texts                       *ingest.Texts
	Feed, Decisions, Statements sources.TextSource
	Store                       *store.Store
	Export                      *export.Events
	Config                      *config.ECBTexts
	Alert                       func(ctx context.Context, text string) error // nil: logged only
	Clock                       Clock                                        // nil: real time
	Log                         *slog.Logger

	mu       sync.Mutex
	lastList time.Time
}

// Poll implements Handler.
func (h *ECBDay) Poll(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
	loc, now := h.Config.Location, h.now()
	dec, decOK, err := export.ECBDecisionOn(ctx, h.Store, ev.ScheduledAt, loc)
	if err != nil {
		return false, err
	}
	if decOK {
		if done, err := h.Export.ECBExported(ctx, dec); err != nil || done {
			return done, err
		}
	}
	_, stOK, err := export.ECBStatementOn(ctx, h.Store, ev.ScheduledAt, loc)
	if err != nil {
		return false, err
	}
	if !decOK || !stOK {
		h.fetch(ctx, now)
		if dec, decOK, err = export.ECBDecisionOn(ctx, h.Store, ev.ScheduledAt, loc); err != nil {
			return false, err
		}
		if _, stOK, err = export.ECBStatementOn(ctx, h.Store, ev.ScheduledAt, loc); err != nil {
			return false, err
		}
	}
	day := ev.ScheduledAt.In(loc)
	statementBy := h.Config.DayMode.StatementBy.On(day, loc)
	if !decOK {
		if alertAt := h.Config.DayMode.AlertDecision.On(day, loc); !now.Before(alertAt) {
			h.alertOnce(ctx, ev, "decision", fmt.Sprintf("⚠ ЕЦБ: решение %s не найдено к %s (опрос RSS и списков продолжается)",
				day.Format(time.DateOnly), h.Config.DayMode.AlertDecisionAt))
		}
		if !now.Before(statementBy) && !stOK {
			region := "all"
			if o, ok := h.Export.Config.Owner(config.ReportECBDiff); ok {
				region = o.Region.ID
			}
			h.alertOnce(ctx, ev, "nothing", fmt.Sprintf("⚠ ЕЦБ: решение и заявление %s не найдены к %s, экспорта нет. Вручную: macroctl fetch --source ecb_texts, затем macroctl export --region %s",
				day.Format(time.DateOnly), h.Config.DayMode.StatementWait, region))
		}
		return false, nil
	}
	if !stOK && now.Before(statementBy) {
		return false, nil // the press conference starts at 14:45
	}
	e, err := h.Export.ECB(ctx, dec)
	if err != nil {
		return false, fmt.Errorf("ecb export: %w", err)
	}
	h.Log.Info("ecb decision exported", "id", dec.ExternalID, "statement", stOK, "iso_week", e.ISOWeek,
		"after", now.Sub(ev.ScheduledAt).Round(time.Second))
	return true, nil
}

// fetch runs the feed, and the lists when ListInterval has passed. A failing
// source is logged by Texts and does not stop the others.
func (h *ECBDay) fetch(ctx context.Context, now time.Time) {
	h.Texts.Run(ctx, h.Feed)
	h.mu.Lock()
	lists := h.lastList.IsZero() || now.Sub(h.lastList) >= h.Config.DayMode.ListInterval
	if lists {
		h.lastList = now
	}
	h.mu.Unlock()
	if lists {
		h.Texts.Run(ctx, h.Decisions)
		h.Texts.Run(ctx, h.Statements)
	}
}

// alertOnce sends text unless the alert of this kind was already sent for
// the event; a failed send is retried at the next poll.
func (h *ECBDay) alertOnce(ctx context.Context, ev model.ReleaseEvent, kind, text string) {
	key := "alert:ecb:" + ev.ScheduledAt.In(h.Config.Location).Format(time.DateOnly) + ":" + kind
	if st, ok, err := h.Store.FetchStateOf(ctx, key); err != nil || (ok && st.Cursor != "") {
		return
	}
	h.Log.Warn("ecb alert", "kind", kind, "text", text)
	if h.Alert != nil {
		if err := h.Alert(ctx, text); err != nil {
			h.Log.Error("ecb alert not sent", "kind", kind, "err", err)
			return
		}
	}
	if err := h.Store.Write(ctx, func(tx *sql.Tx) error {
		return store.SetFetchOK(tx, key, "sent", h.now())
	}); err != nil {
		h.Log.Error("ecb alert state", "err", err)
	}
}

func (h *ECBDay) now() time.Time {
	if h.Clock != nil {
		return h.Clock.Now()
	}
	return time.Now()
}

// ECBStatement closes the press conference event once the statement of the
// day is stored (the decision handler fetches it; this one also polls the
// feed in case the decision event is over).
type ECBStatement struct {
	Texts    *ingest.Texts
	Feed     sources.TextSource
	Store    *store.Store
	Location *time.Location
}

// Poll implements Handler.
func (h *ECBStatement) Poll(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
	if _, ok, err := export.ECBStatementOn(ctx, h.Store, ev.ScheduledAt, h.Location); err != nil || ok {
		return ok, err
	}
	if res := h.Texts.Run(ctx, h.Feed); res.Err != nil {
		return false, res.Err
	}
	_, ok, err := export.ECBStatementOn(ctx, h.Store, ev.ScheduledAt, h.Location)
	return ok, err
}
