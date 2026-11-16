package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"macro-collector/internal/config"
)

// The presets ship with Telegram off and no channels (phase 15: no personal
// reading list in the public repo). The export tests seed posts of these
// three fixture channels, so presetConfig switches Telegram on with them.
const testChannels = `telegram:
  enabled: true
  mode: web
  base_url: "https://t.me"
  channels:
    - {handle: marketnews_ua, region: %R, type: news, default_tag: "", profile: "укр. рыночная лента: быстрые новости, цифры релизов, отчёты, ссылки на подкасты"}
    - {handle: macro_views, region: %R, type: opinion, default_tag: "", opinions_full: 10, profile: "рус. макро-аналитика, авторские выводы о ставках, ликвидности и AI-капексе"}
    - {handle: us_politics_ru, region: %R, type: opinion, default_tag: usa_politics, opinions_full: 5, profile: "российский политический комментарий, оценочный, факты не проверены"}
`

// presetConfig loads presets/<name>.yaml with the fixture Telegram channels
// (region region) and the data dir dataDir ("" — MACRO_DATA_DIR rules).
func presetConfig(t *testing.T, name, region, dataDir string) *config.Config {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "presets", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	from, to := strings.Index(s, "\ntelegram:\n"), strings.Index(s, "\nrss_feeds:")
	if from < 0 || to < from {
		t.Fatal("telegram block not found in preset " + name)
	}
	s = s[:from] + "\n" + strings.ReplaceAll(testChannels, "%R", region) + s[to:]
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg *config.Config
	if dataDir != "" {
		cfg, err = config.LoadAtWithDataDir(path, repoRoot, dataDir)
	} else {
		cfg, err = config.LoadAt(path, repoRoot)
	}
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// presetConfigErr is presetConfig in the (cfg, err) shape of config.LoadAt.
func presetConfigErr(t *testing.T, name, region, dataDir string) (*config.Config, error) {
	t.Helper()
	return presetConfig(t, name, region, dataDir), nil
}
