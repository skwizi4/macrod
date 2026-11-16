package export

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"macro-collector/internal/derived"
	"macro-collector/internal/sources/predict"
	"macro-collector/internal/store"
	"macro-collector/internal/transform"
)

// predictionOpenDays: a market without a price this many days before the
// period end is closed or resolved and is not shown.
const predictionOpenDays = 4

// fedTopic is the prediction topic of the next FOMC decision, compared with
// the ZQ path.
const fedTopic = "fed_next"

// question is the open markets of one question on one platform: its
// outcomes.
type question struct {
	topic, label, source, title string
	outcomes                    []outcome
}

type outcome struct {
	name string // empty for a yes/no question
	id   string // market ID: Kalshi ticker, Polymarket slug
	v    view
}

// PredictionOutcome is an outcome of a question: "" for a yes/no question.
type PredictionOutcome struct {
	Name     string   `json:"name"`
	Level    Quantity `json:"level"`
	Change1W Quantity `json:"change_1w"`
	Change4W Quantity `json:"change_4w"`
}

// PredictionQuestion is one row: a question on one platform, its outcomes
// by falling probability.
type PredictionQuestion struct {
	Topic    string              `json:"topic"`
	Title    string              `json:"title"`
	Source   string              `json:"source"`
	Date     Day                 `json:"date"`
	Freq     string              `json:"freq"`
	Outcomes []PredictionOutcome `json:"outcomes"`
}

// HikePlatform is the sum of the hike outcomes of a platform.
type HikePlatform struct {
	Name     string   `json:"name"`
	Level    Quantity `json:"level"`
	Change1W Quantity `json:"change_1w"`
}

// HikeComparison is P(hike) at the next FOMC meeting from ZQ and the
// platforms; MeetingBP is the move priced by ZQ.
type HikeComparison struct {
	Meeting   Day            `json:"meeting"`
	PriceDate Day            `json:"price_date"`
	MeetingBP Quantity       `json:"meeting_bp"`
	Platforms []HikePlatform `json:"platforms"`
}

// Predictions is section 7 of USA Sentiment.
type Predictions struct {
	Enabled   bool                 `json:"enabled"`
	Questions []PredictionQuestion `json:"questions"`
	Hike      *HikeComparison      `json:"hike"`
}

