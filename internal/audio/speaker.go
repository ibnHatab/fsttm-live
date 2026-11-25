package audio

import (
	"encoding/binary"
	"io"
	"math"
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

	// Level is her speech's loudness out, in dBFS RMS, for a bubble of 1 m: the acoustic
	// calibration's minimum (scripts/calib: as loud at the microphones as a person talking near
	// them). 0: no normalising, her voice as recorded (times SetGain).
	Level float64

	mu     sync.Mutex
	cur    *reply
	gain   float64 // 0: 1 (as recorded); SetGain
	bubble float64 // metres her voice is meant to reach (0: 1); SetBubble
	reach  float64 // the person's distance (0: unknown); SetReach
	est    float64 // her stream's speech level, dBFS RMS (a running estimate)
}

// MaxBubble is the widest bubble the calibration allows: +12 dB over the minimum, her echo still
// under the person's voice on the processed channel (wider: her echo opens utterances).
const MaxBubble = 4.0

// SetGain scales her voice from the next chunk on. 1 is as recorded; clipped to [0.2, 2].
func (s *Speaker) SetGain(g float64) {
	s.mu.Lock()
	s.gain = math.Max(0.2, math.Min(2, g))
	s.mu.Unlock()
}

// SetBubble is how far her voice carries, in metres (the person's knob): 1 is the calibrated
// minimum, each doubling 6 dB louder; [0.5, MaxBubble].
func (s *Speaker) SetBubble(m float64) {
	s.mu.Lock()
	s.bubble = math.Max(0.5, math.Min(MaxBubble, m))
	s.mu.Unlock()
}

// SetReach is the person's distance (the radar): she speaks to reach them, never past the
// bubble. 0: unknown (the bubble's edge).
func (s *Speaker) SetReach(m float64) { s.mu.Lock(); s.reach = m; s.mu.Unlock() }

// Radius is where her voice is aimed now: the person, within the bubble.
func (s *Speaker) Radius() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.radius()
}

func (s *Speaker) radius() float64 {
	b := s.bubble
	if b == 0 {
		b = 1
	}
	if s.reach > 0 {
		return math.Max(0.5, math.Min(b, s.reach))
	}
	return b
}

// level: the gain that brings a chunk of her stream to Level at the radius. The stream's own
// level is learnt from its voiced chunks (a slow average, kept across replies: one reply's start
// is not louder than its end).
func (s *Speaker) level(pcm []byte) float64 {
	if s.est == 0 {
		s.est = -20 // what the Live voice gives, about, before the first measure
	}
	if r := RMS(pcm); r > 0 {
		if d := 20 * math.Log10(float64(r)/32768); d > -50 {
			s.est += (d - s.est) * 0.05
		}
	}
	return math.Pow(10, (s.Level-s.est+20*math.Log10(s.radius()))/20)
}

// scale is pcm (s16le) at the gain, a copy.
func scale(pcm []byte, g float64) []byte {
	out := make([]byte, len(pcm)&^1)
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i:]))) * g
		binary.LittleEndian.PutUint16(out[i:], uint16(int16(math.Max(-32768, math.Min(32767, v)))))
	}
	return out
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
	g := 1.0
	if s.gain != 0 {
		g = s.gain
	}
	if s.Level != 0 {
		g *= s.level(pcm)
	}
	if g != 1 {
		pcm = scale(pcm, g)
	}
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
