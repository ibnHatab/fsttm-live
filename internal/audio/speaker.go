package audio

import (
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Speaker plays one reply at a time (24 kHz s16le mono, what the Live API and RHVoice give)
// through aplay. Write never blocks the caller: the reply arrives faster than real time and
// is queued. Finish ends the reply (Done fires when the last sample has played); Cut kills it
// at once, the local half of a barge-in (Done then fires with false).
type Speaker struct {
	Device string // aplay -D (empty: the default output), or pulse:SINK
	Rate   int
	Done   chan bool // one per reply: true played to the end, false cut or failed

	mu  sync.Mutex
	cur *reply
}

type reply struct {
	cmd     *exec.Cmd
	q       chan []byte
	start   time.Time
	written int
	cut     bool
}

func NewSpeaker(device string, rate int) *Speaker {
	return &Speaker{Device: device, Rate: rate, Done: make(chan bool, 8)}
}

func (s *Speaker) open() *reply {
	var cmd *exec.Cmd
	if sink, ok := strings.CutPrefix(s.Device, "pulse:"); ok { // a PulseAudio sink, e.g. an echo canceller's
		cmd = exec.Command("pacat", "-d", sink, "--rate="+strconv.Itoa(s.Rate), "--channels=1", "--format=s16le", "--latency-msec=60")
	} else {
		args := []string{"-q", "-f", "S16_LE", "-r", strconv.Itoa(s.Rate), "-c", "1", "-t", "raw"}
		if s.Device != "" {
			args = append([]string{"-D", s.Device}, args...)
		}
		cmd = exec.Command("aplay", args...)
	}
	w, err := cmd.StdinPipe()
	if err != nil || cmd.Start() != nil {
		s.Done <- false
		return nil
	}
	r := &reply{cmd: cmd, q: make(chan []byte, 8192), start: time.Now()}
	go func(w io.WriteCloser) {
		for b := range r.q {
			if _, err := w.Write(b); err != nil {
				break
			}
		}
		w.Close()
		cmd.Wait()
		s.mu.Lock()
		if s.cur == r {
			s.cur = nil
		}
		cut := r.cut
		s.mu.Unlock()
		s.Done <- !cut
	}(w)
	return r
}

// Write queues audio of the current reply, starting the player on the first chunk.
func (s *Speaker) Write(pcm []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		if s.cur = s.open(); s.cur == nil {
			return
		}
	}
	if s.cur.q == nil { // finished, still playing out: not a new reply's place
		return
	}
	s.cur.written += len(pcm)
	select {
	case s.cur.q <- pcm:
	default: // longer than the queue: drop rather than block the dialog
	}
}

// Playing reports whether a reply is open or still playing out.
func (s *Speaker) Playing() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.cur != nil }

// Finish closes the reply: Done fires when aplay has played it out. It reports false when no
// reply was open (nothing to wait for).
func (s *Speaker) Finish() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return false
	}
	if s.cur.q != nil {
		close(s.cur.q)
		s.cur.q = nil
	}
	return true
}

// Cut stops the reply now and returns how much of it had been heard and written.
func (s *Speaker) Cut() (heard, written time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.cur
	if r == nil {
		return 0, 0
	}
	written = time.Duration(r.written/2) * time.Second / time.Duration(s.Rate)
	heard = min(time.Since(r.start), written)
	r.cut = true
	if r.q != nil {
		close(r.q)
		r.q = nil
	}
	r.cmd.Process.Kill()
	s.cur = nil
	return heard, written
}
