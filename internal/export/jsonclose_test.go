package export

import (
	"encoding/json"
	"math"
	"strings"
)

// sameJSON reports whether two JSON documents are equal, with numbers equal
// up to a relative 1e-12. Go fuses multiply-add on arm64 but not on amd64, so
// a computed float may differ in its last digits between machines; the
// Markdown, which rounds, is still compared byte for byte.
func sameJSON(got, want string) bool {
	if got == want {
		return true
	}
	a, errA := decodeJSON(got)
	b, errB := decodeJSON(want)
	return errA == nil && errB == nil && closeJSON(a, b)
}

func decodeJSON(s string) (any, error) {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var v any
	err := d.Decode(&v)
	return v, err
}

func closeJSON(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !closeJSON(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !closeJSON(x[i], y[i]) {
				return false
			}
		}
		return true
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		if x == y {
			return true
		}
		f, err1 := x.Float64()
		g, err2 := y.Float64()
		if err1 != nil || err2 != nil {
			return false
		}
		return math.Abs(f-g) <= 1e-12*math.Max(math.Abs(f), math.Abs(g))
	default:
		return a == b
	}
}
