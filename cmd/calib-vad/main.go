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
	flag.Parse()
	in := make(chan audio.Frame)
	out := make(chan audio.Event)
	var fed atomic.Int64
	ep := &audio.Endpointer{Mode: *mode, On: *on, Hang: *hang, PreRoll: 300 * time.Millisecond, MaxUtt: 25 * time.Second}
	go func() {
		buf := make([]byte, audio.FrameLen*2)
		for {
			if _, err := io.ReadFull(os.Stdin, buf); err != nil {
				break
			}
			in <- audio.Frame{PCM: append([]byte(nil), buf...)}
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
	n, total := 0, time.Duration(0)
	for ev := range out {
		switch ev.Kind {
		case audio.Start:
			start = time.Duration(fed.Load()-10)*20*time.Millisecond
		case audio.Audio:
			frames += len(ev.PCM) / (audio.FrameLen * 2)
		case audio.End:
			n++
			total += ev.Dur
			fmt.Printf("utt %6.2fs  len %5.2fs  voiced %5.2fs  pitched %5.2fs\n", start.Seconds(), ev.Dur.Seconds(), ev.Voiced.Seconds(), ev.Pitched.Seconds())
		}
		_ = frames
	}
	fmt.Printf("utterances %d  total %.2fs\n", n, total.Seconds())
}
