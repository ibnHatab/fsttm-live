// oww-check: an openWakeWord model (internal/oww) over a recording: the moments its score passes the
// threshold, and how fast it runs here. Input: s16le mono 16 kHz, or a WAV (its header skipped).
//
//	oww-check -models DIR -word nina.onnx [-threshold 0.5] FILE
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/ibnHatab/fsttm-live/internal/oww"
)

var _ = sherpa.NewKeywordSpotter // links sherpa-onnx, and with it the ONNX runtime, as nina does

func main() {
	dir := flag.String("models", os.ExpandEnv("$HOME/models/oww"), "melspectrogram.onnx and embedding_model.onnx")
	word := flag.String("word", os.ExpandEnv("$HOME/models/oww/nina.onnx"), "the wake word's model")
	th := flag.Float64("threshold", 0.5, "a detection")
	all := flag.Bool("all", false, "print every chunk's score")
	flag.Parse()
	b, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if strings.HasSuffix(flag.Arg(0), ".wav") {
		b = b[44:]
	}
	d, err := oww.New(*dir, *word)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	t0 := time.Now()
	hits, last := 0, -10.0
	for i := 0; i+640 <= len(b); i += 640 { // 20 ms frames, as the capture sends them
		s, err := d.Feed(b[i : i+640])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		t := float64(i+640) / 32000
		if s >= 0 && *all {
			fmt.Printf("%6.2f %.3f\n", t, s)
		}
		if s >= float32(*th) {
			if t-last > 1.5 {
				hits++
				fmt.Printf("  wake at %6.2fs  score %.2f\n", t, s)
			}
			last = t
		}
	}
	dur := float64(len(b)) / 32000
	el := time.Since(t0).Seconds()
	fmt.Printf("%d detections in %.1fs of audio; %.2fs to run (real-time factor %.3f): mel %.2fs, embedding %.2fs, word %.2fs\n",
		hits, dur, el, el/dur, d.Spent[0].Seconds(), d.Spent[1].Seconds(), d.Spent[2].Seconds())
}
