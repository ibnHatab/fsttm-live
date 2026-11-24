package dialog

import "testing"

// 2026-11-24: her weather answer came back through the mic as "-4 … 4°" and, its numbers new
// words, was taken for the person barging in: she answered her own echo five times.
func TestEchoNumbers(t *testing.T) {
	said := "Tomorrow will be cloudy, with temperatures between minus four and four degrees."
	heard := "Tomorrow will be cloudy with temperatures between -4 and 4°."
	if n := Novel(heard, said); BargeIn(n) {
		t.Fatalf("her echo read as a barge-in: novel %v", n)
	}
	if r := StripEcho(heard, said); r != "" {
		t.Fatalf("her echo not stripped: %q", r)
	}
	if n := Novel("set a timer for twenty five minutes", "It is twenty one degrees."); !BargeIn(n) {
		t.Fatalf("the person's words lost: %v", n)
	}
	if got := words("Twenty-one, minus 4°, ninety"); len(got) != 3 || got[0] != "21" || got[1] != "4" || got[2] != "90" {
		t.Fatalf("words: %v", got)
	}
}
