package transform

import (
	"fmt"
	"strconv"
	"time"
)

// Window is a calendar look-back such as "52w" or "5y".
type Window struct {
	Days, Months, Years int
}

// ParseWindow parses <n><unit> with unit d, w, m or y.
func ParseWindow(s string) (Window, error) {
	if len(s) < 2 {
		return Window{}, fmt.Errorf("window %q: want <n>d|w|m|y", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return Window{}, fmt.Errorf("window %q: want a positive number before the unit", s)
	}
	switch s[len(s)-1] {
	case 'd':
		return Window{Days: n}, nil
	case 'w':
		return Window{Days: 7 * n}, nil
	case 'm':
		return Window{Months: n}, nil
	case 'y':
		return Window{Years: n}, nil
	}
	return Window{}, fmt.Errorf("window %q: unit must be d, w, m or y", s)
}

// IsZero reports whether the window is unset.
func (w Window) IsZero() bool { return w == Window{} }

// Before returns t moved back by the window.
func (w Window) Before(t time.Time) time.Time { return t.AddDate(-w.Years, -w.Months, -w.Days) }
