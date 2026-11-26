package oww

import (
	"sync"
	"time"
)

// Stream listens continuously and answers the dialog's spotter calls (internal/dialog.Spotter): an
// utterance's Begin, Feed and End tell a detection made since it began, or in the second before (the
// name before the voice detector opened). Hear takes every microphone frame, utterance or not, so
// the detector is warm; a slow machine drops frames rather than hold the capture.
type Stream struct {
	d     *Detector
	th    float32
	label string
	ev    func(string, map[string]any)
	in    chan []byte

	mu    sync.Mutex
	hits  []time.Time
	last  time.Time
	begun time.Time
	peak  float32
	err   bool
}

// NewStream starts the detector's loop: a score at or over th is a detection, labelled label.
func NewStream(d *Detector, label string, th float32, ev func(string, map[string]any)) *Stream {
	s := &Stream{d: d, th: th, label: label, ev: ev, in: make(chan []byte, 256)}
	go s.run()
	return s
}

// Hear takes a frame of 16 kHz 16-bit PCM (never blocks).
func (s *Stream) Hear(pcm []byte) {
	select {
	case s.in <- pcm:
	default:
	}
}

func (s *Stream) run() {
	for p := range s.in {
		sc, err := s.d.Feed(p)
		s.mu.Lock()
		if err != nil && !s.err {
			s.err = true
			if s.ev != nil {
				s.ev("oww_error", map[string]any{"err": err.Error()})
			}
		}
		if sc > s.peak {
			s.peak = sc
		}
		if sc >= s.th && time.Since(s.last) > 1500*time.Millisecond {
			s.last = time.Now()
			s.hits = append(s.hits, s.last)
		}
		s.mu.Unlock()
	}
}

// take: the label of a detection since the utterance began (or the second before), once.
func (s *Stream) take() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	from := s.begun.Add(-time.Second)
	keep := s.hits[:0]
	hit := ""
	for _, h := range s.hits {
		switch {
		case hit == "" && h.After(from):
			hit = s.label
		case time.Since(h) < 5*time.Second:
			keep = append(keep, h)
		}
	}
	s.hits = keep
	return hit
}

// Begin: an utterance opened.
func (s *Stream) Begin() {
	s.mu.Lock()
	s.begun, s.peak = time.Now(), 0
	s.mu.Unlock()
}

// Feed: the utterance's audio is the stream's already; tells a detection.
func (s *Stream) Feed([]byte) string { return s.take() }

// End: the utterance closed; tells a detection, and logs its highest score (the threshold's tuning).
func (s *Stream) End() string {
	h := s.take()
	s.mu.Lock()
	peak := s.peak
	s.mu.Unlock()
	if s.ev != nil {
		s.ev("oww_peak", map[string]any{"peak": peak, "hit": h != ""})
	}
	return h
}
