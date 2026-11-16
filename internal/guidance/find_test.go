package guidance

import (
	"os"
	"testing"

	"macro-collector/internal/sources/irtranscript"
)

func TestFindGOOGLTranscript(t *testing.T) {
	page, err := os.ReadFile("../../testdata/ir/googl_2026-Q3-Earnings-Call.html")
	if err != nil {
		t.Fatal(err)
	}
	text, err := irtranscript.Text(page)
	if err != nil {
		t.Fatal(err)
	}
	got := Find(text)
	if len(got) != 1 {
		t.Fatalf("matches = %+v, want 1", got)
	}
	m := got[0]
	want := "Moving to investments, our full‑year 2026 CapEx guidance range of $195‑$205 billion remain unchanged."
	if m.Year != 2026 || m.Low != 195 || m.High != 205 || m.Quote != want {
		t.Fatalf("match = %+v", m)
	}
}

func TestFind(t *testing.T) {
	for _, tc := range []struct {
		name      string
		text      string
		ok        bool
		year      int
		low, high float64
	}{
		{"dash nbhyphen", "Our full‑year 2026 CapEx guidance range of $195‑$205 billion remains unchanged.", true, 2026, 195, 205},
		{"en dash", "We expect 2026 capital expenditures of $195–$205 billion.", true, 2026, 195, 205},
		{"to with units", "We now expect 2026 CapEx of $195 billion to $205 billion.", true, 2026, 195, 205},
		{"between", "We expect capex between $195 and $205 billion in 2026.", true, 2026, 195, 205},
		{"between units", "Capital expenditures for 2026 will be between $195 billion and $205 billion.", true, 2026, 195, 205},
		{"B suffix", "We expect full-year 2026 capex of $195B to $205B.", true, 2026, 195, 205},
		{"approx", "We expect 2026 CapEx of ~$195 billion to ~$205 billion.", true, 2026, 195, 205},
		{"decimal", "We expect capex of $91.5-$93.5 billion in 2026.", true, 2026, 91.5, 93.5},
		{"single", "We expect 2027 capital expenditures of approximately $250 billion.", true, 2027, 250, 250},
		{"quarter amount and range", "Q3 CapEx was $51B, bringing full-year 2026 expectations to $195B–$205B.", true, 2026, 195, 205},
		{"quarter single", "We invested: third quarter 2026 CapEx was $51.4 billion.", false, 0, 0, 0},
		{"no year", "CapEx was $51.4 billion in the third quarter.", false, 0, 0, 0},
		{"no amount", "We continue to expect our CapEx to increase significantly in 2027.", false, 0, 0, 0},
		{"two years", "We expect CapEx of $195 billion in 2026 to rise in 2027.", false, 0, 0, 0},
		{"no capex", "We expect revenue for 2026 of $195-$205 billion.", false, 0, 0, 0},
		{"million", "We expect 2026 capex of $195-$205 million.", false, 0, 0, 0},
		{"two ranges", "We expect 2026 capex of $195-$205 billion, up from $170-$180 billion.", false, 0, 0, 0},
		{"two singles", "We expect 2026 capex of $200 billion versus $180 billion planned.", false, 0, 0, 0},
		{"money raised", "In June 2026, we issued stock for net proceeds of $49.6 billion, to be used for general corporate purposes, including capital expenditures.", false, 0, 0, 0},
		{"no outlook word", "Capital expenditures in 2026 were $180 billion.", false, 0, 0, 0},
		{"press release single", "We are confident about the opportunities ahead, and to accelerate our progress, we expect to invest approximately $75 billion in capital expenditures in 2025.”", true, 2025, 75, 75},
		{"press release to", "Our 2026 CapEx investments are anticipated to be in the range of $175 to $185 billion.”", true, 2026, 175, 185},
		{"and without between", "We expect in 2026 capex of $51 billion and $9 billion of leases.", false, 0, 0, 0},
	} {
		got := Find(tc.text)
		if !tc.ok {
			if len(got) != 0 {
				t.Errorf("%s: matches = %+v, want none", tc.name, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Year != tc.year || got[0].Low != tc.low || got[0].High != tc.high {
			t.Errorf("%s: matches = %+v, want %d %v–%v", tc.name, got, tc.year, tc.low, tc.high)
		}
	}
}

func TestFindDuplicateRangeOnce(t *testing.T) {
	text := "Our 2026 capex guidance is $195-$205 billion.\nAgain, 2026 capex of $195-$205 billion."
	if got := Find(text); len(got) != 1 || got[0].Quote != "Our 2026 capex guidance is $195-$205 billion." {
		t.Fatalf("matches = %+v", got)
	}
}