// sentPredictions is F6: one row per question and platform with the
// distribution of its outcomes, Kalshi and Polymarket next to each other
// by topic, then P(hike) at the next FOMC meeting from ZQ, Kalshi and
// Polymarket side by side (FIXES_W44 G4a).
func (w *week) sentPredictions(ctx context.Context) (Predictions, []view, error) {
	p := w.cfg.Prediction
	out := Predictions{Enabled: p.Enabled, Questions: []PredictionQuestion{}}
	if !p.Enabled {
		return out, nil, nil
	}
	var qs []*question
	var vs []view
	k := kindPct
	for _, t := range p.Topics {
		for _, src := range []string{predict.SourceKalshi, predict.SourcePolymarket} {
			meta, err := w.st.SeriesQuestions(ctx, src)
			if err != nil {
				return out, nil, err
			}
			ids := make([]string, 0, len(meta))
			for id := range meta {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			byKey := map[string]*question{}
			for _, id := range ids {
				topic, market, ok := predict.TopicOf(id)
				if !ok || topic != t.Name {
					continue
				}
				v, err := w.load(ctx, ref{src, id}, spec{Codes: []string{transform.Level, transform.D1W, transform.D4W}, Kind: &k})
				if err != nil {
					return out, nil, err
				}
				if v.Last.IsZero() || v.Last.Before(w.asOf.AddDate(0, 0, -predictionOpenDays)) {
					continue
				}
				vs = append(vs, v)
				m := meta[id]
				title, name := splitQuestion(m)
				key := m.Event
				if key == "" { // stored before 0009: the question in the label
					key = title
				}
				q := byKey[key]
				if q == nil {
					q = &question{topic: t.Name, label: t.Label, source: src, title: title}
					byKey[key] = q
					qs = append(qs, q)
				}
				q.outcomes = append(q.outcomes, outcome{name: name, id: market, v: v})
			}
		}
	}
	if len(qs) == 0 {
		return out, nil, nil
	}
	for _, q := range qs {
		slices.SortStableFunc(q.outcomes, func(a, c outcome) int { return cmp.Compare(c.v.level().V, a.v.level().V) })
		last := q.outcomes[0].v
		row := PredictionQuestion{Topic: q.label, Title: q.title, Source: q.source, Outcomes: []PredictionOutcome{}}
		for _, o := range q.outcomes {
			row.Outcomes = append(row.Outcomes, PredictionOutcome{Name: o.name, Level: levelQty(o.v), Change1W: qty(o.v.get(transform.D1W), unitPP), Change4W: qty(o.v.get(transform.D4W), unitPP)})
			if o.v.Last.After(last.Last) {
				last = o.v
			}
		}
		row.Date, row.Freq = Day{last.Last}, last.Freq
		out.Questions = append(out.Questions, row)
	}
	var err error
	if out.Hike, err = w.hikeComparison(ctx, qs); err != nil {
		return out, nil, err
	}
	return out, vs, nil
}

// predictionCell is the distribution of a question.
func predictionCell(q PredictionQuestion) string {
	parts := make([]string, 0, len(q.Outcomes))
	for _, o := range q.Outcomes {
		name := o.Name
		if name == "" || len(q.Outcomes) == 1 { // a yes/no question ("Starts" on Kalshi)
			name = "да"
		}
		parts = append(parts, fmt.Sprintf("%s %s (%s / %s)", name, kindPct.level(o.Level.val()), pp(o.Change1W.val()), pp(o.Change4W.val())))
	}
	return strings.Join(parts, " · ")
}

// hikeCells are the P(hike) cells of ZQ and the platforms.
func hikeCells(h HikeComparison) string {
	zq := "ZQ 0%"
	switch bp := math.Round(h.MeetingBP.val().V); {
	case bp > 0:
		zq = "ZQ " + num(derived.MoveProbability(bp)*100, 0, false) + "%"
	case bp < 0:
		zq += " (заложено снижение " + num(derived.MoveProbability(bp)*100, 0, false) + "%)"
	}
	cells := []string{zq}
	for _, p := range h.Platforms {
		cells = append(cells, fmt.Sprintf("%s %s%% (Δ1w %s п.п.)", p.Name, num(p.Level.val().V, 0, false), pp(p.Change1W.val())))
	}
	return strings.Join(cells, " · ")
}

// splitQuestion is the question and the outcome of a market: the stored
// question (0009), else the label up to " — ".
func splitQuestion(m store.SeriesQuestion) (title, outcome string) {
	if m.Question != "" {
		return m.Question, strings.TrimPrefix(strings.TrimPrefix(m.Label, m.Question), " — ")
	}
	title, outcome, _ = strings.Cut(m.Label, " — ")
	return title, outcome
}

// hikeComparison is P(hike) at the next FOMC meeting: ZQ (the move priced
// for the meeting / 25 b.p., 0 when a cut is priced) next to the sum of the
// hike outcomes on Kalshi and Polymarket, when their question is about the
// same meeting. Nil without any such question.
func (w *week) hikeComparison(ctx context.Context, qs []*question) (*HikeComparison, error) {
	path, err := w.fedPath(ctx)
	if err != nil {
		return nil, err
	}
	if len(path.Rows) == 0 || !path.Rows[0].MeetingBP.OK {
		return nil, nil
	}
	meeting := path.Rows[0].Meeting
	h := &HikeComparison{Meeting: Day{meeting}, PriceDate: Day{path.PriceDate}, MeetingBP: qty(path.Rows[0].MeetingBP, unitBP), Platforms: []HikePlatform{}}
	for _, q := range qs {
		if q.topic != fedTopic || !sameMeeting(q, meeting) {
			continue
		}
		var lvl, d1 transform.Value
		lvl.OK, d1.OK = true, true
		for _, o := range q.outcomes {
			if !isHike(q.source, o) {
				continue
			}
			lvl.V += o.v.level().V
			if x := o.v.get(transform.D1W); x.OK {
				d1.V += x.V
			} else {
				d1.OK = false
			}
		}
		name := map[string]string{predict.SourceKalshi: "Kalshi", predict.SourcePolymarket: "Polymarket"}[q.source]
		h.Platforms = append(h.Platforms, HikePlatform{Name: name, Level: qty(lvl, unitPercent), Change1W: qty(d1, unitPP)})
	}
	if len(h.Platforms) == 0 {
		return nil, nil
	}
	return h, nil
}

// isHike: Kalshi "H25"/"H26" (not "H0", no change), Polymarket "… increase".
func isHike(source string, o outcome) bool {
	if source == predict.SourceKalshi {
		i := strings.LastIndex(o.id, "-")
		suffix := o.id[i+1:]
		return strings.HasPrefix(suffix, "H") && suffix != "H0"
	}
	return strings.Contains(strings.ToLower(o.name), "increase")
}

// sameMeeting reports whether a FOMC question is about the meeting: the
// Kalshi event ticker ends in its year and month ("KXFEDDECISION-26DEC"),
// the Polymarket question names its month ("Fed Decision in December?").
func sameMeeting(q *question, meeting time.Time) bool {
	if len(q.outcomes) == 0 {
		return false
	}
	if q.source == predict.SourceKalshi {
		tag := strings.ToUpper(meeting.Format("06Jan"))
		return strings.Contains(q.outcomes[0].id, "-"+tag+"-")
	}
	return strings.Contains(q.title, meeting.Format("January"))
}
