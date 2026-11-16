package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWritesJSONFileAndConsole(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	var console bytes.Buffer
	root, closer, err := New(Options{Dir: dir, App: "test", Level: slog.LevelInfo, Console: &console})
	if err != nil {
		t.Fatal(err)
	}
	log := For(root, "fred")
	log.Debug("hidden")
	log.Info("fetched", "series", "DGS10", "rows", 3)
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "test.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("file has %d lines, want 1 (debug filtered): %q", len(lines), raw)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec[SourceKey] != "fred" || rec["series"] != "DGS10" || rec["msg"] != "fetched" {
		t.Errorf("record = %v", rec)
	}
	if out := console.String(); !strings.Contains(out, "source=fred") || !strings.Contains(out, "msg=fetched") {
		t.Errorf("console = %q", out)
	}
}
