// Package irxlsx reads the few quarterly numbers EU Micro takes from an
// issuer's own Excel (phase 11, step 18; DECISIONS D15): revenue, the
// company's operating profit, net income to shareholders and basic EPS of
// the file's quarter and the same quarter a year earlier. Nothing else is
// read. A table is found by its anchor text in any sheet, a row by its
// label, a column by its period header — sheet names, cell positions,
// column order and extra columns do not matter. Anything not found exactly
// once is an error: no value is guessed.
package irxlsx

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"macro-collector/internal/config"
	"macro-collector/internal/xlsx"
)

// Form is company_facts.form of the numbers.
const Form = "IR_XLSX"

// Pair is a field's value in the file's quarter and a year earlier, in
// currency units (EPS: per share).
type Pair struct{ Cur, Ago float64 }

// Parse reads the configured fields of the file for fiscal quarter q of
// fiscal year fy.
func Parse(data []byte, cfg config.IssuerQuarterly, fy, q int) (map[string]Pair, error) {
	wb, err := xlsx.Open(data)
	if err != nil {
		return nil, err
	}
	sheets := map[string][][]string{}
	var order []string
	for _, s := range wb.Sheets() {
		rows, err := wb.Rows(s)
		if err != nil {
			return nil, fmt.Errorf("sheet %s: %w", s, err)
		}
		sheets[s] = rows
		order = append(order, s)
	}
	out := map[string]Pair{}
	for i, t := range cfg.Tables {
		tb, err := findTable(sheets, order, t, fy, q)
		if err != nil {
			return nil, fmt.Errorf("table %d (%s): %w", i, strings.Join(t.Anchor, " / "), err)
		}
		for field, r := range t.Rows {
			row, err := tb.row(r)
			if err != nil {
				return nil, fmt.Errorf("table %d %s: %w", i, field, err)
			}
			scale := t.Scale
			if field == config.QuarterEPS {
				scale = 1
			}
			var p Pair
			for k, col := range []int{tb.cur, tb.ago} {
				v, err := number(cell(row, col))
				if err != nil {
					return nil, fmt.Errorf("table %d %s %s: %w", i, field, []string{"quarter", "year ago"}[k], err)
				}
				if k == 0 {
					p.Cur = v * scale
				} else {
					p.Ago = v * scale
				}
			}
			out[field] = p
		}
	}
	return out, nil
}

// table is a found table: its data rows and the two period columns.
type table struct {
	sheet    string
	rows     [][]string
	cur, ago int
}

func findTable(sheets map[string][][]string, order []string, t config.QuarterlyTable, fy, q int) (*table, error) {
	var found []*table
	var reasons []string
	for _, name := range order {
		rows := sheets[name]
		for r := range rows {
			end, ok := anchorAt(rows, r, t.Anchor)
			if !ok {
				continue
			}
			tb, err := tableAfter(name, rows, end, t.Group, fy, q)
			if err != nil {
				reasons = append(reasons, name+": "+err.Error())
				continue
			}
			found = append(found, tb)
		}
	}
	switch len(found) {
	case 0:
		if len(reasons) == 0 {
			return nil, fmt.Errorf("anchor not found")
		}
		return nil, fmt.Errorf("no period columns Q%d %d/%d: %s", q, fy, fy-1, strings.Join(reasons, "; "))
	case 1:
		return found[0], nil
	}
	var names []string
	for _, f := range found {
		names = append(names, f.sheet)
	}
	return nil, fmt.Errorf("found %d times (%s)", len(found), strings.Join(names, ", "))
}

// anchorAt reports whether the anchor texts start at row r (the first text
// of consecutive non-empty rows); end is the row after the anchor.
func anchorAt(rows [][]string, r int, anchor []string) (end int, ok bool) {
	i := r
	for k, a := range anchor {
		if k > 0 {
			for i < len(rows) && firstText(rows[i]) == "" {
				i++
			}
		}
		if i >= len(rows) || !same(firstText(rows[i]), a) {
			return 0, false
		}
		i++
	}
	return i, true
}

var (
	quarterRe = regexp.MustCompile(`(?i)\b(?:Q([1-4])|([1-4])Q)\b`) // "Q3"; Allianz: "3Q"
	yearRe    = regexp.MustCompile(`\b(20\d\d)\b`)
	// Allianz writes a quarter column "3Q 26": the two-digit year right after "nQ".
	shortYearRe = regexp.MustCompile(`(?i)\b[1-4]Q\s+(\d\d)\b`)
)

