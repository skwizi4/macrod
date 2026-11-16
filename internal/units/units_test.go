package units

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckFRED(t *testing.T) {
	if err := CheckFRED("WALCL", MillionsUSD); err != nil {
		t.Error(err)
	}
	if err := CheckFRED("WALCL", "Billions of U.S. Dollars"); err == nil {
		t.Error("mismatch not reported")
	}
	if err := CheckFRED("GDPC1", "anything"); err != nil {
		t.Errorf("unlisted series checked: %v", err)
	}
}

func TestToBillions(t *testing.T) {
	for u, want := range map[string]float64{MillionsUSD: 0.001, MillionsDollar: 0.001, BillionsUSD: 1} {
		if got, err := ToBillions(u); err != nil || got != want {
			t.Errorf("ToBillions(%q) = %v, %v", u, got, err)
		}
	}
	if _, err := ToBillions(Percent); err == nil {
		t.Error("Percent accepted as dollars")
	}
}

// The recorded fred/series responses must agree with the table.
func TestExpectedMatchesFixtures(t *testing.T) {
	for _, id := range []string{"DGS10", "PAYEMS"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fred", "series_"+id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var resp struct {
			Seriess []struct {
				Units string `json:"units"`
			} `json:"seriess"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatal(err)
		}
		if err := CheckFRED(id, resp.Seriess[0].Units); err != nil {
			t.Error(err)
		}
	}
}
