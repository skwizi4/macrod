package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSiteDefaults(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	c, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.Site.Enabled || c.Site.Branch != "main" || c.Site.Dir != filepath.Join(c.DataDir, "site") {
		t.Errorf("site %+v", c.Site)
	}
	if got := c.Site.Message("2026-W46"); got != "site 2026-W46" {
		t.Errorf("message %q", got)
	}
}

func TestSiteEnabled(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	allow := "dashboard:\n  demo_allow: [fred/DGS10]\n"
	c, err := Load(writeConfig(t, minimal+allow+"site:\n  enabled: true\n  repo: \"Example/macro-site\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Site.Remote != "git@github.com:Example/macro-site.git" || c.Site.URL != "https://example.github.io/macro-site/" {
		t.Errorf("site %+v", c.Site)
	}
	tests := []struct{ name, extra, wantErr string }{
		{"bad repo", allow + "site:\n  enabled: true\n  repo: \"macro-site\"\n", "site.repo"},
		{"no demo series", "site:\n  enabled: true\n  repo: \"o/site\"\n", "demo_allow is empty"},
		{"same repo", allow + "git:\n  enabled: true\n  repo: \"o/x\"\nsite:\n  enabled: true\n  repo: \"o/x\"\n", "repository of its own"},
		{"bad url", allow + "site:\n  enabled: true\n  repo: \"o/site\"\n  url: \"ftp://x\"\n", "site.url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, minimal+tt.extra))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
