package export

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"macro-collector/internal/config"
)

func TestParsePrompt(t *testing.T) {
	p, err := ParsePrompt([]byte("---\nversion: 1.10\n---\n\n# Role\n\nText\n\n---\n\nAfter a rule\n"), "usa/master")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Version, "1.10 (") || len(p.Version) != len("1.10 (1234567)") {
		t.Errorf("version = %q", p.Version)
	}
	if p.Body != "# Role\n\nText\n\n---\n\nAfter a rule\n" {
		t.Errorf("body = %q", p.Body)
	}
	want := "---\nprompt: usa/master\nprompt_version: \"" + p.Version + "\"\n---\n\n" + p.Body
	if got := p.Render(); got != want {
		t.Errorf("render = %q", got)
	}

	// CRLF, a BOM and a string version.
	q, err := ParsePrompt([]byte("\xef\xbb\xbf---\r\nversion: \"2026-11 a\"\r\n---\r\n# Role\r\n"), "eu/news")
	if err != nil || !strings.HasPrefix(q.Version, "2026-11 a (") || q.Body != "# Role\n" {
		t.Errorf("crlf: %+v, %v", q, err)
	}

	// The hash follows the file.
	a, _ := ParsePrompt([]byte("---\nversion: 1\n---\nA\n"), "x")
	b, _ := ParsePrompt([]byte("---\nversion: 1\n---\nB\n"), "x")
	a2, _ := ParsePrompt([]byte("---\nversion: 1\n---\nA\n"), "x")
	if a.Version == b.Version || a.Version != a2.Version {
		t.Errorf("hash: %q %q %q", a.Version, b.Version, a2.Version)
	}
}

func TestParsePromptErrors(t *testing.T) {
	for name, raw := range map[string]string{
		"no front matter":  "# Role\n",
		"not closed":       "---\nversion: 1\n# Role\n",
		"no version":       "---\n---\n# Role\n",
		"empty version":    "---\nversion: \"\"\n---\n# Role\n",
		"version list":     "---\nversion: [1]\n---\n# Role\n",
		"unknown key":      "---\nversion: 1\nverison: 2\n---\n# Role\n",
		"empty body":       "---\nversion: 1\n---\n\n",
		"front not a map":  "---\n- 1\n---\n# Role\n",
		"indented opening": " ---\nversion: 1\n---\n# Role\n",
	} {
		if _, err := ParsePrompt([]byte(raw), "x"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestLoadPromptMissing(t *testing.T) {
	_, err := LoadPrompt(filepath.Join(t.TempDir(), "none.md"), "usa/x")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want not exist", err)
	}
}

func TestCheckPrompts(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.Export.PromptsDir = t.TempDir()
	n, err := CheckPrompts(cfg)
	if n != 0 || err == nil || !strings.Contains(err.Error(), filepath.Join("usa", "master.md")) {
		t.Errorf("empty dir: %d, %v", n, err)
	}
	r := &cfg.Regions[0]
	for i := range r.Agents {
		path := cfg.PromptPath(r, &r.Agents[i])
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte("---\nversion: 1\n---\n# "+r.Agents[i].ID+"\n"), 0o644)
	}
	if n, err := CheckPrompts(cfg); n != len(r.Agents) || err != nil {
		t.Errorf("all prompts: %d, %v", n, err)
	}
}

// TestRepoPrompts guards prompts/: every agent of the example config and of
// every preset has a prompt that parses (ТЗ §21.5).
func TestRepoPrompts(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(repoRoot, "presets", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, filepath.Join(repoRoot, "config.example.yaml"))
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			t.Setenv(config.EnvDataDir, t.TempDir())
			cfg, err := config.LoadAt(p, repoRoot)
			if err != nil {
				t.Fatal(err)
			}
			n, err := CheckPrompts(cfg)
			if err != nil {
				t.Fatal(err)
			}
			agents := 0
			for _, r := range cfg.Regions {
				agents += len(r.Agents)
			}
			if n != agents || n == 0 {
				t.Errorf("%d prompts for %d agents", n, agents)
			}
		})
	}
}
