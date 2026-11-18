package dashboard

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"testing"

	"macro-collector/internal/config"
)

// Every text the page passes to t() has a Russian translation with the same
// placeholders, and the dictionary has no entry nobody uses.
func TestFrontendDictionary(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "web", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	entry := regexp.MustCompile(`(?m)^\s*("(?:[^"\\]|\\.)*"): ("(?:[^"\\]|\\.)*"),$`)
	dict := map[string]string{}
	for _, m := range entry.FindAllStringSubmatch(read("i18n.js"), -1) {
		k, err1 := strconv.Unquote(m[1])
		v, err2 := strconv.Unquote(m[2])
		if err1 != nil || err2 != nil {
			t.Fatalf("bad entry %s", m[0])
		}
		dict[k] = v
	}
	if len(dict) < 100 {
		t.Fatalf("dictionary: %d entries", len(dict))
	}
	holes := regexp.MustCompile(`\{\d\}`)
	call := regexp.MustCompile(`\bt\(("(?:[^"\\]|\\.)*")`)
	used := map[string]bool{}
	for _, file := range []string{"app.js", "views.js"} {
		for _, m := range call.FindAllStringSubmatch(read(file), -1) {
			k, err := strconv.Unquote(m[1])
			if err != nil {
				t.Fatalf("%s: %s", file, m[1])
			}
			used[k] = true
			ru, ok := dict[k]
			if !ok {
				t.Errorf("%s: no Russian for %q", file, k)
				continue
			}
			a, b := holes.FindAllString(k, -1), holes.FindAllString(ru, -1)
			slices.Sort(a)
			slices.Sort(b)
			if !slices.Equal(a, b) {
				t.Errorf("%q / %q: placeholders differ", k, ru)
			}
		}
	}
	for k := range dict {
		if !used[k] {
			t.Errorf("dictionary entry %q is not used", k)
		}
	}
}

// title_en and label_en of the config reach the meta; a key row keeps its
// label, the key of the report rows.
func TestMetaCarriesEnglishTitles(t *testing.T) {
	d := fixture(t)
	r := &d.Config.Regions[0]
	at := slices.IndexFunc(r.Dashboard.Pages, func(p config.DashboardPage) bool { return len(p.Charts) > 0 })
	if at < 0 {
		t.Fatal("no page with charts in the fixture")
	}
	p := &r.Dashboard.Pages[at]
	p.TitleEn = "Liquidity EN"
	p.Charts[0].TitleEn = "Chart EN"
	p.Charts[0].Series[0].LabelEn = "Line EN"
	owner, _ := r.Owner("key_metrics")
	owner.Report.Rows[0].LabelEn = "Row EN"
	m := d.Meta().Regions[0]
	pm := m.Pages[at]
	if pm.TitleEn != "Liquidity EN" || pm.Charts[0].TitleEn != "Chart EN" || pm.Charts[0].Series[0].LabelEn != "Line EN" {
		t.Errorf("page %+v", pm)
	}
	if m.KeyRows[0].LabelEn != "Row EN" || m.KeyRows[0].Label == "Row EN" {
		t.Errorf("key row %+v", m.KeyRows[0])
	}
	for _, p := range pagesOf(&config.Region{}) {
		if p.TitleEn == "" {
			t.Errorf("default page %s has no English title", p.ID)
		}
	}
}
