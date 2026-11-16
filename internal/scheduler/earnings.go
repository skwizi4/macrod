package scheduler

import (
	"context"
	"time"

	"macro-collector/internal/collect"
	"macro-collector/internal/ingest"
	"macro-collector/internal/model"
)

// EDGARFetchCron is the daily safety run over every company: it catches
// reports without a calendar event and the 10-Q/10-K filed after the
// polling window (AVGO files a week later).
const EDGARFetchCron = "30 6 * * *"

// Earnings polls EDGAR for one company on its report day (§8.2 plan): the
// event is done once the earnings 8-K is stored and the company facts carry
// a 10-Q/10-K filed after it. If the window ends with the 8-K but before the
// 10-Q, the event is done anyway; the safety run picks up the facts.
type Earnings struct {
	EDGAR *collect.EDGAR
}

// Poll implements Handler.
func (h *Earnings) Poll(ctx context.Context, ev model.ReleaseEvent) (bool, error) {
	rep := h.EDGAR.Run(ctx, ingest.Incremental, ev.Related.Ticker)
	if len(rep.Results) == 0 {
		return false, nil // ticker no longer configured
	}
	res := rep.Results[0]
	r, ok := reportNear(res.Reports, ev, h.EDGAR.Config.Calendar.ReleaseLocation)
	done := ok && res.FactsCurrent && res.LatestPeriodic.After(r.AcceptedAt)
	return done, res.Err
}

// Expire implements Expirer: a stored 8-K makes the event done.
func (h *Earnings) Expire(ctx context.Context, ev model.ReleaseEvent) string {
	reports, err := h.EDGAR.Store.CompanyReportsOf(ctx, ev.Related.Ticker)
	if err == nil {
		if _, ok := reportNear(reports, ev, h.EDGAR.Config.Calendar.ReleaseLocation); ok {
			return model.StatusDone
		}
	}
	return model.StatusTimeout
}

// reportNear finds the report filed within a day of the event date.
func reportNear(reports []model.CompanyReport, ev model.ReleaseEvent, ny *time.Location) (model.CompanyReport, bool) {
	y, m, d := ev.ScheduledAt.In(ny).Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	for _, r := range reports {
		if diff := r.ReportDate.Sub(day); diff >= -24*time.Hour && diff <= 24*time.Hour {
			return r, true
		}
	}
	return model.CompanyReport{}, false
}
