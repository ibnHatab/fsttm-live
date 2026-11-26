package audio

import (
	"math"
	"time"

	webrtcvad "github.com/maxhawkins/go-webrtcvad"
)

// The endpointer is the floor machine's ears for speech and silence: our machine, not the
// model, decides where an utterance starts and ends (N09-1071: the user's G and R).

type Kind int

const (
	Start Kind = iota // speech began; PCM is the pre-roll
	Audio             // 100 ms of the open utterance
	End               // the utterance ended (hang time of silence, or the length cap)
	// Barge: a voice over hers, clearly above her echo (BargeMargin), while she speaks: once per
	// utterance, as soon as the evidence holds (the dialog ducks her at once: the person's
	// feedback, before any word is transcribed)
	Barge
)

type Event struct {
	Kind    Kind
	PCM     []byte
	Ref     int           // mean echo-reference level over the frames (AEC active if > 0)
	Dur     time.Duration // End: the utterance's length
	Voiced  time.Duration // End: its voiced part
	Pitched time.Duration // End: its voiced part with a voice's pitch (Pitch): a click has none
	Forced  bool          // End: cut at MaxUtt
}

type Endpointer struct {
	Mode    int           // WebRTC aggressiveness 0..3
	On      int           // voiced frames out of the last 10 that start an utterance
	Hang    time.Duration // unvoiced time that ends it
	PreRoll time.Duration // audio kept from before the start
	MaxUtt  time.Duration // longest utterance (media in the room must not hold the floor)
	Muted   func() bool   // the mic switch: frames are silence while it is off
	// BargeMargin: a voiced frame within this many dB of the echo reference's envelope is the
	// person's (her echo, after the array's canceller, is ~22 dB under it; a person near the
	// array ~3 dB: scripts/calib). 0: no Barge events (no reference: a plain microphone).
	BargeMargin float64
	// MinLevel: a voiced frame quieter than this (dBFS RMS) is not a voice in the room (0: off).
	// WebRTC's detector judges a voice's shape, not its loudness: behind a software canceller the
	// radio's talk, at -68 dBFS, still opened utterances and ducked the radio every 3 s (pero,
	// 2026-11-26; the person there: -39 dBFS).
	MinLevel float64
	// BargeLevel: with no reference (a software canceller before the capture), a voiced frame at
	// least this loud (dBFS RMS) is the person's: her echo there stays far under it (pero, 2026-11-26:
	// her voice -88 dBFS median, -69 at its loudest, after PipeWire's canceller; the person -39).
	// 0: off. Evidence and events as BargeMargin's (8 of 15 frames).
	BargeLevel float64
}

// barge is the evidence of a voice over hers: the reference's envelope (decaying 40 dB/s: her
// echo's tail in the room and the canceller's latency) and the last 15 frames' verdicts.
type barge struct {
	env  float64
	hist []bool
	sent bool
}

func dbfs(rms int) float64 {
	if rms <= 0 {
		return -120
	}
	return 20 * math.Log10(float64(rms)/32768)
}

// level: the evidence without a reference: a voiced frame at least min dBFS (8 of the last 15).
func (b *barge) level(min float64, voiced bool, speech []byte) bool {
	b.hist = append(b.hist, voiced && dbfs(RMS(speech)) >= min)
	if len(b.hist) > 15 {
		b.hist = b.hist[1:]
	}
	n := 0
	for _, v := range b.hist {
		if v {
			n++
		}
	}
	return n >= 8
}

// frame: true when the evidence of a voice over hers holds now (8 of the last 15 frames, 160 ms).
func (b *barge) frame(margin float64, voiced bool, speech []byte, ref int) bool {
	r := dbfs(ref)
	b.env = math.Max(r, b.env-40*0.02)
	if b.env < -120 {
		b.env = -120
	}
	over := voiced && b.env > -70 && dbfs(RMS(speech)) >= b.env-margin
	b.hist = append(b.hist, over)
	if len(b.hist) > 15 {
		b.hist = b.hist[1:]
	}
	n := 0
	for _, v := range b.hist {
		if v {
			n++
		}
	}
	return n >= 8
}

func (p *Endpointer) Run(in <-chan Frame, out chan<- Event) error {
	vad, err := webrtcvad.New()
	if err != nil {
		return err
	}
	if err := vad.SetMode(p.Mode); err != nil {
		return err
	}
	var (
		hist    []bool
		active  bool
		quiet   int
		voicedN int
		pitchN  int
		pitch   = NewPitch()
		frames  int
		refSum  int
		pre     [][]byte
		chunk   []byte
		bg      barge
	)
	maxPre := int(p.PreRoll / (20 * time.Millisecond))
	hangF := int(p.Hang / (20 * time.Millisecond))
	maxF := int(p.MaxUtt / (20 * time.Millisecond))
	silence := make([]byte, frameByte)
	end := func(forced bool) {
		if len(chunk) > 0 {
			out <- Event{Kind: Audio, PCM: chunk}
			chunk = nil
		}
		ref := 0
		if frames > 0 {
			ref = refSum / frames
		}
		out <- Event{Kind: End, Ref: ref, Dur: time.Duration(frames) * 20 * time.Millisecond,
			Voiced:  time.Duration(voicedN) * 20 * time.Millisecond,
			Pitched: time.Duration(pitchN) * 20 * time.Millisecond, Forced: forced}
		active, hist = false, nil
		bg.sent = false
	}
	for f := range in {
		pcm := f.PCM
		if p.Muted != nil && p.Muted() {
			pcm = silence
		}
		voiced, _ := vad.Process(Rate, pcm)
		if voiced && p.MinLevel < 0 && dbfs(RMS(pcm)) < p.MinLevel {
			voiced = false
		}
		pitched := pitch.Frame(pcm) // always: its window spans frames
		hist = append(hist, voiced)
		if len(hist) > 10 {
			hist = hist[1:]
		}
		over := p.BargeMargin > 0 && bg.frame(p.BargeMargin, voiced, pcm, f.Ref)
		if p.BargeLevel < 0 && p.BargeMargin <= 0 {
			over = bg.level(p.BargeLevel, voiced, pcm)
		}
		if !active {
			pre = append(pre, pcm)
			if len(pre) > maxPre {
				pre = pre[1:]
			}
			n := 0
			for _, v := range hist {
				if v {
					n++
				}
			}
			if n >= p.On {
				active, quiet, voicedN, pitchN, frames, refSum = true, 0, 0, 0, len(pre), 0
				var b []byte
				for _, x := range pre {
					b = append(b, x...)
				}
				pre = nil
				out <- Event{Kind: Start, PCM: b, Ref: f.Ref}
			}
			continue
		}
		frames++
		refSum += f.Ref
		chunk = append(chunk, pcm...)
		if over && !bg.sent {
			bg.sent = true
			out <- Event{Kind: Barge, Ref: f.Ref}
		}
		if voiced {
			quiet = 0
			voicedN++
			if pitched {
				pitchN++
			}
		} else {
			quiet++
		}
		if len(chunk) >= 5*frameByte {
			out <- Event{Kind: Audio, PCM: chunk, Ref: f.Ref}
			chunk = nil
		}
		switch {
		case quiet >= hangF:
			end(false)
		case maxF > 0 && frames >= maxF:
			end(true)
		}
	}
	return nil
}
