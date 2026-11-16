package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"macro-collector/internal/export"
)

// The JSON tree (D23) is for machines: the message lists what agents get.
func TestTextLeavesOutJSON(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	md := write("usa/monetary/current_week.md", "---\nperiod: P\nsources_failed: []\n---\nbody\n")
	js := write("json/usa/monetary/current_week.json", `{"header":{}}`)
	arch := write("archive/2026-W44/json/usa/monetary/current_week.json", `{}`)
	got := Text(export.Exported{Kind: export.KindWeekly, Region: "usa", ISOWeek: "2026-W44", Files: []string{md, js, arch}}, "США", "")
	if !strings.Contains(got, "Файлы (1):") || !strings.Contains(got, "monetary — current_week.md") || strings.Contains(got, ".json") {
		t.Errorf("message:\n%s", got)
	}
}
