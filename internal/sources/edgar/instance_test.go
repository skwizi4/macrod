package edgar

import (
	"slices"
	"testing"

	"macro-collector/internal/model"
)

func rpoTotals(facts []model.CompanyFact) map[string]float64 {
	out := map[string]float64{}
	for _, f := range facts {
		if f.Member == "" && f.Concept == RPOConcept {
			out[f.End.Format("2006-01-02")] = f.Value
		}
	}
	return out
}

func segment(facts []model.CompanyFact, member, concept, start, end string) (float64, bool) {
	i := slices.IndexFunc(facts, func(f model.CompanyFact) bool {
		return f.Member == member && f.Concept == concept && f.Start.Format("2006-01-02") == start && f.End.Format("2006-01-02") == end
	})
	if i < 0 {
		return 0, false
	}
	return facts[i].Value, true
}

const (
	rev = "RevenueFromContractWithCustomerExcludingAssessedTax"
	oi  = "OperatingIncomeLoss"
)

// AMZN Q2 2026 10-Q (recorded 2026-11-01, trimmed): segments on the segment
// axis alone; the RPO total is the one bucket on the expected-timing axis
// (496 bn on 2026-06-30), the OpenAI contract (major customer axis, 38 bn)
// is not part of it (ТЗ §20.2a).
func TestParseInstanceAmazon(t *testing.T) {
	f := Filing{Accession: "0001018724-26-000026", Form: "10-Q", FilingDate: day("2026-07-31")}
	facts, err := ParseInstance(read(t, "instance_AMZN_0001018724-26-000026.xml"),
		SegmentSelect(rev, oi, []string{"AmazonWebServicesSegmentMember", "NorthAmericaSegmentMember"}), f)
	if err != nil {
		t.Fatal(err)
	}
	if got := rpoTotals(facts); len(got) != 1 || got["2026-06-30"] != 496e9 {
		t.Errorf("RPO totals = %v, want only 2026-06-30: 496 bn", got)
	}
	if v, ok := segment(facts, "AmazonWebServicesSegmentMember", rev, "2026-04-01", "2026-06-30"); !ok || v <= 0 {
		t.Errorf("AWS Q2 revenue = %v, %v", v, ok)
	}
	if _, ok := segment(facts, "AmazonWebServicesSegmentMember", oi, "2025-04-01", "2025-06-30"); !ok {
		t.Error("no AWS operating income a year earlier (the 10-Q carries it)")
	}
	if slices.ContainsFunc(facts, func(x model.CompanyFact) bool { return x.Member == "InternationalSegmentMember" }) {
		t.Error("a member not selected was kept")
	}
	for _, x := range facts {
		if x.Accession != f.Accession || x.Form != "10-Q" || !x.Filed.Equal(f.FilingDate) || x.Unit != "USD" {
			t.Errorf("fact = %+v", x)
		}
	}
}

