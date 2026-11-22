package audio

import "math"

// Pitch tells voiced speech from other sounds the VAD lets through (clicks, knocks, keys): a
// voice has a pitch, a periodic low band (80–400 Hz fundamental), a click has neither. On a
// 40 ms window: the share of its energy below ~700 Hz, then the best normalized autocorrelation
// of that band at the lags of a voice's pitch.
type Pitch struct {
	lp   biquad
	win  []float64 // the low band, the last 40 ms
	full []float64 // the signal, the same 40 ms
}

const (
	pitchWin            = Rate / 25 // 40 ms
	minLag, maxLag      = Rate / 400, Rate / 80
	pitchCorr, lowShare = 0.6, 0.25
	pitchMinRMS         = 150.0 // of 32768: quieter is no one speaking to her
)

func NewPitch() *Pitch { return &Pitch{lp: lowpass(700, Rate)} }

// Frame takes 20 ms of 16-bit PCM and tells whether the last 40 ms had a voice's pitch.
func (p *Pitch) Frame(pcm []byte) bool {
	for i := 0; i+1 < len(pcm); i += 2 {
		x := float64(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
		p.full = append(p.full, x)
		p.win = append(p.win, p.lp.step(x))
	}
	if n := len(p.win) - pitchWin; n > 0 {
		p.win, p.full = p.win[n:], p.full[n:]
	}
	if len(p.win) < pitchWin {
		return false
	}
	var eAll, eLow float64
	for i := range p.win {
		eAll += p.full[i] * p.full[i]
		eLow += p.win[i] * p.win[i]
	}
	if math.Sqrt(eAll/float64(len(p.full))) < pitchMinRMS || eLow < lowShare*eAll {
		return false
	}
	return periodic(p.win) >= pitchCorr
}

// periodic: the best normalized autocorrelation of x at a voice's pitch lags.
func periodic(x []float64) float64 {
	best := 0.0
	for lag := minLag; lag <= maxLag && lag < len(x)/2; lag++ {
		var xy, xx, yy float64
		for i := 0; i+lag < len(x); i++ {
			a, b := x[i], x[i+lag]
			xy += a * b
			xx += a * a
			yy += b * b
		}
		if xx > 0 && yy > 0 {
			best = math.Max(best, xy/math.Sqrt(xx*yy))
		}
	}
	return best
}

// biquad: a second-order Butterworth low-pass (RBJ's cookbook).
type biquad struct{ b0, b1, b2, a1, a2, x1, x2, y1, y2 float64 }

func lowpass(fc, fs float64) biquad {
	w := 2 * math.Pi * fc / fs
	alpha := math.Sin(w) / math.Sqrt2
	c := math.Cos(w)
	a0 := 1 + alpha
	return biquad{b0: (1 - c) / 2 / a0, b1: (1 - c) / a0, b2: (1 - c) / 2 / a0, a1: -2 * c / a0, a2: (1 - alpha) / a0}
}

func (f *biquad) step(x float64) float64 {
	y := f.b0*x + f.b1*f.x1 + f.b2*f.x2 - f.a1*f.y1 - f.a2*f.y2
	f.x2, f.x1, f.y2, f.y1 = f.x1, x, f.y1, y
	return y
}
