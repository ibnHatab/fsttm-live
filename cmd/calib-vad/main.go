// calib-vad: Nina's endpointer (internal/audio, as pkg/dialogue builds it) over a raw recording:
// s16le mono 16 kHz on stdin; one line per utterance it would open (start, length, voiced,
// pitched), then a summary. The calibration's question: does her own echo open an utterance
// (a false floor for the person: the music ducks, a barge-in is looked for), does the person's
// voice open one.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/audio"
)

func main() {
	mode := flag.Int("mode", 3, "WebRTC VAD aggressiveness")
	hang := flag.Duration("hang", 700*time.Millisecond, "silence that ends an utterance")
	on := flag.Int("on", 6, "voiced frames of the last 10 that start one")
	margin := flag.Float64("barge", 0, "with -ref: the Barge margin, dB (0 off)")
	withRef := flag.Bool("ref", false, "stdin is 2 channels: speech, then the echo reference")
	flag.Parse()
	in := make(chan audio.Frame)
	out := make(chan audio.Event)
	var fed atomic.Int64
	ep := &audio.Endpointer{Mode: *mode, On: *on, Hang: *hang, PreRoll: 300 * time.Millisecond, MaxUtt: 25 * time.Second, BargeMargin: *margin}
	go func() {
		ch := 1
		if *withRef {
			ch = 2
		}
		buf := make([]byte, audio.FrameLen*2*ch)
		for {
			if _, err := io.ReadFull(os.Stdin, buf); err != nil {
				break
			}
			f := audio.Frame{PCM: make([]byte, audio.FrameLen*2)}
			ref := make([]byte, audio.FrameLen*2)
			for i := 0; i < audio.FrameLen; i++ {
				copy(f.PCM[2*i:], buf[2*ch*i:2*ch*i+2])
				if ch == 2 {
					copy(ref[2*i:], buf[2*ch*i+2:2*ch*i+4])
				}
			}
			if ch == 2 {
				f.Ref = audio.RMS(ref)
			}
			in <- f
			fed.Add(1)
		}
		// flush: a second of silence ends an open utterance
		for i := 0; i < 50; i++ {
			in <- audio.Frame{PCM: make([]byte, audio.FrameLen*2)}
		}
		close(in)
	}()
	go func() { ep.Run(in, out); close(out) }()
	var frames int
	var start time.Duration
	n, nb, total := 0, 0, time.Duration(0)
	for ev := range out {
		switch ev.Kind {
		case audio.Start:
			start = time.Duration(fed.Load()-10)*20*time.Millisecond
		case audio.Audio:
			frames += len(ev.PCM) / (audio.FrameLen * 2)
		case audio.Barge:
			nb++
			fmt.Printf("barge %6.2fs\n", float64(fed.Load())*0.02)
		case audio.End:
			n++
			total += ev.Dur
			fmt.Printf("utt %6.2fs  len %5.2fs  voiced %5.2fs  pitched %5.2fs\n", start.Seconds(), ev.Dur.Seconds(), ev.Voiced.Seconds(), ev.Pitched.Seconds())
		}
		_ = frames
	}
	fmt.Printf("barges %d\n", nb)
	fmt.Printf("utterances %d  total %.2fs\n", n, total.Seconds())
}
