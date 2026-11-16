package transform

import "slices"

// MinResonancePosts is the fewest posts with views a channel needs for its
// median to make a resonance.
const MinResonancePosts = 5

// Median of xs (any order); !OK for none.
func Median(xs []float64) Value {
	if len(xs) == 0 {
		return Value{}
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return some(s[n/2])
	}
	return some((s[n/2-1] + s[n/2]) / 2)
}

// Resonance compares a post's views with its channel's usual: views over
// the median views of the channel's posts (channel, at least
// MinResonancePosts of them with views). Channels of different size
// compare: ×1.3 is a post 30% above its channel's median.
func Resonance(views int64, channel []int64) Value {
	var xs []float64
	for _, v := range channel {
		if v > 0 {
			xs = append(xs, float64(v))
		}
	}
	m := Median(xs)
	if views <= 0 || len(xs) < MinResonancePosts || !m.OK || m.V <= 0 {
		return Value{}
	}
	return some(float64(views) / m.V)
}
