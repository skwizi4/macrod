package export

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"macro-collector/internal/transform"
)

// maxEventSurprises bounds the surprise lines shown per event row.
const maxEventSurprises = 3

// reactionRow is the events at one moment.
type reactionRow struct {
	at      time.Time
	labels  []string
	aliases []string
}

// PostSurprise is a surprise line of a post (E2, D11: as written, not
// numbers of ours).
type PostSurprise struct {
	Label    string `json:"label"`
	Actual   string `json:"actual"`
	Expected string `json:"expected"`
	Diff     string `json:"diff"`
}

// ReactionMove is the move of an instrument after an event: % or, for a
// note future, ≈ b.p. of yield.
type ReactionMove struct {
	First Quantity `json:"first"`
	Full  Quantity `json:"full"`
}

// ReactionEvent is the events at one moment, At in ET; Moves by Symbols.
type ReactionEvent struct {
	Labels    []string       `json:"labels"`
	At        time.Time      `json:"at"`
	Surprises []PostSurprise `json:"surprises"`
	Moves     []ReactionMove `json:"moves"`
}

// ReactionSection is section 1 of a sentiment file. Daily are the labels
// of Symbols read from a stored daily series (no 30-minute move); Close
// is the close HH:MM in the zone of At.
type ReactionSection struct {
	Configured    bool            `json:"configured"`
	WindowMinutes int             `json:"window_minutes"`
	Symbols       []string        `json:"symbols"`
	Daily         []string        `json:"daily,omitempty"`
	Close         string          `json:"close,omitempty"`
	Rows          []ReactionEvent `json:"rows"`
	NoBars        bool            `json:"no_bars"`
}

func reactionQty(v transform.Value, duration float64) Quantity {
	if duration > 0 {
		return qty(transform.YieldFromPrice(v, duration), unitBP)
	}
	return qty(v, unitPercent)
}

// sentReaction is F1: how the market moved in the first 30 minutes after a
// high-importance event and up to the close, next to the surprise posts
// reported for it (E2). A series without minute bars (Bund 10Y) shows its
// change on the close day only. Events of another region come when the
// configuration names them.
func (w *week) sentReaction(ctx context.Context) (ReactionSection, []view, error) {
	r := w.sent().Reaction
	out := ReactionSection{Configured: len(r.Symbols) > 0, WindowMinutes: int(r.Window.Minutes()), Symbols: []string{}, Rows: []ReactionEvent{}}
	if len(r.Symbols) == 0 {
		return out, nil, nil
	}
	if r.Close != "16:00" || r.Timezone != "America/New_York" {
		out.Close = r.Close
	}
	daily := map[string]view{}
	var vs []view
	for _, s := range r.Symbols {
		out.Symbols = append(out.Symbols, s.Label)
		if s.Series != "" {
			v, err := w.resolve(ctx, s.Series)
			if err != nil {
				return out, nil, err
			}
			daily[s.Series] = v
			vs = append(vs, v)
			out.Daily = append(out.Daily, s.Label)
		}
	}
	ny := r.Location
	events, err := w.st.Releases(ctx, w.from, w.to)
	if err != nil {
		return out, nil, err
	}
	var rows []*reactionRow
	for _, e := range events {
		label, aliases, ok := r.Match(e)
		if !ok {
			continue
		}
		if i := slices.IndexFunc(rows, func(x *reactionRow) bool { return x.at.Equal(e.ScheduledAt) }); i >= 0 {
			rows[i].labels = append(rows[i].labels, label)
			rows[i].aliases = append(rows[i].aliases, aliases...)
			continue
		}
		rows = append(rows, &reactionRow{at: e.ScheduledAt, labels: []string{label}, aliases: slices.Clone(aliases)})
	}
	if len(rows) == 0 {
		return out, vs, nil
	}
	surprises, err := w.weekSurprises(ctx)
	if err != nil {
		return out, nil, err
	}
	for _, row := range rows {
		closeAt := transform.ReactionClose(row.at, ny, r.CloseAt.Hour, r.CloseAt.Minute)
		ev := ReactionEvent{Labels: row.labels, At: row.at.In(ny), Surprises: eventSurprises(surprises, row, ny), Moves: []ReactionMove{}}
		for _, s := range r.Symbols {
			if s.Series != "" {
				ev.Moves = append(ev.Moves, dailyReaction(daily[s.Series], dayOf(closeAt, ny)))
				continue
			}
			bars, err := w.st.IntradayBars(ctx, s.Symbol, row.at.Add(-time.Hour), closeAt)
			if err != nil {
				return out, nil, err
			}
			ticks := make([]transform.Tick, len(bars))
			for i, x := range bars {
				ticks[i] = transform.Tick{TS: x.TS, Close: x.Close}
			}
			first, full := transform.MoveBetween(ticks, row.at, row.at.Add(r.Window)), transform.MoveBetween(ticks, row.at, closeAt)
			if !first.OK && !full.OK {
				out.NoBars = true
			}
			ev.Moves = append(ev.Moves, ReactionMove{First: reactionQty(first, s.Duration), Full: reactionQty(full, s.Duration)})
		}
		out.Rows = append(out.Rows, ev)
	}
	return out, vs, nil
}

