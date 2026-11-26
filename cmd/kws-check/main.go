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

	"github.com/ibnHatab/fsttm-live/internal/kws"
)

func main() {
	model := flag.String("model", os.ExpandEnv("$HOME/models/sherpa-kws/sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01"), "the model's folder")
	words := flag.String("keywords", os.ExpandEnv("$HOME/repo/fsttm-live/testdata/kws-keywords.txt"), "the keywords file")
	th := flag.Float64("threshold", 0.25, "lower hears more")
	whole := flag.Bool("whole", false, "one stream for the whole file")
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
