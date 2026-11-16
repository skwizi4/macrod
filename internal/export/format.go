package export

import (
	"math"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/transform"
)

// Number formatting of §17.8: a space between thousands, a decimal point,
// "+" on positive changes and "−" (U+2212) on negatives, "—" for missing.
const (
	dash  = "—"
	minus = "−"
)

// num formats v with dec decimals and thousand separators; signed adds "+".
func num(v float64, dec int, signed bool) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return dash
	}
	r := math.Pow(10, float64(dec))
	v = math.Round(v*r) / r
	if v == 0 {
		v = 0 // no "−0"
	}
	s := strconv.FormatFloat(math.Abs(v), 'f', dec, 64)
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if frac != "" {
		out += "." + frac
	}
	switch {
	case v < 0:
		return minus + out
	case signed && v > 0:
		return "+" + out
	}
	return out
}

// val formats an optional value.
func val(v transform.Value, dec int, signed bool) string {
	if !v.OK {
		return dash
	}
	return num(v.V, dec, signed)
}

// pct1 is a percentage with one decimal: "2.9%".
func pct1(v transform.Value) string {
	if !v.OK {
		return dash
	}
	return num(v.V, 1, false) + "%"
}

// signedPct1 is a signed percentage change: "+2.1%".
func signedPct1(v transform.Value) string {
	if !v.OK {
		return dash
	}
	return num(v.V, 1, true) + "%"
}

// rate2 is a rate or yield with two decimals: "3.52%".
func rate2(v transform.Value) string {
	if !v.OK {
		return dash
	}
	return num(v.V, 2, false) + "%"
}

// bp is a signed change in whole basis points: "+6", "0".
func bp(v transform.Value) string { return val(v, 0, true) }

// pp is a signed change in percentage points with one decimal.
func pp(v transform.Value) string { return val(v, 1, true) }

// billions is an amount in billions of dollars: whole billions from 100 on,
// one decimal below ("4.5 млрд $"), three below 1 so that millions stay
// visible ("0.011 млрд $": SRF take-up must not read as 0.0 next to `gt`).
func billions(v transform.Value, signed bool) string {
	if !v.OK {
		return dash
	}
	return billionsNum(v, signed) + " млрд $"
}

// billionsNum is billions without the unit (columns that state it).
func billionsNum(v transform.Value, signed bool) string {
	if !v.OK {
		return dash
	}
	dec := 0
	switch a := math.Abs(v.V); {
	case a == 0:
		dec = 1
	case a < 1:
		dec = 3
	case a < 100:
		dec = 1
	}
	return num(v.V, dec, signed)
}

func some(v float64) transform.Value { return transform.Value{V: v, OK: true} }

// obsDate formats an observation date by frequency: "2026-Q2", "2026-08",
// "2026-10-23".
func obsDate(t time.Time, freq string) string {
	if t.IsZero() {
		return dash
	}
	switch freq {
	case "q":
		return t.Format("2006") + "-Q" + strconv.Itoa((int(t.Month())-1)/3+1)
	case "m":
		return t.Format("2006-01")
	}
	return t.Format(time.DateOnly)
}

var ruWeekdays = [...]string{"Вс", "Пн", "Вт", "Ср", "Чт", "Пт", "Сб"}

var ruMonthsShort = [...]string{"Янв", "Фев", "Мар", "Апр", "Май", "Июн", "Июл", "Авг", "Сен", "Окт", "Ноя", "Дек"}

// ruMonthsGen are lower-case abbreviations after «к»: «к окт. 2027».
var ruMonthsGen = [...]string{"янв.", "фев.", "мар.", "апр.", "мая", "июн.", "июл.", "авг.", "сен.", "окт.", "ноя.", "дек."}

// contractMonth is "Ноя 2026".
func contractMonth(t time.Time) string { return ruMonthsShort[t.Month()-1] + " " + t.Format("2006") }

// byMonth is "окт. 2027".
func byMonth(t time.Time) string { return ruMonthsGen[t.Month()-1] + " " + t.Format("2006") }

// flags joins flags with ", ".
func flags(fs []string) string { return strings.Join(fs, ", ") }

// list renders a YAML flow list: [a, b].
func list(items []string) string { return "[" + strings.Join(items, ", ") + "]" }
