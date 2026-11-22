package main

// The acoustic test: the ReSpeaker's processed channel (ch 0 of 6) captured with arecord,
// framed by a local voice detector (WebRTC VAD, 20 ms frames), and streamed to the Live
// session between our activityStart and activityEnd; the test utterances are played from
// the speaker (paplay) one by one.

import (
	"encoding/binary"
	"flag"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"time"

	webrtcvad "github.com/maxhawkins/go-webrtcvad"
	"google.golang.org/genai"
)

var (
	mic       = flag.Bool("mic", false, "capture the ReSpeaker and play the WAVs from the speaker")
	micDev    = flag.String("dev", "hw:ArrayUAC10", "ALSA capture device (6 channels, keep ch 0)")
	vadMode   = flag.Int("vadmode", 3, "WebRTC VAD aggressiveness 0..3")
	onFrames  = flag.Int("on", 6, "voiced frames out of the last 10 to start an utterance")
	hangMs    = flag.Int("hang", 700, "unvoiced time that ends an utterance (ms)")
	preRollMs = flag.Int("preroll", 300, "audio kept before the start (ms)")
	settle    = flag.Duration("settle", 4*time.Second, "pause after each played file")
)

const frame = 320 // 20 ms at 16 kHz

func runMic(sess *genai.Session, L *logger, files []string) {
	vad, err := webrtcvad.New()
	if err != nil {
		log.Fatal(err)
	}
	if err := vad.SetMode(*vadMode); err != nil {
		log.Fatal(err)
	}
	rec := exec.Command("arecord", "-q", "-D", *micDev, "-c", "6", "-r", "16000", "-f", "S16_LE", "-t", "raw")
	out, _ := rec.StdoutPipe()
	if err := rec.Start(); err != nil {
		log.Fatal(err)
	}
	defer rec.Process.Kill()

	go func() { // the speaker side: the test utterances, one by one
		time.Sleep(1500 * time.Millisecond)
		for _, f := range files {
			L.ev("play", map[string]any{"file": filepath.Base(f)})
			exec.Command("paplay", f).Run()
			L.ev("play_end", map[string]any{"file": filepath.Base(f)})
			time.Sleep(*settle)
		}
		L.ev("playlist_done", nil)
	}()

	buf := make([]byte, frame*6*2)
	mono := make([]byte, frame*2)
	var (
		hist    []bool
		active  bool
		quiet   int
		preroll [][]byte
		chunk   []byte
		voicedN int
		started time.Time
	)
	maxPre := *preRollMs / 20
	hangF := *hangMs / 20
	deadline := time.Now().Add(time.Duration(len(files))*(*settle+6*time.Second) + 5*time.Second)
	for time.Now().Before(deadline) {
		if _, err := io.ReadFull(out, buf); err != nil {
			log.Fatal(err)
		}
		for i := 0; i < frame; i++ {
			copy(mono[2*i:], buf[i*12:i*12+2]) // ch 0
		}
		f := append([]byte(nil), mono...)
		voiced, _ := vad.Process(16000, f)
		hist = append(hist, voiced)
		if len(hist) > 10 {
			hist = hist[1:]
		}
		n := 0
		for _, v := range hist {
			if v {
				n++
			}
		}
		if !active {
			preroll = append(preroll, f)
			if len(preroll) > maxPre {
				preroll = preroll[1:]
			}
			if n >= *onFrames {
				active, quiet, voicedN, started = true, 0, 0, time.Now()
				sess.SendRealtimeInput(genai.LiveRealtimeInput{ActivityStart: &genai.ActivityStart{}})
				L.ev("vad_start", map[string]any{"level": rms(f)})
				for _, p := range preroll {
					chunk = append(chunk, p...)
				}
				preroll = nil
			}
			continue
		}
		chunk = append(chunk, f...)
		if voiced {
			quiet = 0
			voicedN++
		} else {
			quiet++
		}
		if len(chunk) >= 3200 { // 100 ms
			sess.SendRealtimeInput(genai.LiveRealtimeInput{Audio: &genai.Blob{Data: chunk, MIMEType: "audio/pcm;rate=16000"}})
			chunk = nil
		}
		if quiet >= hangF {
			if len(chunk) > 0 {
				sess.SendRealtimeInput(genai.LiveRealtimeInput{Audio: &genai.Blob{Data: chunk, MIMEType: "audio/pcm;rate=16000"}})
				chunk = nil
			}
			sess.SendRealtimeInput(genai.LiveRealtimeInput{ActivityEnd: &genai.ActivityEnd{}})
			L.setMark()
			L.ev("vad_end", map[string]any{"dur_s": time.Since(started).Seconds(), "voiced_s": float64(voicedN) * 0.02})
			active = false
		}
	}
}

func rms(b []byte) int {
	var s float64
	for i := 0; i+1 < len(b); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(b[i:])))
		s += v * v
	}
	return int(s / float64(len(b)/2))
}
