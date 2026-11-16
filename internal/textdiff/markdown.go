package textdiff

import (
	"fmt"
	"strings"
)

// Markdown renders the diff as "##" sections: a summary line, changed,
// added and removed sentences, then the whole new text with the changes
// marked in place (~~removed~~ **added**).
func (d Diff) Markdown() string {
	var b strings.Builder
	changed, added, removed := d.Stats()
	if d.Identical() {
		b.WriteString("**Итог:** текст не изменился.\n")
	} else {
		fmt.Fprintf(&b, "**Итог:** изменено %d · добавлено %d · удалено %d (по предложениям).\n", changed, added, removed)
	}

	section := func(title string, kind Kind, line func(Entry) string) {
		var lines []string
		for _, e := range d.Entries {
			if e.Kind == kind {
				lines = append(lines, line(e))
			}
		}
		fmt.Fprintf(&b, "\n## %s\n\n", title)
		if len(lines) == 0 {
			b.WriteString("—\n")
			return
		}
		for i, l := range lines {
			if kind == Changed {
				fmt.Fprintf(&b, "%d. %s\n", i+1, l)
			} else {
				fmt.Fprintf(&b, "- %s\n", l)
			}
		}
	}
	section("Изменено", Changed, func(e Entry) string { return Words(e.Old, e.New) })
	section("Добавлено", Added, func(e Entry) string { return escape(normalize(e.New)) })
	section("Удалено", Removed, func(e Entry) string { return escape(normalize(e.Old)) })

	b.WriteString("\n## Полный текст с разметкой\n\n")
	var paras []string
	var cur []string
	para := -1
	for _, e := range d.Entries {
		if e.Para != para && len(cur) > 0 {
			paras = append(paras, strings.Join(cur, " "))
			cur = nil
		}
		para = e.Para
		switch e.Kind {
		case Equal:
			cur = append(cur, escape(normalize(e.New)))
		case Changed:
			cur = append(cur, Words(e.Old, e.New))
		case Added:
			cur = append(cur, "**"+escape(normalize(e.New))+"**")
		case Removed:
			cur = append(cur, "~~"+escape(normalize(e.Old))+"~~")
		}
	}
	if len(cur) > 0 {
		paras = append(paras, strings.Join(cur, " "))
	}
	b.WriteString(strings.Join(paras, "\n\n"))
	b.WriteString("\n")
	return b.String()
}
