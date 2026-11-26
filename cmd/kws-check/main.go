// kws-check: the wake-word spotter (internal/kws, as the dialog runs it) over a recording, timed: which
// keywords it hears and where, and how fast it decodes on this machine (the real-time factor: a Pi that
// decodes slower than the audio lags). Input: s16le mono 16 kHz. The whole file is one stream (-whole)
// or cut where it is quiet, as the endpointer would (the default: utterances of voiced 20 ms frames).
//
//	kws-check -model DIR -keywords FILE [-threshold 0.25] FILE.s16
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/audio"
	"github.com/ibnHatab/fsttm-live/internal/kws"
)

func main() {
	model := flag.String("model", os.ExpandEnv("$HOME/models/sherpa-kws/sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01"), "the model's folder")
	words := flag.String("keywords", os.ExpandEnv("$HOME/repo/fsttm-live/testdata/kws-keywords.txt"), "the keywords file")
	th := flag.Float64("threshold", 0.25, "lower hears more")
	whole := flag.Bool("whole", false, "one stream for the whole file")
	ep := flag.Bool("ep", false, "cut by the dialog's own endpointer (vad_mode 3, hang 700 ms, pre-roll 300 ms), fed as the engine feeds it")
	hang := flag.Duration("hang", 700*time.Millisecond, "the endpointer's hang (-ep)")
	preroll := flag.Duration("preroll", 300*time.Millisecond, "the endpointer's pre-roll (-ep)")
	flag.Parse()
	b, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	k, err := kws.New(*model, *words, float32(*th))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *ep {
		runEP(k, b, *hang, *preroll)
		return
	}
	const chunk = 3200 // 100 ms, as the dialog feeds it
	db := func(p []byte) float64 {
		var s float64
		for i := 0; i+1 < len(p); i += 2 {
			v := float64(int16(binary.LittleEndian.Uint16(p[i:])))
			s += v * v
		}
		return 20 * math.Log10(math.Max(math.Sqrt(s/float64(len(p)/2)), 1)/32768)
	}
	// utterances: chunks louder than the file's quiet by 10 dB, joined across gaps under 0.6 s
	var levels []float64
	for i := 0; i+chunk <= len(b); i += chunk {
		levels = append(levels, db(b[i:i+chunk]))
	}
	floor := 0.0
	{
		s := append([]float64(nil), levels...)
		for i := range s {
			for j := i + 1; j < len(s); j++ {
				if s[j] < s[i] {
					s[i], s[j] = s[j], s[i]
				}
			}
		}
		if len(s) > 0 {
			floor = s[len(s)/5]
		}
	}
	type span struct{ a, b int }
	var spans []span
	if *whole {
		spans = []span{{0, len(levels)}}
	} else {
		open, quiet := -1, 0
		for i, l := range levels {
			if l > floor+10 {
				if open < 0 {
					open = max(0, i-3) // a 0.3 s pre-roll, as the dialog's
				}
				quiet = 0
			} else if open >= 0 {
				if quiet++; quiet > 6 {
					spans = append(spans, span{open, i})
					open = -1
				}
			}
		}
		if open >= 0 {
			spans = append(spans, span{open, len(levels)})
		}
	}
	var spent time.Duration
	hits := 0
	for _, s := range spans {
		k.Begin()
		peak := -120.0
		for i := s.a; i < s.b; i++ {
			peak = math.Max(peak, levels[i])
			t0 := time.Now()
			hit := k.Feed(b[i*chunk : (i+1)*chunk])
			spent += time.Since(t0)
			if hit != "" {
				hits++
				fmt.Printf("  %-9s at %5.1fs (%.1fs into the utterance)\n", hit, float64(i+1)/10, float64(i+1-s.a)/10)
			}
		}
		t0 := time.Now()
		if hit := k.End(); hit != "" {
			hits++
			fmt.Printf("  %-9s at the end of the utterance at %.1fs\n", hit, float64(s.b)/10)
		}
		spent += time.Since(t0)
		fmt.Printf("utterance %5.1f–%5.1fs  peak %.1f dBFS\n", float64(s.a)/10, float64(s.b)/10, peak)
	}
	audio := time.Duration(len(b)/2) * time.Second / 16000
	fed := 0
	for _, s := range spans {
		fed += s.b - s.a
	}
	fmt.Printf("floor %.1f dBFS; %d utterances, %d hits; decoding %.2fs for %.1fs of audio fed (real-time factor %.2f)\n",
		floor, len(spans), hits, spent.Seconds(), float64(fed)/10, spent.Seconds()/(float64(fed)/10))
	_ = audio
}

// runEP replays the file through audio.Endpointer and feeds the spotter as internal/dialog's engine
// does (Begin + the pre-roll at Start, each Audio chunk until a hit, End's flush).
func runEP(k *kws.Spotter, b []byte, hang, preroll time.Duration) {
	in := make(chan audio.Frame, 64)
	out := make(chan audio.Event, 64)
	e := &audio.Endpointer{Mode: 3, On: 6, Hang: hang, PreRoll: preroll}
	go func() {
		for i := 0; i+640 <= len(b); i += 640 {
			in <- audio.Frame{PCM: b[i : i+640]}
		}
		for i := 0; i < 100; i++ { // a second of silence: the last utterance ends
			in <- audio.Frame{PCM: make([]byte, 640)}
		}
		close(in)
	}()
	go func() { _ = e.Run(in, out); close(out) }()
	pos, start, hit, n, hits := 0, 0, "", 0, 0
	for ev := range out {
		switch ev.Kind {
		case audio.Start:
			start, hit = pos, ""
			k.Begin()
			hit = k.Feed(ev.PCM)
			pos += len(ev.PCM) / 2 // not exact: the pre-roll is from before; a position for reading
			n++
		case audio.Audio:
			if hit == "" {
				hit = k.Feed(ev.PCM)
			}
			pos += len(ev.PCM) / 2
		case audio.End:
			if h := k.End(); hit == "" {
				hit = h
			}
			if hit != "" {
				hits++
			}
			fmt.Printf("utterance #%d %.1fs voiced %v: %q\n", n, float64(ev.Dur)/1e9, ev.Voiced, hit)
			_ = start
		}
	}
	fmt.Printf("%d utterances, %d hits\n", n, hits)
}
