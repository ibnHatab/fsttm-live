package audio

import (
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
)

// pitched runs 1 s of samples through Pitch: the share of 20 ms frames with a voice's pitch
func pitched(x []float64) float64 {
	p, n, frames := NewPitch(), 0, 0
	for at := 0; at+FrameLen <= len(x); at += FrameLen {
		b := make([]byte, 2*FrameLen)
		for i := 0; i < FrameLen; i++ {
			binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(max(-32768, min(32767, x[at+i])))))
		}
		frames++
		if p.Frame(b) {
			n++
		}
	}
	return float64(n) / float64(frames)
}

func TestPitch(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	voice := func(f0 float64) []float64 { // a glottal-like pulse train through a vowel's formants
		x := make([]float64, Rate)
		for i := range x {
			ph := math.Mod(float64(i)*f0/Rate, 1)
			for h := 1.0; h <= 15; h++ {
				x[i] += 3000 / h * math.Sin(2*math.Pi*h*ph) * (1 + 0.8*math.Exp(-math.Pow(h*f0-700, 2)/2e5))
			}
		}
		return x
	}
	clicks := func(n int) []float64 {
		x := make([]float64, Rate)
		for k := 0; k < n; k++ {
			at := r.Intn(Rate - 400)
			for i := 0; i < 300; i++ {
				x[at+i] += r.NormFloat64() * 9000 * math.Pow(0.985, float64(i))
			}
		}
		return x
	}
	ring := func() []float64 { // a key's 2 kHz ring, decaying, every 150 ms
		x := make([]float64, Rate)
		for at := 0; at+1600 < Rate; at += 2400 {
			for i := 0; i < 1600; i++ {
				x[at+i] += 9000 * math.Sin(2*math.Pi*2000*float64(i)/Rate) * math.Pow(0.997, float64(i))
			}
		}
		return x
	}
	noise := make([]float64, Rate)
	for i := range noise {
		noise[i] = r.NormFloat64() * 3000
	}
	for name, c := range map[string]struct {
		x      []float64
		lo, hi float64
	}{
		"voice 120 Hz": {voice(120), 0.8, 1}, "voice 220 Hz": {voice(220), 0.8, 1},
		"clicks": {clicks(12), 0, 0.1}, "key ring": {ring(), 0, 0.1}, "noise": {noise, 0, 0.1},
	} {
		if got := pitched(c.x); got < c.lo || got > c.hi {
			t.Errorf("%s: pitched %.2f, want %.2f–%.2f", name, got, c.lo, c.hi)
		}
	}
}
