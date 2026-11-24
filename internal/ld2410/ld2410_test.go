package ld2410

import (
	"bytes"
	"testing"
)

// a basic report as the module sends it: both targets, moving 62 cm (100), stationary 50 cm (100), 43 cm
var frame = []byte{0xF4, 0xF3, 0xF2, 0xF1, 0x0D, 0x00, 0x02, 0xAA, 0x03, 0x3E, 0x00, 0x64, 0x32, 0x00, 0x64, 0x2B, 0x00, 0x55, 0x00, 0xF8, 0xF7, 0xF6, 0xF5}

func TestScan(t *testing.T) {
	in := append(append([]byte{0x00, 0xF4, 0x11}, frame...), frame...) // noise first, then two frames
	var got []Frame
	Scan(bytes.NewReader(in), func(f Frame) { got = append(got, f) })
	if len(got) != 2 {
		t.Fatalf("frames: %d", len(got))
	}
	f := got[0]
	if f.Target != 3 || f.MovingCM != 62 || f.StationaryCM != 50 || f.DetectionCM != 43 || f.MovingEnergy != 100 {
		t.Fatalf("frame: %+v", f)
	}
	if !f.Detected() || !f.Moving() || !f.Stationary() || f.Nearest() != 43 {
		t.Fatalf("flags: %+v", f)
	}
	if (Frame{Target: 5}).Detected() {
		t.Fatal("a calibration code is not a person")
	}
}
