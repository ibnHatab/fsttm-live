package audio

import (
	"encoding/binary"
	"testing"
)

// The duck fades in over two pieces and back over two: no step at a piece's edge.
func TestFade(t *testing.T) {
	p := make([]byte, 960)
	for i := 0; i < 480; i++ {
		binary.LittleEndian.PutUint16(p[2*i:], uint16(int16(10000)))
	}
	at := func(b []byte, i int) int16 { return int16(binary.LittleEndian.Uint16(b[2*i:])) }
	var f fade
	a := f.apply(p, true)
	b := f.apply(p, true)
	c := f.apply(p, true)
	if at(a, 0) != 10000 || at(a, 479) > 7000 || at(b, 0) > at(a, 479)+100 || at(c, 0) != 2500 || at(c, 479) != 2500 {
		t.Fatalf("duck: %d…%d, %d…%d, %d…%d", at(a, 0), at(a, 479), at(b, 0), at(b, 479), at(c, 0), at(c, 479))
	}
	d := f.apply(p, false)
	e := f.apply(p, false)
	g := f.apply(p, false)
	if at(d, 0) != 2500 || at(e, 479) < 9900 || &g[0] != &p[0] {
		t.Fatalf("back: %d…, …%d", at(d, 0), at(e, 479))
	}
}
