package delivery

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"macro-collector/internal/export"
)

// fileInfo is what the message tells about one exported file.
type fileInfo struct {
	rel    string // usa/monetary/current_week.md
	chars  int    // -1: unreadable
	period string
	failed []string // sources_failed
	stale  []string // stale_series
}

// Text is the message about an export (§12.11, extended at the user's
// request 2026-11-06): the region's title, period, files by agent folder
// with sizes, failed sources and stale series from the front matter, and
// the git line.
func Text(e export.Exported, title, gitLine string) string {
	files := inspect(e.Files, e.Region)
	var b strings.Builder
	switch e.Kind {
	case export.KindWeekly:
		fmt.Fprintf(&b, "✅ Недельный экспорт %s — %s готов", e.ISOWeek, title)
	case export.KindEventFOMC:
		fmt.Fprintf(&b, "🏛 FOMC statement экспортирован (%s)", e.ISOWeek)
	case export.KindEventECB:
		fmt.Fprintf(&b, "🏛 ЕЦБ: решение экспортировано (%s)", e.ISOWeek)
	default:
		fmt.Fprintf(&b, "Экспорт %s %s — %s готов", e.Kind, e.ISOWeek, title)
	}
	if p := periodOf(files); p != "" {
		fmt.Fprintf(&b, "\nПериод: %s", p)
	}
	if len(files) > 0 {
		fmt.Fprintf(&b, "\n\nФайлы (%d):", len(files))
		for _, line := range byFolder(files) {
			b.WriteString("\n• " + line)
		}
	}
	var failed, stale []string
	for _, f := range files {
		failed = appendNew(failed, f.failed...)
		stale = appendNew(stale, f.stale...)
	}
	b.WriteString("\n")
	if len(failed) == 0 {
		b.WriteString("\nИсточники: все ответили")
	} else {
		fmt.Fprintf(&b, "\n⚠ Не ответили: %s", strings.Join(failed, ", "))
	}
	if len(stale) > 0 {
		shown := stale
		if len(shown) > 8 {
			shown = shown[:8]
		}
		more := ""
		if len(stale) > len(shown) {
			more = fmt.Sprintf(" и ещё %d", len(stale)-len(shown))
		}
		fmt.Fprintf(&b, "\n⚠ Устаревшие ряды (%d): %s%s", len(stale), strings.Join(shown, ", "), more)
	}
	if gitLine != "" {
		b.WriteString("\n" + gitLine)
	}
	return b.String()
}

// inspect reads the files in the agents' folders (archive copies and the
// JSON tree left out). Unreadable files are listed without details.
func inspect(paths []string, region string) []fileInfo {
	var out []fileInfo
	for _, p := range paths {
		slash := filepath.ToSlash(p)
		if strings.Contains(slash, "/archive/") || strings.HasSuffix(slash, ".json") {
			continue
		}
		fi := fileInfo{rel: slash, chars: -1}
		if i := strings.LastIndex(slash, "/"+region+"/"); i >= 0 {
			fi.rel = slash[i+1:]
		}
		if raw, err := os.ReadFile(p); err == nil {
			fi.chars = utf8.RuneCount(raw)
			fi.period, fi.failed, fi.stale = frontMatter(string(raw))
		}
		out = append(out, fi)
	}
	return out
}

// frontMatter reads period, sources_failed and stale_series from the YAML
// header written by the exporter ("key: value", lists as [a, b]).
func frontMatter(body string) (period string, failed, stale []string) {
	sc := bufio.NewScanner(strings.NewReader(body))
	for n := 0; sc.Scan(); n++ {
		line := sc.Text()
		if n == 0 && line != "---" {
			return
		}
		if n > 0 && line == "---" {
			return
		}
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch k {
		case "period":
			period = v
		case "sources_failed":
			failed = list(v)
		case "stale_series":
			stale = list(v)
		}
	}
	return
}

func list(v string) []string {
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(v), "["), "]"))
	if v == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.Trim(strings.TrimSpace(s), `"'`); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// periodOf is the period most files share (an event file may cover a
// shorter one).
func periodOf(files []fileInfo) string {
	count := map[string]int{}
	best := ""
	for _, f := range files {
		if f.period == "" {
			continue
		}
		count[f.period]++
		if count[f.period] > count[best] {
			best = f.period
		}
	}
	return best
}

// byFolder renders "monetary — current_week.md 14.3k, fomc_diff.md 3.1k".
func byFolder(files []fileInfo) []string {
	var folders []string
	items := map[string][]string{}
	for _, f := range files {
		dir, name := path.Split(f.rel)
		dir = strings.TrimSuffix(dir, "/")
		if i := strings.IndexByte(dir, '/'); i >= 0 {
			dir = dir[i+1:] // drop the region
		}
		if _, ok := items[dir]; !ok {
			folders = append(folders, dir)
		}
		item := name
		if f.chars >= 0 {
			item += " " + size(f.chars)
		}
		items[dir] = append(items[dir], item)
	}
	slices.Sort(folders)
	out := make([]string, 0, len(folders))
	for _, d := range folders {
		out = append(out, d+" — "+strings.Join(items[d], ", "))
	}
	return out
}

func size(chars int) string {
	if chars < 1000 {
		return fmt.Sprintf("%d зн.", chars)
	}
	return fmt.Sprintf("%.1fk зн.", float64(chars)/1000)
}

func appendNew(dst []string, vals ...string) []string {
	for _, v := range vals {
		if !slices.Contains(dst, v) {
			dst = append(dst, v)
		}
	}
	return dst
}
