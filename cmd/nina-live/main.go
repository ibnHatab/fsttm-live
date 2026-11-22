// nina-live: Nina's dialog runtime on the Gemini Live API. The ReSpeaker (or any mic) feeds
// our endpointer; the ears (gemini-3.5-transcribe-live) turn each utterance into text; the
// floor machine and attention decide what reaches the voice+mind session
// (gemini-3.1-flash-live-preview), whose audio is played locally and cut on barge-in.
//
// Lines typed on stdin: plain text is the person's words; "/note LEVEL text" a NOTE from
// SOURCES; "/say text" spoken verbatim. -script runs an acoustic test (see script.go).
package main

import (
	"bufio"
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/audio"
	"github.com/ibnHatab/fsttm-live/internal/dialog"
	"github.com/ibnHatab/fsttm-live/internal/evlog"
	"github.com/ibnHatab/fsttm-live/internal/live"
	"github.com/ibnHatab/fsttm-live/internal/rhvoice"
)

var (
	keyFile    = flag.String("key", "~/.config/fsttm/gemini.key", "AI Studio key file")
	charter    = flag.String("charter", "testdata/charter-v2.txt", "the voice session's system instruction")
	earsModel  = flag.String("ears", "gemini-3.5-transcribe-live", "transcription model")
	mindModel  = flag.String("mind", "gemini-3.1-flash-live-preview", "voice+mind model")
	voiceName  = flag.String("voice", "", "the mind's prebuilt voice (empty: default)")
	vocab      = flag.String("vocab", "Nina,Prana,lab,bedroom,living room,ventilation,night mode,boost", "customVocabulary, comma-separated")
	micDev     = flag.String("dev", "hw:ArrayUAC10", "capture device (ALSA, or pulse:SOURCE)")
	micCh      = flag.Int("channels", 6, "capture channels")
	speechCh   = flag.Int("speech", 0, "speech channel")
	refCh      = flag.Int("ref", 5, "echo reference channel (-1: none)")
	outDev     = flag.String("out", "", "playback device for Nina (aplay -D, or pulse:SINK; empty: default; plughw:ArrayUAC10: the ReSpeaker's jack)")
	local      = flag.String("local", "", "local voice: '' none, rhvoice: speak the mind's transcript with RHVoice")
	rhProfile  = flag.String("rhvoice", "slt", "RHVoice profile")
	vadMode    = flag.Int("vadmode", 3, "WebRTC VAD aggressiveness")
	hang       = flag.Duration("hang", 700*time.Millisecond, "silence that ends an utterance")
	followUp   = flag.Duration("followup", 12*time.Second, "addressed without her name this long after she spoke (0: always while awake)")
	awakeFor   = flag.Duration("awake", 90*time.Second, "asleep after this long without a turn")
	knob       = flag.String("knob", "important", "lowest NOTE level spoken while awake: info, important, urgent")
	asleep     = flag.Bool("asleep", true, "start asleep (waiting for her name)")
	logFile    = flag.String("log", "", "JSONL event log (also on stdout)")
	script     = flag.String("script", "", "acoustic test script")
	quiet      = flag.Bool("q", false, "events only to the log file")
	personSink = flag.String("person-sink", "", "-script: the PulseAudio sink the person's WAVs play on")
)

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	return p
}

func main() {
	flag.Parse()
	var outs []io.Writer
	if !*quiet {
		outs = append(outs, os.Stdout)
	}
	if *logFile != "" {
		f, err := os.Create(*logFile)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		outs = append(outs, f)
	}
	L := evlog.New(outs...)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client, err := live.NewClient(ctx, expand(*keyFile))
	if err != nil {
		log.Fatal(err)
	}
	sys, err := os.ReadFile(*charter)
	if err != nil {
		log.Fatal(err)
	}
	lv, ok := dialog.ParseLevel(*knob)
	if !ok {
		log.Fatalf("knob: %q", *knob)
	}

	home := newHome()
	mind := &live.Voice{Client: client, Model: *mindModel, VoiceName: *voiceName, System: string(sys),
		Tools: home.tools(), Funcs: home.funcs(), Compress: 24000, Log: L.Ev}
	ears := &live.Ears{Client: client, Model: *earsModel, Vocab: strings.Split(*vocab, ","),
		Settle: 200 * time.Millisecond, FinalWait: 1500 * time.Millisecond, MaxAge: 8 * time.Minute, Log: L.Ev}
	spk := audio.NewSpeaker(*outDev, 24000)
	var tts dialog.TTS
	if *local == "rhvoice" {
		tts = rhvoice.Voice{Profile: *rhProfile}
	}
	eng := dialog.New(dialog.Config{FollowUp: *followUp, AwakeFor: *awakeFor, ReplyTimeout: 10 * time.Second,
		Knob: lv, NoteGap: 2 * time.Second, LocalVoice: *local == "rhvoice", StartAsleep: *asleep},
		ears, mind, spk, tts, L.Ev)
	mind.Out, ears.Out = eng.MindEv, eng.Heard
	go func() {
		for ok := range spk.Done {
			eng.SpkDone <- ok
		}
	}()
	ears.Start(ctx)
	if err := mind.Connect(ctx); err != nil {
		log.Fatal(err)
	}

	frames := make(chan audio.Frame, 64)
	capt := &audio.Capture{Device: *micDev, Channels: *micCh, Speech: *speechCh, Ref: *refCh}
	go func() {
		if err := capt.Run(frames); err != nil && ctx.Err() == nil {
			L.Ev("capture_error", map[string]any{"err": err.Error()})
		}
		close(frames)
	}()
	defer capt.Stop()
	ep := &audio.Endpointer{Mode: *vadMode, On: 6, Hang: *hang, PreRoll: 300 * time.Millisecond, MaxUtt: 25 * time.Second}
	go ep.Run(frames, eng.Vad)

	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			eng.Typed <- sc.Text()
		}
	}()
	L.Ev("ready", map[string]any{"mind": *mindModel, "ears": *earsModel, "out": *outDev, "local": *local, "knob": lv.String()})
	if *script != "" {
		go func() {
			if err := runScript(ctx, *script, eng, L); err != nil {
				L.Ev("script_error", map[string]any{"err": err.Error()})
			}
			stop()
		}()
	}
	eng.Run(ctx)
}
