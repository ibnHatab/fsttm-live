package live

import "testing"

func TestVocabEcho(t *testing.T) {
	vocab := []string{"Nina", "Prana", "lab", "bedroom", "living room", "ventilation", "night mode", "boost"}
	for s, want := range map[string]bool{
		"Nina, Prana, lab, bedroom, living room, ventilation, night mode, boost.": true,
		"Nina, Prana, lab":                       true, // its beginning, as an interim
		"nina prana lab and bedroom":             true,
		"Nina, boost.":                           false,
		"Nina, boost the lab ventilation.":       false,
		"Nina, night mode in the bedroom.":       false,
		"Is the ventilation running in the lab?": false,
		"":                                       false,
	} {
		if got := VocabEcho(s, vocab); got != want {
			t.Errorf("VocabEcho(%q) = %v", s, got)
		}
	}
}

func TestRefusal(t *testing.T) {
	for _, s := range []string{"I'm just a language model and can't help with that.", "I’m just a language model and can’t help with that.",
		"As a language model, I can't do that."} {
		if !Refusal(s) {
			t.Errorf("not a refusal: %q", s)
		}
	}
	for _, s := range []string{"Nina, show me the album Nina's gallery.", "I can't help with that right now, Nina.",
		"Tell me about large language models."} {
		if Refusal(s) {
			t.Errorf("a refusal: %q", s)
		}
	}
}

func TestCut(t *testing.T) {
	for _, c := range []struct {
		full, base, want string
		fresh            bool
	}{
		{"Next video.", "Nina, what time is it?", "Next video.", true}, // anew: no letters lost
		{"Nina, stop the radio.", "Next video.", "Nina, stop the radio.", true},
		{"Go to sleep.", "Nina, stop the radio.", "Go to sleep.", true},
		{"Cancel the timer.", "Cancel the alarm.", "Cancel the timer.", true},                  // the same first words (pero)
		{"Nina, what time is it? Next video.", "Nina, what time is it?", "Next video.", false}, // accumulated
		{"Nina, what time is it.Next video.", "Nina, what time is it?", "Next video.", false},  // its end revised
		{"", "", "", false},
	} {
		if s, fresh := Cut(c.full, c.base); s != c.want || fresh != c.fresh {
			t.Errorf("Cut(%q, %q) = %q %v, want %q %v", c.full, c.base, s, fresh, c.want, c.fresh)
		}
	}
}
