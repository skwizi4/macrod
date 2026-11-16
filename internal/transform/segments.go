package transform

import (
	"time"

	"macro-collector/internal/model"
)

// SegmentFigures are a segment's figures for one quarter (phase 5d, ТЗ
// §20): revenue, its YoY, the segment's profit (operating, or gross where
// the company reports only that) and the profit margin in %. The margin is
// left out when the loss exceeds the revenue (Reality Labs): a −1000%
// margin says nothing the loss does not.
type SegmentFigures struct {
	Revenue, RevenueYoY, Profit, Margin Value
}

// SegmentAt returns the figures of a segment member for the quarter ending
// at end, from instance facts: quarters are derived as for company facts
// (a year to date difference, a fiscal Q4 = year − 9 months).
func SegmentAt(facts []model.CompanyFact, member, revenue, profit string, end time.Time) SegmentFigures {
	var own []model.CompanyFact
	for _, f := range facts {
		if f.Member == member {
			own = append(own, f)
		}
	}
	rev := Quarters(own, []string{revenue}, "USD", false)
	pr := Quarters(own, []string{profit}, "USD", false)
	s := SegmentFigures{Revenue: at(rev, end), RevenueYoY: QuarterYoY(rev, end), Profit: at(pr, end)}
	if s.Revenue.OK && s.Profit.OK && s.Revenue.V > 0 && s.Profit.V >= -s.Revenue.V {
		s.Margin = some(s.Profit.V / s.Revenue.V * 100)
	}
	return s
}
