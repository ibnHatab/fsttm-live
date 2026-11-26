package audio

import (
	"encoding/binary"
	"testing"
)

func frameAt(amp int16) []byte {
	b := make([]byte, 640)
	for i := 0; i < 320; i++ {
		v := amp
		if i%2 == 1 {
			v = -amp
		}
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

// Without a reference: her echo behind the canceller (-69 dBFS at its loudest) is never a barge;
// the person (-39) is, after 8 of 15 voiced frames; unvoiced loud frames are not.
func TestBargeLevel(t *testing.T) {
	var b barge
	echo, person := frameAt(12), frameAt(370) // ≈ -69 and -39 dBFS
	for i := 0; i < 30; i++ {
		if b.level(-55, true, echo) {
			t.Fatal("her echo taken for the person")
		}
	}
	var b2 barge
	got := -1
	for i := 0; i < 15; i++ {
		if b2.level(-55, true, person) && got < 0 {
			got = i + 1
		}
	}
	if got != 8 {
		t.Fatalf("the person: evidence after %d frames, want 8", got)
	}
	var b3 barge
	for i := 0; i < 15; i++ {
		if b3.level(-55, false, person) {
			t.Fatal("an unvoiced sound taken for the person")
		}
	}
}