// tableAfter reads the header rows after the anchor (up to the first data
// row) and finds the columns of Q q in fiscal years fy and fy−1, under the
// group if set; the data rows run to the next header row.
func tableAfter(sheet string, rows [][]string, start int, group string, fy, q int) (*table, error) {
	data := -1
	for i := start; i < len(rows); i++ {
		if isDataRow(rows[i]) {
			data = i
			break
		}
	}
	if data < 0 {
		return nil, fmt.Errorf("no data rows")
	}
	width := 0
	for _, r := range rows[start:data] {
		width = max(width, len(r))
	}
	labels := make([][]string, width) // header texts per column, carried right
	for _, r := range rows[start:data] {
		carry := ""
		for c := range width {
			if v := strings.TrimSpace(cell(r, c)); v != "" {
				carry = v
			}
			if c > 0 && carry != "" {
				labels[c] = append(labels[c], carry)
			}
		}
	}
	var groups []string // the group of each column, from the row holding the group label
	if group != "" {
		for _, r := range rows[start:data] {
			gs := make([]string, width)
			carry, hit := "", false
			for c := range width {
				if v := strings.TrimSpace(cell(r, c)); v != "" {
					carry = firstLine(v)
					hit = hit || same(carry, group)
				}
				gs[c] = carry
			}
			if hit {
				groups = gs
				break
			}
		}
		if groups == nil {
			return nil, fmt.Errorf("group %q not found", group)
		}
	}
	end := len(rows)
	for i := data + 1; i < len(rows); i++ {
		if isHeaderRow(rows[i]) {
			end = i
			break
		}
	}
	cur, ago := -1, -1
	for c := 1; c < width; c++ {
		if groups != nil && !same(groups[c], group) {
			continue
		}
		if !columnHasNumber(rows[data:end], c) {
			continue // a spacer column the header carried into (Allianz)
		}
		pq, py, ok := period(strings.Join(labels[c], " "))
		if !ok || pq != q {
			continue
		}
		switch py {
		case fy:
			if cur >= 0 {
				return nil, fmt.Errorf("two columns of Q%d %d", q, fy)
			}
			cur = c
		case fy - 1:
			if ago >= 0 {
				return nil, fmt.Errorf("two columns of Q%d %d", q, fy-1)
			}
			ago = c
		}
	}
	if cur < 0 || ago < 0 {
		return nil, fmt.Errorf("columns of Q%d %d and %d not found", q, fy, fy-1)
	}
	return &table{sheet: sheet, rows: rows[data:end], cur: cur, ago: ago}, nil
}

// period parses a column header: one quarter and one year ("Q3 FY 2026",
// "Q3\n2026\nmillions of €", Allianz "3Q 26"); a range (Q1 - Q3), a change
// column ("∆ 3Q 26 / 3Q 25") or a year column is not a quarter.
func period(s string) (q, year int, ok bool) {
	qs := quarterRe.FindAllStringSubmatch(s, -1)
	ys := yearRe.FindAllStringSubmatch(s, -1)
	if len(qs) != 1 {
		return 0, 0, false
	}
	q, _ = strconv.Atoi(qs[0][1] + qs[0][2])
	switch short := shortYearRe.FindAllStringSubmatch(s, -1); {
	case len(ys) == 1 && len(short) == 0:
		year, _ = strconv.Atoi(ys[0][1])
	case len(ys) == 0 && len(short) == 1:
		year, _ = strconv.Atoi(short[0][1])
		year += 2000
	default:
		return 0, 0, false
	}
	return q, year, true
}

