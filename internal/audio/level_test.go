package audio

import (
	"encoding/binary"
	"math"
	"testing"
)

func tone(dbfs float64) []byte {
	a := math.Pow(10, dbfs/20) * 32768 * math.Sqrt2
	b := make([]byte, 2*480)
	for i := 0; i < 480; i++ {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(a*math.Sin(2*math.Pi*440*float64(i)/24000))))
	}
	return b
}

// The calibrated level at the bubble's radius, the person's reach within it.
func TestLevelBubbleReach(t *testing.T) {
	s := &Speaker{Level: -36}
	var g float64
	for i := 0; i < 400; i++ { // the stream's level learnt: -20 dBFS
		g = s.level(tone(-20))
	}
	db := func(g float64) float64 { return math.Round(20 * math.Log10(g)) }
	if db(g) != -16 {
		t.Fatalf("1 m: %v dB, want -16", db(g))
	}
	s.SetBubble(2)
	if g = s.level(tone(-20)); db(g) != -10 {
		t.Fatalf("2 m: %v dB, want -10", db(g))
	}
	s.SetReach(1)
	if g = s.level(tone(-20)); db(g) != -16 {
		t.Fatalf("reach 1 m in a 2 m bubble: %v dB, want -16", db(g))
	}
	s.SetReach(6) // farther than the bubble: its edge
	if g = s.level(tone(-20)); db(g) != -10 {
		t.Fatalf("reach 6 m: %v dB, want -10", db(g))
	}
	s.SetBubble(99)
	if s.bubble != 4 {
		t.Fatalf("bubble %v, capped at 4", s.bubble)
	}
	if g = s.level(tone(-60)); db(g) != -4 { // silence is not learnt from: still -20; at 4 m, 12 dB over -16
		t.Fatalf("quiet chunk moved the estimate: %v", db(g))
	}
}

// The distance calibration's slope: 4 dB a doubling, the bubble's top at +12 dB is 8 m.
func TestSlope(t *testing.T) {
	s := &Speaker{Level: -36}
	s.SetSlope(4)
	if m := s.MaxBubble(); math.Abs(m-8) > 1e-9 {
		t.Fatalf("max %v, want 8", m)
	}
	s.SetBubble(4)
	if g := s.Gain(4); g != 8 {
		t.Fatalf("4 m: %v dB, want 8", g)
	}
}
