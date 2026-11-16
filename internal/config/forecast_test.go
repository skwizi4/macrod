package config

import (
	"strings"
	"testing"
)

func TestForecastDefaults(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	c, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.Forecast.Enabled || c.Forecast.Agent != "master" || c.Forecast.RangePct != 1 {
		t.Errorf("forecast %+v", c.Forecast)
	}
}

func TestForecastValidate(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	ok := "forecast:\n  enabled: true\n  agent: monetary\n  range_pct: 0.5\n  instruments:\n    - { name: NASDAQ, price: \"^NDX\", fallback: DGS10 }\n"
	c, err := Load(writeConfig(t, minimal+ok))
	if err != nil {
		t.Fatal(err)
	}
	if in, found := c.ForecastInstrument("NASDAQ"); !found || in.Price != "^NDX" || c.Forecast.RangePct != 0.5 {
		t.Errorf("instrument %+v %v", in, found)
	}
	tests := []struct{ name, extra, wantErr string }{
		{"unknown price", "forecast:\n  enabled: true\n  agent: monetary\n  instruments:\n    - { name: X, price: NOPE }\n", "price \"NOPE\""},
		{"unknown fallback", "forecast:\n  enabled: true\n  agent: monetary\n  instruments:\n    - { name: X, price: \"^NDX\", fallback: NOPE }\n", "fallback \"NOPE\""},
		{"duplicate", "forecast:\n  enabled: true\n  agent: monetary\n  instruments:\n    - { name: X, price: \"^NDX\" }\n    - { name: X, price: \"^NDX\" }\n", "duplicate name"},
		{"bad name", "forecast:\n  enabled: true\n  agent: monetary\n  instruments:\n    - { name: \"N X\", price: \"^NDX\" }\n", "name \"N X\""},
		{"empty", "forecast:\n  enabled: true\n  agent: monetary\n", "instruments: empty"},
		{"negative", "forecast:\n  enabled: true\n  agent: monetary\n  range_pct: -1\n  instruments:\n    - { name: X, price: \"^NDX\" }\n", "range_pct"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, minimal+tt.extra))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