// NVDA files segments with the consolidation axis on operating segments
// too; TSLA has an RPO total without dimensions and product buckets — the
// total alone counts, nothing is added twice.
func TestParseInstanceAxes(t *testing.T) {
	nvda, err := ParseInstance(read(t, "instance_NVDA_0001045810-26-000075.xml"),
		SegmentSelect("Revenues", oi, []string{"ComputeAndNetworkingSegmentMember"}), Filing{Accession: "n"})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := segment(nvda, "ComputeAndNetworkingSegmentMember", "Revenues", "2026-04-27", "2026-07-26"); !ok || v < 88.2e9 || v > 88.4e9 {
		t.Errorf("Compute & Networking Q2 FY27 revenue = %v, %v", v, ok)
	}
	tsla, err := ParseInstance(read(t, "instance_TSLA_0001628280-26-066775.xml"),
		SegmentSelect("Revenues", "GrossProfit", []string{"AutomotiveSegmentMember"}), Filing{Accession: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rpoTotals(tsla); got["2026-09-30"] != 10.73e9 {
		t.Errorf("TSLA RPO totals = %v, want 10.73 bn on 2026-09-30", got)
	}
	if v, ok := segment(tsla, "AutomotiveSegmentMember", "GrossProfit", "2026-07-01", "2026-09-30"); !ok || v <= 0 {
		t.Errorf("TSLA automotive gross profit = %v, %v", v, ok)
	}
}

const synthetic = `<?xml version="1.0"?>
<xbrli:xbrl xmlns:xbrli="http://www.xbrl.org/2003/instance" xmlns:xbrldi="http://xbrl.org/2006/xbrldi"
  xmlns:us-gaap="http://fasb.org/us-gaap/2025" xmlns:iso4217="http://www.xbrl.org/2003/iso4217" xmlns:srt="http://fasb.org/srt/2025">
<xbrli:unit id="usd"><xbrli:measure>iso4217:USD</xbrli:measure></xbrli:unit>
<xbrli:context id="b1"><xbrli:entity><xbrli:segment>
  <xbrldi:typedMember dimension="us-gaap:RevenueRemainingPerformanceObligationExpectedTimingOfSatisfactionStartDateAxis"><us-gaap:date.domain>2026-10-01</us-gaap:date.domain></xbrldi:typedMember>
</xbrli:segment></xbrli:entity><xbrli:period><xbrli:instant>2026-09-30</xbrli:instant></xbrli:period></xbrli:context>
<xbrli:context id="b2"><xbrli:entity><xbrli:segment>
  <xbrldi:typedMember dimension="us-gaap:RevenueRemainingPerformanceObligationExpectedTimingOfSatisfactionStartDateAxis"><us-gaap:date.domain>2027-10-01</us-gaap:date.domain></xbrldi:typedMember>
</xbrli:segment></xbrli:entity><xbrli:period><xbrli:instant>2026-09-30</xbrli:instant></xbrli:period></xbrli:context>
<xbrli:context id="c1"><xbrli:entity><xbrli:segment>
  <xbrldi:explicitMember dimension="srt:MajorCustomersAxis">x:BigCustomerMember</xbrldi:explicitMember>
  <xbrldi:typedMember dimension="us-gaap:RevenueRemainingPerformanceObligationExpectedTimingOfSatisfactionStartDateAxis"><us-gaap:date.domain>2026-10-01</us-gaap:date.domain></xbrldi:typedMember>
</xbrli:segment></xbrli:entity><xbrli:period><xbrli:instant>2026-09-30</xbrli:instant></xbrli:period></xbrli:context>
<xbrli:context id="t0"><xbrli:entity/><xbrli:period><xbrli:instant>2025-09-30</xbrli:instant></xbrli:period></xbrli:context>
<xbrli:context id="t1"><xbrli:entity><xbrli:segment>
  <xbrldi:typedMember dimension="us-gaap:RevenueRemainingPerformanceObligationExpectedTimingOfSatisfactionStartDateAxis"><us-gaap:date.domain>2025-10-01</us-gaap:date.domain></xbrldi:typedMember>
</xbrli:segment></xbrli:entity><xbrli:period><xbrli:instant>2025-09-30</xbrli:instant></xbrli:period></xbrli:context>
<us-gaap:RevenueRemainingPerformanceObligation contextRef="b1" unitRef="usd" decimals="-9">300000000000</us-gaap:RevenueRemainingPerformanceObligation>
<us-gaap:RevenueRemainingPerformanceObligation contextRef="b2" unitRef="usd" decimals="-9">200000000000</us-gaap:RevenueRemainingPerformanceObligation>
<us-gaap:RevenueRemainingPerformanceObligation contextRef="c1" unitRef="usd" decimals="-9">40000000000</us-gaap:RevenueRemainingPerformanceObligation>
<us-gaap:RevenueRemainingPerformanceObligation contextRef="t0" unitRef="usd" decimals="-9">350000000000</us-gaap:RevenueRemainingPerformanceObligation>
<us-gaap:RevenueRemainingPerformanceObligation contextRef="t1" unitRef="usd" decimals="-9">120000000000</us-gaap:RevenueRemainingPerformanceObligation>
</xbrli:xbrl>`

// Two timing buckets (12 months and after) sum to the total; a customer's
// share is left out; with a total without dimensions filed, the buckets of
// that date are not added to it.
func TestParseInstanceRPOBuckets(t *testing.T) {
	facts, err := ParseInstance([]byte(synthetic), InstanceSelect{}, Filing{Accession: "s"})
	if err != nil {
		t.Fatal(err)
	}
	got := rpoTotals(facts)
	if len(got) != 2 || got["2026-09-30"] != 500e9 || got["2025-09-30"] != 350e9 {
		t.Errorf("RPO totals = %v, want 2026-09-30: 500 bn (two buckets), 2025-09-30: 350 bn (the total)", got)
	}
}

// Trimming keeps what the parser uses: a trimmed instance parses the same,
// and trimming it again changes nothing.
func TestTrimInstance(t *testing.T) {
	body := read(t, "instance_GOOGL_0001652044-26-000071.xml")
	sel := SegmentSelect(rev, oi, []string{"GoogleCloudMember", "GoogleServicesMember", "AllOtherSegmentsMember"})
	trimmed, err := TrimInstance(body, sel)
	if err != nil {
		t.Fatal(err)
	}
	a, err1 := ParseInstance(body, sel, Filing{})
	b, err2 := ParseInstance(trimmed, sel, Filing{})
	if err1 != nil || err2 != nil || len(a) == 0 || !slices.EqualFunc(a, b, func(x, y model.CompanyFact) bool { return x == y }) {
		t.Errorf("parse before/after trim: %d/%d facts, %v %v", len(a), len(b), err1, err2)
	}
	again, err := TrimInstance(trimmed, sel)
	if err != nil || string(again) != string(trimmed) {
		t.Errorf("trim is not stable: %d → %d bytes, %v", len(trimmed), len(again), err)
	}
}
