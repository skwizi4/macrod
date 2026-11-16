package export

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// surprise is one "indicator — actual — expected" line of a post (FIXES_W44
// E2). The numbers are taken from the text as written and are not checked
// (DECISIONS D11, exception of 2026-11-02).
type surprise struct {
	label, actual, expected, diff string
}

var (
	// expectedRe is the expectation in brackets after the actual value:
	// "(очік. 3.2%🔼)", "(прогноз: $12.87 млрд)", "(est. 3.0%)".
	expectedRe = regexp.MustCompile(`(?i)\(\s*(?:очік(?:ування)?\.?|прогноз|ожидани[ея]|expected|exp\.|consensus|cons\.|est\.)\s*:?\s*([^()]+?)\s*\)`)
	// valueRe is a number with an optional currency, range and unit.
	valueRe   = regexp.MustCompile(`(?i)([-+−]?\$?\d+(?:[.,]\d+)?)(?:\s*[-–]\s*\$?\d+(?:[.,]\d+)?)?(\s*(?:%|п\.\s?п\.|б\.\s?п\.|bp|тис\.?|тыс\.?|млн|млрд|трлн|bn|mn|[kmb]\b))?`)
	rangeRe   = regexp.MustCompile(`\d\s*[-–]\s*\$?\d`)
	cashtagRe = regexp.MustCompile(`\$[A-Z]{1,5}\b`)
	// labelTail are words left between the label and the value: "CapEx до $135".
	labelTail = regexp.MustCompile(`(?i)\s+(?:до|to|at|на|=)$`)
)

// extractSurprises finds lines with an actual value followed by the
// expectation in brackets. The first cashtag of the post prefixes labels
// that do not name a company ("Revenue" in an earnings post).
func extractSurprises(text string) []surprise {
	tag := cashtagRe.FindString(text)
	var out []surprise
	for _, line := range strings.Split(text, "\n") {
		m := expectedRe.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		exp := firstValue(line[m[2]:m[3]])
		if exp == "" {
			continue
		}
		before := strings.TrimRightFunc(line[:m[0]], isDecoration)
		act, start := lastValue(before)
		if act == "" {
			continue
		}
		label := strings.TrimFunc(before[:start], func(r rune) bool { return isDecoration(r) || r == ':' || r == '-' || r == '—' })
		label = strings.TrimSpace(labelTail.ReplaceAllString(label, ""))
		label = strings.TrimRight(label, ":")
		if label == "" {
			continue
		}
		if tag != "" && !cashtagRe.MatchString(label) && !hasUpperWord(label) {
			label = tag + " " + label
		}
		out = append(out, surprise{label: label, actual: act, expected: exp, diff: surpriseDiff(act, exp)})
	}
	return out
}

var (
	// forecastRe is an expectation in a post before the release, without the
	// actual value: "Зростання Core: прогнозують +0,34% m/m (3,1% y/y)".
	forecastRe = regexp.MustCompile(`(?i)(?:^|[\s:—-])(прогнозують|прогнозируют|очікують|ожидают|expected at|consensus is|consensus:)\s*:?\s*`)
	// periodValueRe is a value with the period after it: "+0,34% m/m".
	periodValueRe = regexp.MustCompile(`(?i)([-+−]?\d+(?:[.,]\d+)?\s*%)\s*(m/m|y/y|mom|yoy|м/м|р/р|г/г|q/q)?`)
)

// extractForecasts finds expectations of posts written before the release
// (review of W44, 2026-11-03): "<indicator>: прогнозують <value> [m/m]".
// Each value is a surprise without an actual; its label is the line's
// indicator, the period and, for context, the post's first line. Lines
// with an "actual (expected)" pair are extractSurprises'.
func extractForecasts(text string) []surprise {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	head := strings.TrimFunc(lines[0], isDecoration)
	var out []surprise
	for i, line := range lines {
		if expectedRe.MatchString(line) {
			continue
		}
		m := forecastRe.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		label := strings.TrimFunc(line[:m[2]], func(r rune) bool { return isDecoration(r) || r == ':' || r == '-' || r == '—' })
		if label == "" {
			continue
		}
		if i > 0 && head != "" {
			label = head + " — " + label
		}
		for _, v := range periodValueRe.FindAllStringSubmatch(line[m[1]:], -1) {
			l := label
			if v[2] != "" {
				l += " " + v[2]
			}
			out = append(out, surprise{label: l, expected: strings.ReplaceAll(v[1], " ", "")})
		}
	}
	return out
}

