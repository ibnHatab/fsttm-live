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
	return numbers(strings.Fields(nonWord.ReplaceAllString(strings.ToLower(s), " ")))
}

// numbers puts numbers one way on both sides: she says "minus four degrees", the transcriber
// writes her echo "-4°" (2026-11-24: her weather answer, heard back as the person's, looped).
// Spelled numbers become digits ("twenty one" → "21"); the words the transcriber writes as
// symbols (minus, degrees, percent) go, on both sides. English only, for now.
func numbers(ws []string) []string {
	out := ws[:0:0]
	for i := 0; i < len(ws); i++ {
		w := ws[i]
		if dropUnit[w] {
			continue
		}
		if t, ok := tens[w]; ok {
			if i+1 < len(ws) {
				if u, ok := units[ws[i+1]]; ok && u > 0 && u < 10 {
					out = append(out, itoa(t+u))
					i++
					continue
				}
			}
			out = append(out, itoa(t))
			continue
		}
		if u, ok := units[w]; ok {
			out = append(out, itoa(u))
			continue
		}
		out = append(out, w)
	}
	return out
}

var dropUnit = map[string]bool{"minus": true, "degrees": true, "degree": true, "percent": true}

var units = map[string]int{"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
	"fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19}

var tens = map[string]int{"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50, "sixty": 60,
	"seventy": 70, "eighty": 80, "ninety": 90}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
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

// StripEcho is what the person said in an utterance that carries her voice too: what she said
// comes through the mic in her order, so it is taken out only as runs of her words in that
// order (a typo's tolerance): a leading run of two words or more (the tail of her reply the
// utterance began in), and runs of four or more elsewhere. The person's own words stay, in
// order, even those she used too: "No, just boost it." after "…boost it, or switch it back?"
// is kept whole. Nothing taken out: the heard text as it was.
func StripEcho(heard, said string) string {
	hw, sw := words(heard), words(said)
	run := func(at int) int { // the longest run of hw from at found in sw
		best := 0
		for i := range sw {
			k := 0
			for at+k < len(hw) && i+k < len(sw) && (hw[at+k] == sw[i+k] || editAtMost1(hw[at+k], sw[i+k])) {
				k++
			}
			best = max(best, k)
		}
		return best
	}
	var kept []string
	stripped := false
	for at := 0; at < len(hw); {
		if k := run(at); (at == 0 && k >= 2) || k >= 4 {
			at += k
			stripped = true
			continue
		}
		kept = append(kept, hw[at])
		at++
	}
	if !stripped {
		return strings.TrimSpace(heard)
	}
	return strings.Join(kept, " ")
}
