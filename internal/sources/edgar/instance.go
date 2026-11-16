package edgar

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/model"
)

// XBRL instances (phase 5d, ТЗ §20, DECISIONS D15). Company facts hold only
// facts without dimensions: segments (AWS, Google Cloud) and AMZN's RPO,
// filed with the expected-timing axis, are missing there. The instance of
// the same 10-Q/10-K is structured XML with every fact and its contexts;
// it is read for these facts only. HTML documents are never parsed.

// Axes and members of the selection (local names, LOOKUP S2, S4).
const (
	segmentAxis       = "StatementBusinessSegmentsAxis"
	consolidationAxis = "ConsolidationItemsAxis"
	operatingSegments = "OperatingSegmentsMember"
	timingAxis        = "RevenueRemainingPerformanceObligationExpectedTimingOfSatisfactionStartDateAxis"
	// RPOConcept is the remaining performance obligations tag.
	RPOConcept = "RevenueRemainingPerformanceObligation"
)

// InstanceSelect names the segment facts to keep: concepts (revenue,
// operating income) of the members. The RPO total is always kept.
type InstanceSelect struct {
	Concepts []string
	Members  []string // local names: AmazonWebServicesSegmentMember
}

// InstanceURL is the instance of a 10-Q/10-K: next to the primary document,
// "<document without .htm>_htm.xml" (LOOKUP S1).
func (c *Client) InstanceURL(cik string, f Filing) string {
	return c.FilingURL(cik, f.Accession) + "/" + strings.TrimSuffix(f.PrimaryDocument, ".htm") + "_htm.xml"
}

// InstanceFacts fetches the instance of a filing and returns the selected
// facts, Member set for segments and "" for the RPO total.
func (c *Client) InstanceFacts(ctx context.Context, cik string, f Filing, sel InstanceSelect) ([]model.CompanyFact, error) {
	if f.PrimaryDocument == "" {
		return nil, fmt.Errorf("edgar instance %s: no primary document", f.Accession)
	}
	body, err := c.get(ctx, c.InstanceURL(cik, f))
	if err != nil {
		return nil, fmt.Errorf("edgar instance %s: %w", f.Accession, err)
	}
	facts, err := ParseInstance(body, sel, f)
	if err != nil {
		return nil, fmt.Errorf("edgar instance %s: %w", f.Accession, err)
	}
	return facts, nil
}

type xMember struct {
	Dimension string `xml:"dimension,attr"`
	Value     string `xml:",chardata"`
}

type xDims struct {
	Explicit []xMember `xml:"explicitMember"`
	Typed    []xMember `xml:"typedMember"`
}

type xContext struct {
	ID       string `xml:"id,attr"`
	Segment  xDims  `xml:"entity>segment"`
	Scenario xDims  `xml:"scenario"`
	Start    string `xml:"period>startDate"`
	End      string `xml:"period>endDate"`
	Instant  string `xml:"period>instant"`
}

type xUnit struct {
	ID      string   `xml:"id,attr"`
	Measure []string `xml:"measure"`
}

// xbrlContext is a resolved context: period, explicit dimensions (axis →
// member, local names) and typed axes.
type xbrlContext struct {
	start, end time.Time
	explicit   map[string]string
	typed      []string
}

type rawFact struct {
	concept, ctx, unit, value string
}

func local(qname string) string {
	qname = strings.TrimSpace(qname)
	if i := strings.LastIndexByte(qname, ':'); i >= 0 {
		return qname[i+1:]
	}
	return qname
}

