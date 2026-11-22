package audio

import (
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
)

type Event struct {
	Kind   Kind
	PCM    []byte
	Ref    int           // mean echo-reference level over the frames (AEC active if > 0)
	Dur    time.Duration // End: the utterance's length
	Voiced time.Duration // End: its voiced part
	Forced bool          // End: cut at MaxUtt
}

type Endpointer struct {
	Mode    int           // WebRTC aggressiveness 0..3
	On      int           // voiced frames out of the last 10 that start an utterance
	Hang    time.Duration // unvoiced time that ends it
	PreRoll time.Duration // audio kept from before the start
	MaxUtt  time.Duration // longest utterance (media in the room must not hold the floor)
	Muted   func() bool   // the mic switch: frames are silence while it is off
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
		frames  int
		refSum  int
		pre     [][]byte
		chunk   []byte
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
			Voiced: time.Duration(voicedN) * 20 * time.Millisecond, Forced: forced}
		active, hist = false, nil
	}
	for f := range in {
		pcm := f.PCM
		if p.Muted != nil && p.Muted() {
			pcm = silence
		}
		voiced, _ := vad.Process(Rate, pcm)
		hist = append(hist, voiced)
		if len(hist) > 10 {
			hist = hist[1:]
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
				active, quiet, voicedN, frames, refSum = true, 0, 0, len(pre), 0
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
		if voiced {
			quiet = 0
			voicedN++
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
