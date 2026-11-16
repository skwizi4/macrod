package forecast

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/period"
	"macro-collector/internal/store"
)

// recentWeeks is the window of the "last weeks" hit rate.
const recentWeeks = 12

var directionRU = map[string]string{Up: "рост (up)", Down: "падение (down)", Range: "диапазон (range)"}

var outcomeRU = map[string]string{
	store.OutcomeHit:         "✅ hit",
	store.OutcomeMiss:        "❌ miss",
	store.OutcomeInvalidated: "⛔ invalidated",
	store.OutcomePending:     "⏳ ожидает",
}

type tally struct{ hit, miss, inv int }

func (t *tally) add(outcome string) {
	switch outcome {
	case store.OutcomeHit:
		t.hit++
	case store.OutcomeMiss:
		t.miss++
	case store.OutcomeInvalidated:
		t.inv++
	}
}

func (t tally) total() int { return t.hit + t.miss + t.inv }

func (t tally) rate() string {
	if t.total() == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f %%", float64(t.hit)*100/float64(t.total()))
}

// Card is what forecast_scorecard.md of a region is rendered from.
type Card struct {
	Title string
	Rows  []store.Forecast
	Notes []Note // weeks whose report had no usable block
	// Primary maps an instrument to its price series (source/id): a row
	// priced on another one was scored on the fallback.
	Primary  map[string]string
	RangePct float64
	Now      time.Time
	Loc      *time.Location // of the export periods
}

// Render is forecast_scorecard.md: the rules, the hit rate (invalidated
// counts as a miss) and the forecasts by week, newest first. A pending row
// whose forecast week has ended is marked: it has no closes to be scored on.
func Render(c Card) string {
	title, rows, notes, primary, rangePct := c.Title, c.Rows, c.Notes, c.Primary, c.RangePct
	now := c.Now.In(c.Loc)
	next := func(week string) string { return period.Next(week, c.Loc) }
	overdue := func(r store.Forecast) bool {
		end, err := period.EndOfWeek(next(r.ISOWeek), c.Loc)
		return r.Outcome == store.OutcomePending && err == nil && !now.Before(end)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Лог прогнозов — %s\n\n", title)
	fmt.Fprintf(&b, "Обновлено: %s. Прогноз из отчёта мастера за неделю W — на следующую торговую неделю.\n\n", now.Format("2006-01-02 15:04 MST"))
	b.WriteString("Правила оценки: опорная цена — последнее закрытие недели отчёта, итог — последнее закрытие недели прогноза; " +
		"up — итог выше опорной, down — ниже; range — |Δ| < " + num(rangePct) + " %; " +
		"любое дневное закрытие недели прогноза за уровнем invalidation (up — ниже, down — выше, range — по стороне уровня от опорной) — invalidated. " +
		"Hit rate = hit / (hit + miss + invalidated).\n\n")

	weeks := map[string]bool{}
	for _, r := range rows {
		weeks[r.ISOWeek] = true
	}
	var order []string
	for w := range weeks {
		order = append(order, w)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(order)))
	recent := map[string]bool{}
	for i, w := range order {
		if i < recentWeeks {
			recent[w] = true
		}
	}

	var all, last tally
	by := map[string]*tally{Up: {}, Down: {}, Range: {}}
	pending, late := 0, 0
	for _, r := range rows {
		all.add(r.Outcome)
		if t := by[r.Direction]; t != nil {
			t.add(r.Outcome)
		}
		if recent[r.ISOWeek] {
			last.add(r.Outcome)
		}
		if r.Outcome == store.OutcomePending {
			pending++
		}
		if overdue(r) {
			late++
		}
	}
	b.WriteString("## Итог\n\n")
	b.WriteString("| | Оценено | hit | miss | invalidated | Hit rate |\n|---|---:|---:|---:|---:|---:|\n")
	line := func(name string, t tally) {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %s |\n", name, t.total(), t.hit, t.miss, t.inv, t.rate())
	}
	line("Все прогнозы", all)
	line(fmt.Sprintf("Последние %d недель", recentWeeks), last)
	for _, d := range []string{Up, Down, Range} {
		line(directionRU[d], *by[d])
	}
	if pending > 0 {
		fmt.Fprintf(&b, "\nОжидают оценки: %d", pending)
		if late > 0 {
			fmt.Fprintf(&b, " (из них %d — неделя прошла, а закрытий нет: проверьте загрузку цен)", late)
		}
		b.WriteString(".\n")
	}

	b.WriteString("\n## По неделям\n\n")
	sorted := append([]store.Forecast(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].ISOWeek != sorted[j].ISOWeek {
			return sorted[i].ISOWeek > sorted[j].ISOWeek
		}
		return sorted[i].Instrument < sorted[j].Instrument
	})
	if len(sorted) == 0 {
		b.WriteString("Прогнозов пока нет.\n")
	} else {
		b.WriteString("| Отчёт | Неделя прогноза | Инструмент | Прогноз | Invalidation | Опорная | Итог | Δ | Результат |\n")
		b.WriteString("|---|---|---|---|---:|---|---|---:|---|\n")
		for _, r := range sorted {
			refCell, resCell, chg := "—", "—", "—"
			if r.Outcome != store.OutcomePending {
				refCell = num(r.RefPrice) + " (" + r.RefDate.Format("02.01") + ")"
				resCell = num(r.ResultPrice) + " (" + r.ResultDate.Format("02.01") + ")"
				chg = fmt.Sprintf("%+.2f %%", r.ChangePct)
			}
			result := outcomeRU[r.Outcome]
			if overdue(r) {
				result = "⚠ нет закрытий за неделю прогноза"
			}
			if r.Outcome != store.OutcomePending && r.PriceSeries != primary[r.Instrument] {
				result += " (по " + r.PriceSeries + ", уровень не проверялся)"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
				r.ISOWeek, next(r.ISOWeek), r.Instrument, r.Direction, num(r.Invalidation), refCell, resCell, chg, result)
		}
	}
	if len(notes) > 0 {
		b.WriteString("\n## Отчёты без разобранного прогноза\n\n")
		sorted := append([]Note(nil), notes...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Week > sorted[j].Week })
		for _, n := range sorted {
			if errors.Is(n.Err, ErrNoBlock) {
				fmt.Fprintf(&b, "- %s — блока forecast нет, в hit rate не входит.\n", n.Week)
			} else {
				fmt.Fprintf(&b, "- %s — блок не разобран (%s), в hit rate не входит.\n", n.Week, n.Err)
			}
		}
	}
	return b.String()
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
