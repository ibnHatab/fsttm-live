package audio

import (
	"encoding/binary"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
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
	// Slope is how much louder she gets per doubling of the radius, dB (0: 6, the free field's):
	// the person's distance calibration fits it (comfortable at r metres: Slope·log2(r) over the
	// minimum).
	Slope float64

	mu     sync.Mutex
	cur    *reply
	gain   float64 // 0: 1 (as recorded); SetGain
	bubble float64 // metres her voice is meant to reach (0: 1); SetBubble
	reach  float64 // the person's distance (0: unknown); SetReach
	est    float64 // her stream's speech level, dBFS RMS (a running estimate)
	last   []byte  // the last reply as it came (up to SampleMax): Sample plays it again
	cur0   []byte  // the current reply's, while it comes
	ducked atomic.Bool
}

// DuckGain is her voice while a person speaks over it (-12 dB): the barge-in's feedback, before
// the words decide (a cut, or her full voice back).
const DuckGain = 0.25

// Stats: the stream's learnt speech level and the level it is brought to now (dBFS RMS), and
// the radius it is aimed at (metres).
func (s *Speaker) Stats() (est, target, radius float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.radius()
	return s.est, s.Level + s.Gain(r), r
}

// Duck lowers her voice now, as it is played (not as it is queued: the reply comes faster than
// real time), or brings it back.
func (s *Speaker) Duck(on bool) { s.ducked.Store(on) }

// Ducked reports whether her voice is lowered.
func (s *Speaker) Ducked() bool { return s.ducked.Load() }

// MaxGain is the loudest the calibration allows over the minimum: +12 dB, her echo still 10 dB
// under the person's voice on the processed channel (+18: her echo opens utterances).
const MaxGain = 12.0

// SampleMax is the length of her last reply kept for Sample (24 kHz s16le: 12 s).
const SampleMax = 24000 * 2 * 12

func (s *Speaker) slope() float64 {
	if s.Slope > 0 {
		return s.Slope
	}
	return 6
}

// MaxBubble is the widest bubble: MaxGain at the slope (4 m at 6 dB a doubling).
func (s *Speaker) MaxBubble() float64 { return math.Pow(2, MaxGain/s.slope()) }

// Gain is the dB over the minimum for a radius in metres.
func (s *Speaker) Gain(r float64) float64 { return s.slope() * math.Log2(r) }

// SetGain scales her voice from the next chunk on. 1 is as recorded; clipped to [0.2, 2].
func (s *Speaker) SetGain(g float64) {
	s.mu.Lock()
	s.gain = math.Max(0.2, math.Min(2, g))
	s.mu.Unlock()
}

// SetBubble is how far her voice carries, in metres (the person's knob): 1 is the calibrated
// minimum, each doubling Slope dB louder; [0.5, MaxBubble].
func (s *Speaker) SetBubble(m float64) {
	s.mu.Lock()
	s.bubble = math.Max(0.5, math.Min(s.MaxBubble(), m))
	s.mu.Unlock()
}

// SetSlope is the distance calibration's dB per doubling (clipped to 2..9).
func (s *Speaker) SetSlope(db float64) {
	s.mu.Lock()
	s.Slope = math.Max(2, math.Min(9, db))
	s.bubble = math.Min(s.bubble, s.MaxBubble())
	s.mu.Unlock()
}

// Sample plays her last reply again at the level now (the knob being set); false when there is
// none or she is speaking.
func (s *Speaker) Sample() bool {
	s.mu.Lock()
	pcm := s.last
	busy := s.cur != nil
	s.mu.Unlock()
	if len(pcm) == 0 || busy {
		return false
	}
	for i := 0; i < len(pcm); i += 4800 {
		s.Write(pcm[i:min(len(pcm), i+4800)])
	}
	s.mu.Lock()
	s.cur0 = nil // a sample is not a reply to keep
	s.mu.Unlock()
	s.Finish()
	return true
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
		// an average of the chunks' energies, as the calibration measured speech (an average of
		// their dB reads speech ~3 dB lower: live, 2026-11-25, her feed at -33 for a -36 target)
		if d := 20 * math.Log10(float64(r)/32768); d > -50 {
			p := math.Pow(10, s.est/10)
			s.est = 10 * math.Log10(p+(math.Pow(10, d/10)-p)*0.05)
		}
	}
	return math.Pow(10, (s.Level-s.est+s.Gain(s.radius()))/20)
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
	if f, ok := w.(interface{ Fd() uintptr }); ok {
		// a small pipe: what is written is played within ~85 ms, so a duck is heard at once
		unix.FcntlInt(f.Fd(), unix.F_SETPIPE_SZ, 4096)
	}
	s.ducked.Store(false)
	r := &reply{cmd: cmd, q: make(chan []byte, 8192), start: time.Now()}
	go func(w io.WriteCloser) {
		const piece = 960 // 20 ms at 24 kHz: the duck applies from the next piece
	out:
		for b := range r.q {
			for i := 0; i < len(b); i += piece {
				p := b[i:min(len(b), i+piece)]
				if s.ducked.Load() {
					p = scale(p, DuckGain)
				}
				if _, err := w.Write(p); err != nil {
					break out
				}
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
	if len(s.cur0) < SampleMax {
		s.cur0 = append(s.cur0, pcm[:min(len(pcm), SampleMax-len(s.cur0))]...)
	}
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
	if len(s.cur0) > 24000 { // a second or more: what Sample plays
		s.last = s.cur0
	}
	s.cur0 = nil
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
	s.cur0 = nil
	return heard, written
}
