package export

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/classify"
	"macro-collector/internal/config"
	"macro-collector/internal/store"
	"macro-collector/internal/transform"
)

// newsSurprise is a row of "Сюрпризы недели": the expectation from a post
// and the actual value from our series where we have it (2026-11-03),
// else from the post.
type newsSurprise struct {
	post store.WeekPost
	surprise
	ours   bool   // actual from our series
	source string // series of the actual
}

// periodNames name a transform in a forecast row: "Core PCE m/m".
var periodNames = map[string]string{transform.MoM: "m/m", transform.YoY: "y/y", transform.QoQA: "q/q, годовых"}

// surpriseFactSince is how far back a fact series is read: enough for yoy
// and qoqa of the latest observation.
const surpriseFactSince = 3 // years

// resolveSurprises merges the rows naming the same indicator on the same
// day (first post wins) and takes the actual from surprise_facts. A
// forecast (an expectation without an actual, extractForecasts) stays only
// with an actual from our series and is named by the entry's label and
// period ("Core PCE m/m"), not by the post's wording.
func (w *week) resolveSurprises(ctx context.Context, d *newsDoc) error {
	seen := make(map[string]bool)
	var out []newsSurprise
	cache := make(map[int]factValue)
	for _, s := range d.surprises {
		key := s.post.PublishedAt.In(w.loc).Format(time.DateOnly) + "|" + strings.Join(classify.Normalize(s.label), " ")
		if seen[key] {
			continue
		}
		seen[key] = true
		forecast := s.actual == ""
		if i, ok := matchSurpriseFact(w.cfg.News.SurpriseFacts, s.label, forecast); ok {
			fv, ok := cache[i]
			if !ok {
				var err error
				if fv, err = w.surpriseFact(ctx, w.cfg.News.SurpriseFacts[i]); err != nil {
					return err
				}
				cache[i] = fv
			}
			if fv.ok {
				f := w.cfg.News.SurpriseFacts[i]
				if act, ok := factString(fv.v, f.Unit, s.actual, s.expected); ok {
					if !forecast && act != s.actual {
						s.label += " (в посте: " + s.actual + ")"
					}
					if forecast && f.Label != "" {
						s.label = strings.TrimSpace(f.Label + " " + periodNames[f.Transform])
					}
					s.actual, s.diff, s.ours, s.source = act, surpriseDiff(act, s.expected), true, "FRED "+w.cfg.News.SurpriseFacts[i].Series
				}
			}
		}
		if s.actual == "" {
			continue
		}
		out = append(out, s)
	}
	d.surprises = out
	return nil
}

// matchSurpriseFact is the first entry whose words name the indicator.
// anyOrder matches a phrase when all its words are in the label, in any
// order: a forecast's label joins the post's first line and the line's
// indicator ("Очікування щодо сьогоднішнього PCE — Зростання Core").
func matchSurpriseFact(facts []config.SurpriseFact, label string, anyOrder bool) (int, bool) {
	ws := classify.Normalize(label)
	has := func(list []string) bool {
		for _, s := range list {
			ph := classify.CompilePhrase(s)
			if ph.In(ws) {
				return true
			}
			if anyOrder && len(ph) > 1 && allWords(ph, ws) {
				return true
			}
		}
		return false
	}
	for i, f := range facts {
		if has(f.Words) && (len(f.When) == 0 || has(f.When)) && !has(f.Unless) {
			return i, true
		}
	}
	return 0, false
}

// allWords: every word of ph is somewhere in ws.
func allWords(ph classify.Phrase, ws []string) bool {
	for i := range ph {
		if !ph[i : i+1].In(ws) {
			return false
		}
	}
	return true
}

type factValue struct {
	v  float64
	ok bool
}

// surpriseFact is the transform of the series at its latest observation,
// if that observation became known during the week (not from the initial
// history load): only then is it the release the posts write about.
func (w *week) surpriseFact(ctx context.Context, f config.SurpriseFact) (factValue, error) {
	known, err := w.known(ctx, "fred", f.Series, w.from.AddDate(-surpriseFactSince, 0, 0))
	if err != nil || len(known) == 0 {
		return factValue{}, err
	}
	meta, _, err := w.st.Meta(ctx, "fred", f.Series)
	if err != nil {
		return factValue{}, err
	}
	res, err := transform.Compute(transform.Input{Obs: plain(known), Freq: strings.ToLower(meta.Frequency), AsOf: w.asOf}, []string{f.Transform})
	if err != nil {
		return factValue{}, fmt.Errorf("surprise fact %s: %w", f.Series, err)
	}
	v := res.Values[f.Transform]
	if !v.OK {
		return factValue{}, nil
	}
	for _, k := range known {
		if !k.Date.Equal(res.Date) {
			continue
		}
		if k.Initial || k.Seen.Before(w.from) || !k.Seen.Before(w.to) {
			return factValue{}, nil
		}
		scale := f.Scale
		if scale == 0 {
			scale = 1
		}
		return factValue{v: v.V * scale, ok: true}, nil
	}
	return factValue{}, nil
}

// factString spells our value v (in unit "%", "K", "M", "B") as the post
// spells the expectation: its decimals, sign and unit ("194 тис.",
// "3.4%"). An expectation without a unit borrows the actual's. Units that
// do not match fail.
func factString(v float64, unit, actual, expected string) (string, bool) {
	spelled := expected
	_, u, ok := parseValue(expected)
	if !ok {
		return "", false
	}
	if u == "" {
		if _, ua, ok := parseValue(actual); ok {
			u, spelled = ua, actual
		}
	}
	if unitClass(u) != unit {
		return "", false
	}
	m := valueRe.FindStringSubmatch(spelled)
	s := strconv.FormatFloat(v, 'f', decimals(spelled), 64)
	if strings.HasPrefix(m[1], "+") && v > 0 {
		s = "+" + s
	}
	return strings.Replace(s, "-", "−", 1) + m[2], true
}

// unitClass maps a parseValue unit to "%", "K", "M", "B", or "" for none.
func unitClass(u string) string {
	switch u {
	case "%":
		return "%"
	case "тис.", "k":
		return "K"
	case "m", "mn", "млн":
		return "M"
	case "b", "bn", "млрд":
		return "B"
	}
	return ""
}

// surpriseRows are the rows of "1. Сюрпризы недели" (FIXES_W44 E2): the
// expectation from posts, the actual from our series where we have it.
func (w *week) surpriseRows(rows []newsSurprise) []NewsSurpriseRow {
	out := []NewsSurpriseRow{}
	for _, r := range rows {
		src := ""
		if r.ours {
			src = r.source
		}
		out = append(out, NewsSurpriseRow{Date: Day{r.post.PublishedAt.In(w.loc)}, Label: r.label, Expected: r.expected, Actual: r.actual, FactSource: src, Diff: r.diff, URL: r.post.URL})
	}
	return out
}
