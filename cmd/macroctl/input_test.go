package main

import "testing"

func TestInputArgs(t *testing.T) {
	for _, tt := range []struct {
		args                  []string
		series, value, period string
		ok                    bool
	}{
		{[]string{"IFO_CLIMATE", "90.2", "--period", "2026-10"}, "IFO_CLIMATE", "90.2", "2026-10", true},
		{[]string{"--period=2026-10", "ZEW_SITUATION", "-35.9"}, "ZEW_SITUATION", "-35.9", "2026-10", true}, // a negative value is not a flag
		{[]string{"IFO_CLIMATE", "--period", "2026-10", "90.2"}, "IFO_CLIMATE", "90.2", "2026-10", true},
		{[]string{"IFO_CLIMATE", "90.2"}, "", "", "", false},
		{[]string{"IFO_CLIMATE", "--period", "2026-10"}, "", "", "", false},
		{[]string{"IFO_CLIMATE", "90.2", "--period"}, "", "", "", false},
		{[]string{"IFO_CLIMATE", "90.2", "91", "--period", "2026-10"}, "", "", "", false},
	} {
		s, v, p, err := inputArgs(tt.args)
		if (err == nil) != tt.ok || s != tt.series || v != tt.value || p != tt.period {
			t.Errorf("inputArgs(%v) = %q, %q, %q, %v", tt.args, s, v, p, err)
		}
	}
}
