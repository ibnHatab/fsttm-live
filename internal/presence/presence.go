// Package presence turns the radar's frames into a person's presence (the user's tuning,
// 2026-11-24): present after Confirm of detection, absent after Release without; the distance
// smoothed; approaching or departing from its trend over Window; near (Near) and in reach of the
// screen (Wake). Approaching and departing are derived here, the radar reports distance only.
package presence

import (
	"math"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/ld2410"
)

type Config struct {
	Confirm, Release, Window time.Duration
	Near, Wake               float64       // metres
	Trend                    float64       // metres over Window: approaching or departing beyond it
	Hold                     time.Duration // a new trend holds this long before it counts
}

// Default is the user's initial tuning (2026-11-24).
var Default = Config{Confirm: 500 * time.Millisecond, Release: 10 * time.Second, Window: 2 * time.Second,
	Near: 1.5, Wake: 2, Trend: 0.25, Hold: time.Second}

// State is what the tracker says now.
type State struct {
	Present       bool
	Distance      float64 // metres, smoothed (0 when absent)
	Trend         string  // approaching | departing | still | "" (absent)
	Near, InReach bool
	Moving, Still bool // the radar's flags
	Since         time.Time
}

type sample struct {
	at time.Time
	d  float64
}

// Tracker keeps the state between frames.
type Tracker struct {
	C                   Config
	st                  State
	firstSeen, lastSeen time.Time
	ema                 float64
	hist                []sample
	cand                string    // a new trend, waiting to hold
	candAt              time.Time // since when
}

// Update takes a frame at a time and says whether the state changed in a way worth reporting.
func (t *Tracker) Update(f ld2410.Frame, now time.Time) (State, bool) {
	prev := t.st
	if f.Detected() {
		if t.firstSeen.IsZero() || now.Sub(t.lastSeen) > t.C.Release {
			t.firstSeen = now
		}
		t.lastSeen = now
		d := float64(f.Nearest()) / 100
		if t.ema == 0 {
			t.ema = d
		} else {
			t.ema += 0.3 * (d - t.ema)
		}
		t.hist = append(t.hist, sample{now, t.ema})
		for len(t.hist) > 1 && now.Sub(t.hist[0].at) > t.C.Window {
			t.hist = t.hist[1:]
		}
	}
	present := prev.Present
	if !present && f.Detected() && now.Sub(t.firstSeen) >= t.C.Confirm {
		present = true
	}
	if present && now.Sub(t.lastSeen) > t.C.Release {
		present = false
	}
	st := State{Present: present, Since: prev.Since}
	if present != prev.Present {
		st.Since = now
	}
	if present {
		st.Distance = math.Round(t.ema*100) / 100
		st.Near, st.InReach = st.Distance <= t.C.Near, st.Distance <= t.C.Wake
		st.Moving, st.Still = f.Moving(), f.Stationary()
		raw := "still"
		if len(t.hist) > 1 && now.Sub(t.hist[0].at) >= t.C.Window/2 {
			switch dd := t.ema - t.hist[0].d; {
			case dd < -t.C.Trend:
				raw = "approaching"
			case dd > t.C.Trend:
				raw = "departing"
			}
		}
		// a new trend holds a second before it counts (standing still, the radar's distance wanders)
		st.Trend = prev.Trend
		if prev.Trend == "" {
			st.Trend = raw
		} else if raw != prev.Trend {
			if raw != t.cand {
				t.cand, t.candAt = raw, now
			} else if now.Sub(t.candAt) >= t.C.Hold {
				st.Trend, t.cand = raw, ""
			}
		} else {
			t.cand = ""
		}
	} else {
		t.ema, t.hist = 0, nil
	}
	t.st = st
	changed := st.Present != prev.Present || st.Near != prev.Near || st.InReach != prev.InReach || st.Trend != prev.Trend ||
		math.Abs(st.Distance-prev.Distance) >= 0.1
	return st, changed
}
