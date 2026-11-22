package dialog

import (
	"regexp"
	"strings"
)

// Without a working echo canceller (Nina on a speaker the array has no reference for) the
// microphone hears Nina at the level of a person. The ears transcribe her, so what they hear
// while she speaks is compared with what she is saying (her output transcript): her own words
// are echo; words she did not say are the person barging in.

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}' ]+`)

func words(s string) []string {
	return strings.Fields(nonWord.ReplaceAllString(strings.ToLower(s), " "))
}

// interrupters are words that barge in alone. Not her name: the transcriber's vocabulary bias
// puts a phantom "Nina" into her own echo.
var interrupters = map[string]bool{"stop": true, "wait": true, "quiet": true, "enough": true, "cancel": true}

// Novel returns the words of heard that are not in said (with a typo's tolerance).
func Novel(heard, said string) []string {
	known := map[string]bool{}
	sw := words(said)
	for _, w := range sw {
		known[w] = true
	}
	joined := strings.Join(sw, "") // "goodnight" said, "good night" heard
	var out []string
	for _, w := range words(heard) {
		if known[w] || (len([]rune(w)) >= 3 && strings.Contains(joined, w)) {
			continue
		}
		near := false
		if len([]rune(w)) >= 4 {
			for _, s := range sw {
				if editAtMost1(w, s) {
					near = true
					break
				}
			}
		}
		if !near {
			out = append(out, w)
		}
	}
	return out
}

// filler words don't count towards a barge-in, nor does her name: the transcriber invents
// both in her echo ("for Nina").
var filler = map[string]bool{"nina": true, "a": true, "an": true, "the": true, "for": true, "to": true,
	"and": true, "of": true, "is": true, "it": true, "in": true, "on": true, "i": true, "you": true,
	"so": true, "uh": true, "um": true, "oh": true, "yeah": true, "that": true, "this": true, "at": true}

// BargeIn decides from the novel words: two or more that are not filler, or one interrupter.
func BargeIn(novel []string) bool {
	n := 0
	for _, w := range novel {
		if interrupters[w] {
			return true
		}
		if !filler[w] {
			n++
		}
	}
	return n >= 2
}

func editAtMost1(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	if len(ra)-len(rb) > 1 {
		return false
	}
	i, j, d := 0, 0, 0
	for i < len(ra) && j < len(rb) {
		if ra[i] == rb[j] {
			i++
			j++
			continue
		}
		d++
		if d > 1 {
			return false
		}
		if len(ra) == len(rb) {
			j++
		}
		i++
	}
	return d+(len(ra)-i) <= 1
}

// HeardPart is the beginning of said that fits in heard/written of its audio: what the person
// heard before the cut, for the next turn's note.
func HeardPart(said string, frac float64) string {
	w := strings.Fields(said)
	n := int(float64(len(w))*frac + 0.5)
	if n <= 0 {
		return ""
	}
	if n >= len(w) {
		return said
	}
	return strings.Join(w[:n], " ") + " …"
}

// AfterEcho is what the person said at the end of an utterance that began in her voice: the
// longest run of its first words found, in order, in what she said (her echo; a typo's
// tolerance) is dropped. "the bedroom? In the lab." after "…the lab or the bedroom?" is "in the
// lab"; "In the lab." keeps all of it (no run of two of her words begins it).
func AfterEcho(heard, said string) string {
	hw, sw := words(heard), words(said)
	best := 0
	for i := range sw {
		k := 0
		for k < len(hw) && i+k < len(sw) && (hw[k] == sw[i+k] || editAtMost1(hw[k], sw[i+k])) {
			k++
		}
		best = max(best, k)
	}
	if best < 2 && best < len(hw) {
		best = 0 // one shared word is not her voice
	}
	return strings.Join(hw[best:], " ")
}
