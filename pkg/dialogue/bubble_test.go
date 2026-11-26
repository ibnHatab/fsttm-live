package dialogue

import (
	"math"
	"testing"
)

// The distance calibration keeps what the person saved: one point (even under 1.2 m) by the offset,
// several spread over distances by the slope and the offset.
func TestFitBubble(t *testing.T) {
	gain := func(s, o, d float64) float64 { return o + s*math.Log2(d) }
	// comfortable at 1 m with +6 dB set: the slope stays, the offset carries it
	s, o := FitBubble([][2]float64{{1, 6}}, 6)
	if s != 6 || math.Abs(gain(s, o, 1)-6) > 0.05 {
		t.Fatalf("one point at 1 m: slope %v offset %v", s, o)
	}
	// at 0.5 m with +3 dB
	s, o = FitBubble([][2]float64{{0.5, 3}}, 6)
	if math.Abs(gain(s, o, 0.5)-3) > 0.05 {
		t.Fatalf("one point at 0.5 m: slope %v offset %v → %v", s, o, gain(s, o, 0.5))
	}
	// two points a doubling and more apart: both kept
	s, o = FitBubble([][2]float64{{1, 2}, {4, 10}}, 6)
	if math.Abs(gain(s, o, 1)-2) > 0.1 || math.Abs(gain(s, o, 4)-10) > 0.1 {
		t.Fatalf("two points: slope %v offset %v", s, o)
	}
}
