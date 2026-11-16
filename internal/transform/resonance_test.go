package transform

import (
	"math"
	"testing"
)

func TestResonance(t *testing.T) {
	if m := Median([]float64{3, 1, 2}); !m.OK || m.V != 2 {
		t.Errorf("median odd = %+v", m)
	}
	if m := Median([]float64{4, 1, 3, 2}); !m.OK || m.V != 2.5 {
		t.Errorf("median even = %+v", m)
	}
	if Median(nil).OK {
		t.Error("median of none is OK")
	}
	// W44: a decipher post at 1.2K against its usual 900 is louder than an
	// ordinary politics post at 15K against his 15.5K.
	decipher := []int64{850, 900, 920, 880, 1200, 0}
	dudakov := []int64{14000, 15000, 15500, 39500, 16000}
	d, m := Resonance(1200, decipher), Resonance(15000, dudakov)
	if !d.OK || math.Abs(d.V-1200.0/900) > 1e-9 || !m.OK || math.Abs(m.V-15000.0/15500) > 1e-9 || d.V <= m.V {
		t.Errorf("decipher %+v, dudakov %+v", d, m)
	}
	if Resonance(1200, []int64{900, 1000, 0, 0}).OK || Resonance(0, decipher).OK {
		t.Error("too few posts or no views must not make a resonance")
	}
}
