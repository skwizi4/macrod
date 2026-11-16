package fedtexts

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseDecisionRecorded(t *testing.T) {
	for file, want := range map[string]Decision{
		// A raise, unanimous.
		"monetary20260916a.htm": {Action: ActionRaise, MoveBP: 25, Range: "3.75–4.00%", For: 12},
		// Unchanged, three for a raise.
		"monetary20260729a.htm": {Action: ActionMaintain, Range: "3.50–3.75%", For: 9, Against: 3, Dissents: []Dissent{
			{Names: []string{"Beth M. Hammack", "Neel Kashkari", "Lorie K. Logan"}, Preferred: ActionRaise, MoveBP: 25}}},
	} {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fed", file))
		if err != nil {
			t.Fatal(err)
		}
		p, err := parsePage(b)
		if err != nil {
			t.Fatal(err)
		}
		if got := ParseDecision(p.Body); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, want %+v", file, got, want)
		}
	}
}

// Statements before 2026 list the voters and word dissents differently.
func TestParseDecisionVoterList(t *testing.T) {
	body := "The Committee decided to lower the target range for the federal funds rate by 1/4 percentage point to 3-1/2 to 3-3/4 percent.\n\n" +
		"Voting for the monetary policy action were Jerome H. Powell, Chair; John C. Williams, Vice Chair; Michael S. Barr; Michelle W. Bowman; " +
		"Lisa D. Cook; Philip N. Jefferson; Christopher J. Waller; and Susan M. Collins.\n\n" +
		"Voting against this action was Stephen I. Miran, who preferred to lower the target range for the federal funds rate by 1/2 percentage point at this meeting. " +
		"Voting against this action were Austan D. Goolsbee and Jeffrey R. Schmid, who preferred no change to the target range for the federal funds rate at this meeting.\n"
	got := ParseDecision(body)
	want := Decision{Action: ActionLower, MoveBP: 25, Range: "3.50–3.75%", For: 8, Against: 3, Dissents: []Dissent{
		{Names: []string{"Stephen I. Miran"}, Preferred: ActionLower, MoveBP: 50},
		{Names: []string{"Austan D. Goolsbee", "Jeffrey R. Schmid"}, Preferred: ActionMaintain},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if d := ParseDecision("Nothing here."); !reflect.DeepEqual(d, Decision{}) {
		t.Errorf("empty: %+v", d)
	}
}
