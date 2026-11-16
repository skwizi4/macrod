package config

import (
	"strings"
	"testing"
)

func TestResolveSeries(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, source, id string
		ok               bool
	}{
		{"DGS10", SourceFRED, "DGS10", true},
		{"fred/DGS10", SourceFRED, "DGS10", true},
		{"UST_2S10S", SourceDerived, "UST_2S10S", true},
		{"derived/UST_2S10S", SourceDerived, "UST_2S10S", true},
		{"^GDAXI", SourceYahoo, "^GDAXI", true},
		{"yahoo/^GDAXI", SourceYahoo, "^GDAXI", true},
		{"TGA_DTS", SourceTreasury, "TGA_DTS", true},
		{"derived/DGS10", "", "", false}, // the source is part of the address
		{"NOPE", "", "", false},
	} {
		s, ok := cfg.ResolveSeries(tt.name)
		if ok != tt.ok || s.Source != tt.source || s.ID != tt.id {
			t.Errorf("ResolveSeries(%q) = %s/%s, %v; want %s/%s, %v", tt.name, s.Source, s.ID, ok, tt.source, tt.id, tt.ok)
		}
		if ok && s.Quality != QualityOfficial {
			t.Errorf("ResolveSeries(%q): quality %q, want the default %q", tt.name, s.Quality, QualityOfficial)
		}
	}
}

func TestSeriesFullReferenceInTable(t *testing.T) {
	body := strings.Replace(minimal, `tables: { rows: [DGS10] }`, `tables: { rows: ["fred/DGS10"] }`, 1)
	if body == minimal {
		t.Fatal("fixture changed")
	}
	if _, err := Load(writeConfig(t, body)); err != nil {
		t.Fatalf("a table names a series by source/id: %v", err)
	}
}

func TestSeriesValidation(t *testing.T) {
	derived := `- { name: UST_2S10S, formula: "DGS10 - DGS2", agent: usa_fiscal, unit: bp, transforms: [level, d] }`
	for _, tt := range []struct{ name, new, want string }{
		{"id of two sources", strings.Replace(derived, "name: UST_2S10S", "name: DGS10", 1), `series "DGS10" is configured in fred and derived`},
		{"unknown formula name", strings.Replace(derived, "name: UST_2S10S", "name: UST_5S30S", 1), `derived[0] UST_5S30S: unknown formula name (want one of`},
		{"formula differs", strings.Replace(derived, "DGS10 - DGS2", "DGS2 - DGS10", 1), `formula "DGS2 - DGS10" differs from the implemented "DGS10 - DGS2"`},
		{"input not configured", strings.Replace(derived, `name: UST_2S10S, formula: "DGS10 - DGS2"`, `name: UST_3M10Y, formula: "DGS10 - DGS3MO"`, 1), `derived[0] UST_3M10Y: input DGS3MO is not a configured series`},
		{"unknown quality", strings.Replace(derived, "unit: bp", "unit: bp, quality: guess", 1), `derived/UST_2S10S: quality "guess": want one of official, proxy, manual`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(minimal, derived) {
				t.Fatal("fixture changed")
			}
			_, err := Load(writeConfig(t, strings.Replace(minimal, derived, tt.new, 1)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSeriesQualityAndLabel(t *testing.T) {
	derived := `unit: bp, transforms: [level, d] }`
	body := strings.Replace(minimal, derived, `unit: bp, transforms: [level, d], quality: proxy, label: "Curve proxy" }`, 1)
	if body == minimal {
		t.Fatal("fixture changed")
	}
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := cfg.ResolveSeries("UST_2S10S")
	if !ok || s.Quality != QualityProxy || s.Label != "Curve proxy" {
		t.Errorf("series = %+v, %v; want quality proxy and the configured label", s, ok)
	}
}
