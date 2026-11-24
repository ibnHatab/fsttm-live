package presence

import (
	"testing"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/ld2410"
)

func TestTracker(t *testing.T) {
	tr := &Tracker{C: Default}
	t0 := time.Unix(0, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	seen := func(cm uint16) ld2410.Frame {
		return ld2410.Frame{Target: 3, DetectionCM: cm, MovingCM: cm, StationaryCM: cm}
	}
	if st, _ := tr.Update(seen(300), at(0)); st.Present {
		t.Fatal("present before Confirm")
	}
	st, ch := tr.Update(seen(300), at(600))
	if !st.Present || !ch || st.Distance != 3 || st.InReach {
		t.Fatalf("not present after Confirm: %+v", st)
	}
	ms := 600
	for cm := uint16(300); cm >= 120; cm -= 15 { // walking up to the screen, 10 frames a second
		ms += 100
		st, _ = tr.Update(seen(cm), at(ms))
	}
	if st.Trend != "approaching" || !st.InReach { // near comes a moment later: the smoothing lags ~0.3 s
		t.Fatalf("walking up: %+v", st)
	}
	for i := 0; i < 30; i++ { // standing there
		ms += 100
		st, _ = tr.Update(seen(120), at(ms))
	}
	if st.Trend != "still" || !st.Near || st.Distance > 1.25 {
		t.Fatalf("standing: %+v", st)
	}
	ms += 11000 // gone: no target for longer than Release
	if st, ch = tr.Update(ld2410.Frame{}, at(ms)); st.Present || !ch {
		t.Fatalf("still present after Release: %+v", st)
	}
}