// row finds the data row of r: its label exactly once (after the section
// row when set).
func (t *table) row(r config.QuarterlyRow) ([]string, error) {
	from := 0
	if r.Section != "" {
		from = -1
		for i, row := range t.rows {
			if same(cell(row, 0), r.Section) {
				if from >= 0 {
					return nil, fmt.Errorf("section %q twice", r.Section)
				}
				from = i + 1
			}
		}
		if from < 0 {
			return nil, fmt.Errorf("section %q not found", r.Section)
		}
	}
	var hits [][]string
	for i := from; i < len(t.rows); i++ {
		row := t.rows[i]
		label := cell(row, 0)
		if r.Section != "" && label != "" && !hasNumber(row) && i > from {
			break // the next section
		}
		for _, l := range r.Labels {
			if same(label, l) {
				hits = append(hits, row)
				break
			}
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("row %q not found", strings.Join(r.Labels, " | "))
	case 1:
		return hits[0], nil
	}
	return nil, fmt.Errorf("row %q found %d times", strings.Join(r.Labels, " | "), len(hits))
}

func columnHasNumber(rows [][]string, c int) bool {
	for _, r := range rows {
		if _, err := number(cell(r, c)); err == nil {
			return true
		}
	}
	return false
}

// isDataRow: a label (not a units note in brackets) and a number, and no
// quarter in the other cells (Allianz's header "EUR mn | 1Q 25 | … | 2025"
// has a year that reads as a number).
func isDataRow(r []string) bool {
	label := strings.TrimSpace(cell(r, 0))
	if label == "" || strings.HasPrefix(label, "(") || !hasNumber(r) {
		return false
	}
	for _, c := range r[min(1, len(r)):] {
		if quarterRe.MatchString(c) {
			return false
		}
	}
	return true
}

// isHeaderRow: no label, a quarter in the other cells (the next table).
func isHeaderRow(r []string) bool {
	if strings.TrimSpace(cell(r, 0)) != "" {
		return false
	}
	for _, c := range r[min(1, len(r)):] {
		if quarterRe.MatchString(c) {
			return true
		}
	}
	return false
}

func hasNumber(r []string) bool {
	for _, c := range r[min(1, len(r)):] {
		if _, err := number(c); err == nil {
			return true
		}
	}
	return false
}

// number parses a cell value; text and empty cells are errors.
func number(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty cell")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("not a number: %q", s)
	}
	return v, nil
}

func cell(r []string, c int) string {
	if c < 0 || c >= len(r) {
		return ""
	}
	return r[c]
}

func firstText(r []string) string {
	for _, c := range r {
		if c = strings.TrimSpace(c); c != "" {
			return c
		}
	}
	return ""
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(l)
}

var spaces = regexp.MustCompile(`\s+`)

// same compares labels ignoring case and runs of whitespace.
func same(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(spaces.ReplaceAllString(strings.TrimSpace(s), " ")) }
	return norm(a) == norm(b)
}

// File is a quarterly workbook linked from the issuer's page.
type File struct {
	URL  string
	Name string // file name, the accession of its facts
	FY   int
	Q    int
}

var xlsxURL = regexp.MustCompile(`https?://[^\s"'<>\\]+?\.xlsx(?:\?[^\s"'<>\\]*)?`)

// Links finds the workbooks of the page: absolute .xlsx URLs anywhere in
// the unescaped page (Siemens lists them inside an escaped HTML block)
// whose decoded file name matches one of the patterns; named groups year
// (2 or 4 digits) and q give the fiscal quarter. Newest first, one per
// quarter, at most max.
func Links(page []byte, patterns []*regexp.Regexp, max int) []File {
	text := html.UnescapeString(strings.ReplaceAll(string(page), `\/`, `/`))
	seen := map[[2]int]bool{}
	var out []File
	for _, u := range xlsxURL.FindAllString(text, -1) {
		path := u
		if i := strings.IndexByte(path, '?'); i >= 0 {
			path = path[:i]
		}
		name, err := url.PathUnescape(path[strings.LastIndexByte(path, '/')+1:])
		if err != nil {
			continue
		}
		f, ok := FileOf(name, patterns)
		if !ok || seen[[2]int{f.FY, f.Q}] {
			continue
		}
		seen[[2]int{f.FY, f.Q}] = true
		f.URL = u
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b File) int { return (b.FY*10 + b.Q) - (a.FY*10 + a.Q) })
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// FileOf reads the fiscal quarter of a workbook from its file name: the
// first pattern that matches, named groups year (2 or 4 digits) and q.
func FileOf(name string, patterns []*regexp.Regexp) (File, bool) {
	for _, re := range patterns {
		m := re.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		year, _ := strconv.Atoi(m[re.SubexpIndex("year")])
		q, _ := strconv.Atoi(m[re.SubexpIndex("q")])
		if year < 100 {
			year += 2000
		}
		if q < 1 || q > 4 {
			return File{}, false
		}
		return File{Name: name, FY: year, Q: q}, true
	}
	return File{}, false
}

var userQuarterRe = regexp.MustCompile(`(?i)\b(?:Q([1-4])|([1-4])Q)\s*(?:FY\s*)?(\d{4}|\d{2})\b`)

// ParseQuarter reads a fiscal quarter written by the user: "Q3 2026",
// "3Q 2026", "Q3 FY2026", "Q3 26".
func ParseQuarter(s string) (fy, q int, ok bool) {
	m := userQuarterRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	q, _ = strconv.Atoi(m[1] + m[2])
	fy, _ = strconv.Atoi(m[3])
	if fy < 100 {
		fy += 2000
	}
	return fy, q, true
}
