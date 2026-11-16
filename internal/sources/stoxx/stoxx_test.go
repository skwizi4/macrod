package stoxx

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseFixtures(t *testing.T) {
	for file, want := range map[string]struct {
		symbol string
		last   float64
	}{
		"h_v1x.txt":  {"V1X", 18.66},
		"h_v2tx.txt": {"V2TX", 19.3109},
	} {
		body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "stoxx", file))
		if err != nil {
			t.Fatal(err)
		}
		obs, symbol, err := Parse(body)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		last := obs[len(obs)-1]
		if symbol != want.symbol || last.Date.Format(time.DateOnly) != FixtureTo || last.Value != want.last {
			t.Errorf("%s: symbol %s, last %s %v", file, symbol, last.Date.Format(time.DateOnly), last.Value)
		}
		if first := obs[0].Date; first.Year() != 2026 || first.Month() != time.January {
			t.Errorf("%s: first %s", file, first.Format(time.DateOnly))
		}
	}
}

func TestParseErrors(t *testing.T) {
	head := header + "\n"
	for name, body := range map[string]string{
		"html":          "<html><head><title>403 Forbidden</title></head></html>",
		"empty":         head,
		"two symbols":   head + "05.11.2026;V1X;19.58\n06.11.2026;V2TX;20.8\n",
		"bad date":      head + "2026-11-05;V1X;19.58\n",
		"bad value":     head + "05.11.2026;V1X;n/a\n",
		"zero":          head + "05.11.2026;V1X;0\n",
		"out of order":  head + "06.11.2026;V1X;20.8\n05.11.2026;V1X;19.58\n",
		"missing field": head + "05.11.2026;19.58\n",
	} {
		if _, _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	obs, _, err := Parse([]byte("\xef\xbb\xbf" + head + "05.11.2026;V1X;19.58\r\n\r\n06.11.2026;V1X;20.8\r\n"))
	if err != nil || len(obs) != 2 || obs[1].Value != 20.8 {
		t.Errorf("BOM and CRLF: %v, %v", obs, err)
	}
}