// scan reads the contexts, units and the facts of the concepts (us-gaap
// namespace only).
func scan(body []byte, concepts []string) (map[string]xbrlContext, map[string]string, []rawFact, error) {
	contexts := map[string]xbrlContext{}
	units := map[string]string{}
	var facts []rawFact
	d := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, nil, fmt.Errorf("decode: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch {
		case se.Name.Local == "context":
			var x xContext
			if err := d.DecodeElement(&x, &se); err != nil {
				return nil, nil, nil, fmt.Errorf("context: %w", err)
			}
			c := xbrlContext{explicit: map[string]string{}}
			for _, m := range slices.Concat(x.Segment.Explicit, x.Scenario.Explicit) {
				c.explicit[local(m.Dimension)] = local(m.Value)
			}
			for _, m := range slices.Concat(x.Segment.Typed, x.Scenario.Typed) {
				c.typed = append(c.typed, local(m.Dimension))
			}
			end := x.End
			if x.Instant != "" {
				end = x.Instant
			}
			if c.end, err = time.Parse(time.DateOnly, strings.TrimSpace(end)); err != nil {
				return nil, nil, nil, fmt.Errorf("context %s: end %q: %w", x.ID, end, err)
			}
			if x.Start != "" {
				if c.start, err = time.Parse(time.DateOnly, strings.TrimSpace(x.Start)); err != nil {
					return nil, nil, nil, fmt.Errorf("context %s: start %q: %w", x.ID, x.Start, err)
				}
			}
			contexts[x.ID] = c
		case se.Name.Local == "unit":
			var x xUnit
			if err := d.DecodeElement(&x, &se); err != nil {
				return nil, nil, nil, fmt.Errorf("unit: %w", err)
			}
			if len(x.Measure) == 1 {
				units[x.ID] = local(x.Measure[0])
			}
		case strings.Contains(se.Name.Space, "us-gaap") && slices.Contains(concepts, se.Name.Local):
			f := rawFact{concept: se.Name.Local}
			nilled := false
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "contextRef":
					f.ctx = a.Value
				case "unitRef":
					f.unit = a.Value
				case "nil":
					nilled = a.Value == "true"
				}
			}
			if err := d.DecodeElement(&f.value, &se); err != nil {
				return nil, nil, nil, fmt.Errorf("%s: %w", f.concept, err)
			}
			if f.ctx != "" && !nilled {
				facts = append(facts, f)
			}
		}
	}
	return contexts, units, facts, nil
}

// ParseInstance returns the selected facts of an instance (LOOKUP S2, S4):
//   - segment facts: a duration fact of a selected concept whose only
//     dimensions are the segment axis with a selected member, and maybe the
//     consolidation axis on operating segments (NVDA, AAPL, GOOGL);
//   - the RPO total per balance date: the fact without dimensions when filed
//     (NVDA, MSFT, GOOGL, AVGO, TSLA); otherwise the sum of the facts whose
//     only dimension is the expected-timing axis — its buckets (AMZN: one
//     bucket "from 2026-10-01", 578 bn). A fact with any other axis (one
//     customer, a product line) is never part of the total.
//
// Only USD facts are kept; the filing gives accession, form and date.
func ParseInstance(body []byte, sel InstanceSelect, f Filing) ([]model.CompanyFact, error) {
	contexts, units, raw, err := scan(body, append(slices.Clone(sel.Concepts), RPOConcept))
	if err != nil {
		return nil, err
	}
	type key struct {
		member, concept string
		start, end      time.Time
	}
	var out []model.CompanyFact
	seen := map[key]bool{}
	add := func(k key, v float64) {
		if seen[k] {
			return // the same fact repeated in the instance
		}
		seen[k] = true
		out = append(out, model.CompanyFact{Member: k.member, Concept: k.concept, Unit: "USD", Start: k.start, End: k.end, Value: v,
			Accession: f.Accession, Form: f.Form, Filed: f.FilingDate})
	}
	direct := map[time.Time]float64{}
	buckets := map[time.Time]float64{}
	for _, r := range raw {
		c, ok := contexts[r.ctx]
		if !ok || units[r.unit] != "USD" {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(r.value), 64)
		if err != nil {
			return nil, fmt.Errorf("%s %s: value %q: %w", r.concept, r.ctx, r.value, err)
		}
		if r.concept == RPOConcept && c.start.IsZero() {
			switch {
			case len(c.explicit) == 0 && len(c.typed) == 0:
				direct[c.end] = v
			case len(c.explicit) == 0 && !slices.ContainsFunc(c.typed, func(a string) bool { return a != timingAxis }):
				buckets[c.end] += v
			}
		}
		if !slices.Contains(sel.Concepts, r.concept) || c.start.IsZero() || len(c.typed) > 0 {
			continue
		}
		member, ok := c.explicit[segmentAxis]
		if !ok || !slices.Contains(sel.Members, member) {
			continue
		}
		other := len(c.explicit) - 1
		if cm, ok := c.explicit[consolidationAxis]; ok && cm == operatingSegments {
			other--
		}
		if other == 0 {
			add(key{member, r.concept, c.start, c.end}, v)
		}
	}
	for end, v := range direct {
		add(key{"", RPOConcept, time.Time{}, end}, v)
	}
	for end, v := range buckets {
		if _, ok := direct[end]; !ok {
			add(key{"", RPOConcept, time.Time{}, end}, v)
		}
	}
	slices.SortFunc(out, func(a, b model.CompanyFact) int {
		return strings.Compare(a.Member+a.Concept+a.End.Format(time.DateOnly)+a.Start.Format(time.DateOnly),
			b.Member+b.Concept+b.End.Format(time.DateOnly)+b.Start.Format(time.DateOnly))
	})
	return out, nil
}
