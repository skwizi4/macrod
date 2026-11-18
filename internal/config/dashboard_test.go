package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadPresetWith loads the us-equities preset after replacing old by new.
func loadPresetWith(t *testing.T, old, new string) (*Config, error) {
	t.Helper()
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "presets", "us-equities.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if old != "" && !strings.Contains(string(raw), old) {
		t.Fatalf("preset has no %q", old)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), old, new, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadAt(path, root)
}

func TestDashboardPresetLoads(t *testing.T) {
	cfg, err := loadPresetWith(t, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(cfg.Regions[0].Dashboard.Pages); got != 10 {
		t.Fatalf("%d pages, want 10", got)
	}
}

func TestDashboardValidation(t *testing.T) {
	cases := []struct{ name, old, new, want string }{
		{"unknown series", "ref: derived/NET_LIQUIDITY", "ref: derived/NET_LIQUIDTY", `unknown series "derived/NET_LIQUIDTY"`},
		{"unknown kind", `title_en: "2s10s spread", kind: line`, `title_en: "2s10s spread", kind: pie`, `unknown kind "pie"`},
		{"unknown view", "view: fed_path", "view: fedpath", `unknown view "fedpath"`},
		{"unknown transform", "transform: sum12m", "transform: sum12", `unknown transform "sum12"`},
		{"curve without compare", "compare_days: 28", "compare_days: 0", "curve needs compare_days"},
		{"misspelt field", "charts:", "chartz:", "chartz"},
		{"unknown allow", "    - fred/WALCL", "    - fred/NOPE", `unknown series "fred/NOPE"`},
		{"market price allowed", "    - fred/WALCL", `    - "yahoo/^VIX"`, "cannot be redistributed"},
		{"ratio allowed", "    - fred/WALCL", `    - "ratio/RSP:SPY"`, "cannot be redistributed"},
		{"unknown part", "ref: derived/NET_LIQUIDITY", "ref: derived/NET_LIQUIDITY, part: past", `part "past"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadPresetWith(t, tc.old, tc.new)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestKeyRowRef(t *testing.T) {
	cfg, err := loadPresetWith(t, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"NET_LIQUIDITY": "derived/NET_LIQUIDITY", "DGS10": "fred/DGS10", "^VIX": "yahoo/^VIX"} {
		if got, ok := cfg.KeyRowRef(KeyRow{ID: id}); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", id, got, ok, want)
		}
	}
	if _, ok := cfg.KeyRowRef(KeyRow{ID: KeyRowFedPath}); ok {
		t.Error("FED_PATH has no stored series")
	}
}
