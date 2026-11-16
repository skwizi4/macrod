package forecast

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"macro-collector/internal/store"
)

func known(name string) bool { return name == "NASDAQ" || name == "GER40" }

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseFiles(t *testing.T) {
	tests := []struct {
		file string
		want []store.ForecastItem
	}{
		// The last forecast block wins: an earlier one quotes last week.
		{"usa_master.md", []store.ForecastItem{{Instrument: "NASDAQ", Direction: Up, Invalidation: 24500}}},
		{"two_instruments.md", []store.ForecastItem{
			{Instrument: "GER40", Direction: Range, Invalidation: 23150.5},
			{Instrument: "NASDAQ", Direction: Down, Invalidation: 26000},
		}},
	}
	for _, tt := range tests {
		got, err := Parse(readTestdata(t, tt.file), known)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: %+v, %v; want %+v", tt.file, got, err, tt.want)
		}
	}
	// A copy cut before the closing fence still has its block.
	cut := []byte("text\n\n```yaml\nforecast:\n  - instrument: NASDAQ\n    direction: down\n    invalidation: 26000\n")
	if got, err := Parse(cut, known); err != nil || len(got) != 1 || got[0].Direction != Down {
		t.Errorf("unclosed block: %+v, %v", got, err)
	}
	if _, err := Parse(readTestdata(t, "no_block.md"), known); !errors.Is(err, ErrNoBlock) {
		t.Errorf("no_block.md: %v, want ErrNoBlock", err)
	}
}

func TestParseErrors(t *testing.T) {
	block := func(body string) []byte { return []byte("text\n\n```yaml\n" + body + "\n```\n") }
	tests := []struct{ name, body, wantErr string }{
		{"spaced number", "forecast:\n  - instrument: NASDAQ\n    direction: up\n    invalidation: 24 500", `"24 500": want a bare number`},
		{"comment in level", "forecast:\n  - instrument: NASDAQ\n    direction: up\n    invalidation: 24500 (лоу среды)", "want a bare number"},
		{"quoted number", "forecast:\n  - instrument: NASDAQ\n    direction: up\n    invalidation: \"24500\"", "want a bare number"},
		{"no level", "forecast:\n  - instrument: NASDAQ\n    direction: up", "invalidation: missing"},
		{"zero level", "forecast:\n  - instrument: NASDAQ\n    direction: up\n    invalidation: 0", "positive"},
		{"template direction", "forecast:\n  - instrument: NASDAQ\n    direction: up | down | range\n    invalidation: 24500", `direction "up | down | range"`},
		{"unknown instrument", "forecast:\n  - instrument: SPX\n    direction: up\n    invalidation: 6000", `unknown instrument "SPX"`},
		{"twice", "forecast:\n  - instrument: NASDAQ\n    direction: up\n    invalidation: 1\n  - instrument: NASDAQ\n    direction: down\n    invalidation: 2", "twice"},
		{"extra key", "forecast:\n  - instrument: NASDAQ\n    direction: up\n    invalidation: 24500\n    note: лоу", "field note not found"},
		{"extra top key", "forecast:\n  - instrument: NASDAQ\n    direction: up\n    invalidation: 24500\nhorizon: 1w", "field horizon not found"},
		{"empty", "forecast: []", "empty list"},
		{"broken yaml", "forecast:\n  - instrument: NASDAQ\n   direction: up", "forecast block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(block(tt.body), known)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