// dailyReaction is the change of a daily series on the close day against
// its previous value: b.p. for a rate, else %; no 30-minute move.
func dailyReaction(v view, day time.Time) ReactionMove {
	unit := unitPercent
	if v.Kind == kindRate {
		unit = unitBP
	}
	m := ReactionMove{First: qty(transform.Value{}, unit), Full: qty(transform.Value{}, unit)}
	for i := 1; i < len(v.Obs); i++ {
		if !v.Obs[i].Date.Equal(day) {
			continue
		}
		prev, cur := v.Obs[i-1].Value, v.Obs[i].Value
		switch {
		case unit == unitBP:
			m.Full = qty(some((cur-prev)*100), unit)
		case prev != 0:
			m.Full = qty(some((cur/prev-1)*100), unit)
		}
	}
	return m
}

// reactionCell is a % move, or ≈ b.p. of yield for a note future.
func reactionCell(q Quantity) string {
	v := q.val()
	switch {
	case !v.OK:
		return dash
	case q.Unit == unitBP:
		return "≈" + num(v.V, 0, true)
	}
	return num(v.V, 2, true) + "%"
}

// surpriseCell lists the surprise lines of an event.
func surpriseCell(ss []PostSurprise) string {
	if len(ss) == 0 {
		return dash
	}
	cells := make([]string, 0, len(ss))
	for _, s := range ss {
		c := fmt.Sprintf("%s %s vs %s", mdCell(s.Label), s.Actual, s.Expected)
		if s.Diff != "" && s.Diff != dash {
			c += " (" + s.Diff + ")"
		}
		cells = append(cells, c)
	}
	return strings.Join(cells, "; ")
}

// postSurprise is a surprise line with the post's time.
type postSurprise struct {
	at time.Time
	surprise
}

// weekSurprises are the surprise lines of the week's news posts (E2), from
// the cleaned bodies as the region's News reads them.
func (w *week) weekSurprises(ctx context.Context) ([]postSurprise, error) {
	posts, err := w.st.PostsBetween(ctx, w.from, w.to)
	if err != nil {
		return nil, err
	}
	cl := newNewsCleaner(w.cfg.News, w.newsConfig(), w.cfg.Classify.Rules, posts)
	var out []postSurprise
	for _, p := range posts {
		if !w.inNews(p) || p.DupOf != 0 || p.IsJunk {
			continue
		}
		for _, s := range extractSurprises(cl.clean(p)) {
			out = append(out, postSurprise{at: p.PublishedAt, surprise: s})
		}
	}
	return out, nil
}

// eventSurprises are the surprise lines naming the event, posted from its
// New York day to the end of the next one.
func eventSurprises(all []postSurprise, row *reactionRow, ny *time.Location) []PostSurprise {
	t := row.at.In(ny)
	from := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, ny)
	to := from.AddDate(0, 0, 2)
	out := []PostSurprise{}
	seen := map[string]bool{}
	for _, s := range all {
		if s.at.Before(from) || !s.at.Before(to) || !namesEvent(s.label, row.aliases) || seen[s.label] {
			continue
		}
		seen[s.label] = true
		out = append(out, PostSurprise{Label: s.label, Actual: s.actual, Expected: s.expected, Diff: s.diff})
		if len(out) == maxEventSurprises {
			break
		}
	}
	return out
}

// namesEvent: the label contains an alias as a word ("GDP Q3 (1st)" for
// GDP); a cashtag alias matches the label's prefix.
func namesEvent(label string, aliases []string) bool {
	low := strings.ToLower(label)
	for _, a := range aliases {
		al := strings.ToLower(a)
		if strings.HasPrefix(al, "$") {
			if strings.HasPrefix(low, al+" ") || low == al {
				return true
			}
			continue
		}
		for i := strings.Index(low, al); i >= 0; {
			end := i + len(al)
			if (i == 0 || !isWordByte(low[i-1])) && (end == len(low) || !isWordByte(low[end])) {
				return true
			}
			j := strings.Index(low[i+1:], al)
			if j < 0 {
				break
			}
			i += 1 + j
		}
	}
	return false
}

func isWordByte(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c >= 0x80 }