// hasUpperWord: the label names an indicator by an acronym (GDP, IJC, CPI)
// and needs no company prefix.
func hasUpperWord(s string) bool {
	for _, w := range strings.Fields(s) {
		if len(w) >= 2 && strings.ToUpper(w) == w && strings.IndexFunc(w, unicode.IsLetter) >= 0 && w != "EPS" {
			return true
		}
	}
	return false
}

// isDecoration is emoji, pictographs, bullets and spaces around values.
func isDecoration(r rune) bool {
	return unicode.IsSpace(r) || unicode.In(r, unicode.So, unicode.Sk, unicode.Cf, unicode.Me, unicode.Variation_Selector) ||
		strings.ContainsRune("▫▪•➣►✓", r)
}

// valueAt reports whether the value match at [s, e) of text stands alone:
// not part of a word ("Q3", "1st").
func valueAt(text string, s, e int) bool {
	if s > 0 {
		r := rune(text[s-1])
		if r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == ',') {
			return false
		}
	}
	if e < len(text) {
		r := []rune(text[e:])[0]
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func firstValue(text string) string {
	for _, m := range valueRe.FindAllStringIndex(text, -1) {
		if valueAt(text, m[0], m[1]) {
			return strings.TrimSpace(text[m[0]:m[1]])
		}
	}
	return ""
}

// lastValue is the value that ends text, and where it starts.
func lastValue(text string) (string, int) {
	ms := valueRe.FindAllStringIndex(text, -1)
	if len(ms) == 0 {
		return "", 0
	}
	m := ms[len(ms)-1]
	if strings.TrimSpace(text[m[1]:]) != "" || !valueAt(text, m[0], m[1]) {
		return "", 0
	}
	return strings.TrimSpace(text[m[0]:m[1]]), m[0]
}

// surpriseDiff is actual − expected in the units of both, or "—" for
// ranges and different units. Percent values differ in п.п.
func surpriseDiff(act, exp string) string {
	a, ua, ok1 := parseValue(act)
	e, ue, ok2 := parseValue(exp)
	if !ok1 || !ok2 || ua != ue {
		return "—"
	}
	dec := max(decimals(act), decimals(exp))
	d := a - e
	if math.Abs(d) < math.Pow(10, -float64(dec))/2 {
		return "0"
	}
	sign := "+"
	if d < 0 {
		sign = "−"
	}
	num := strconv.FormatFloat(math.Abs(d), 'f', dec, 64)
	// The unit as the actual value spells it: "тис.", "K", "млрд".
	unit := strings.TrimSpace(valueRe.FindStringSubmatch(act)[2])
	cur := ""
	if strings.HasPrefix(ua, "$") {
		cur = "$"
	}
	switch {
	case unit == "%":
		return sign + num + " п.п."
	case unit == "" || len(unit) == 1:
		return sign + cur + num + unit
	}
	return sign + cur + num + " " + unit
}

// parseValue splits "$14.58 млрд" into 14.58 and "$млрд"; ranges fail.
func parseValue(v string) (float64, string, bool) {
	m := valueRe.FindStringSubmatch(v)
	if m == nil || m[0] != v || rangeRe.MatchString(v) {
		return 0, "", false
	}
	num := m[1]
	cur := ""
	if strings.Contains(num, "$") {
		cur = "$"
		num = strings.Replace(num, "$", "", 1)
	}
	num = strings.NewReplacer("−", "-", ",", ".").Replace(num)
	x, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, "", false
	}
	unit := strings.ToLower(strings.TrimSpace(m[2]))
	unit = strings.TrimSuffix(strings.ReplaceAll(unit, " ", ""), ".")
	if unit == "тыс" {
		unit = "тис"
	}
	if unit == "тис" {
		unit = "тис."
	}
	return x, cur + unit, true
}

func decimals(v string) int {
	m := valueRe.FindStringSubmatch(v)
	if m == nil {
		return 0
	}
	if i := strings.IndexAny(m[1], ".,"); i >= 0 {
		return len(m[1]) - i - 1
	}
	return 0
}

func mdCell(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
