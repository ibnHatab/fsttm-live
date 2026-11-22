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
