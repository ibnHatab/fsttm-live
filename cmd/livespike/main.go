// livespike: the design assumptions of fsttm-live, tested against the Gemini Live API.
//
// Each WAV is one user utterance. It is streamed at real-time pace (16 kHz PCM, 100 ms
// chunks), framed by our own activityStart/activityEnd when -manual (the floor machine's
// endpointing), and every server message is logged as one JSON line with its time from the
// utterance's activityEnd. -play sends Nina's audio to the speaker; -save keeps it as WAV.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
)

var (
	model    = flag.String("model", "gemini-3.8-live", "Live model")
	voice    = flag.String("voice", "", "prebuilt voice (empty: the model's default)")
	manual   = flag.Bool("manual", true, "our activityStart/activityEnd (automatic detection off)")
	vocab    = flag.String("vocab", "Nina,Prana,lab,bedroom,ventilation,night mode,boost", "customVocabulary, comma-separated")
	sysFile  = flag.String("sys", "", "system instruction file")
	tools    = flag.Bool("tools", true, "declare the fake home tools")
	play     = flag.Bool("play", false, "play Nina's audio")
	save     = flag.String("save", "", "directory for Nina's audio per turn")
	gap      = flag.Duration("gap", 1500*time.Millisecond, "silence between utterances after the turn ends")
	wait     = flag.Duration("wait", 10*time.Second, "longest wait for a turn to end")
	keyFile  = flag.String("key", "~/.config/fsttm/gemini.key", "API key file")
	sayFirst = flag.String("say", "", "text sent as a client turn before the utterances (the narration test)")
	logFile  = flag.String("log", "", "JSONL event log (stdout too)")
	silence  = flag.Int("silence", 0, "automatic detection: silenceDurationMs (0: the server's default)")
	textOut  = flag.Bool("text", false, "TEXT response modality (the transcribe model)")
)

type logger struct {
	mu   sync.Mutex
	t0   time.Time
	mark time.Time // the last activityEnd
	out  []io.Writer
}

func (l *logger) ev(kind string, f map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f == nil {
		f = map[string]any{}
	}
	f["ev"] = kind
	now := time.Now()
	f["t"] = now.Sub(l.t0).Milliseconds()
	if !l.mark.IsZero() {
		f["since_end"] = now.Sub(l.mark).Milliseconds()
	}
	b, _ := json.Marshal(f)
	for _, w := range l.out {
		fmt.Fprintln(w, string(b))
	}
}

func (l *logger) setMark() { l.mu.Lock(); l.mark = time.Now(); l.mu.Unlock() }

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	return p
}

// readWav returns mono s16 samples and the rate (a plain PCM WAV).
func readWav(path string) ([]int16, int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(b) < 44 || string(b[0:4]) != "RIFF" {
		return nil, 0, fmt.Errorf("%s: not a WAV", path)
	}
	var rate, ch int
	i := 12
	for i+8 <= len(b) {
		id := string(b[i : i+4])
		n := int(binary.LittleEndian.Uint32(b[i+4:]))
		body := b[i+8 : min(len(b), i+8+n)]
		switch id {
		case "fmt ":
			ch = int(binary.LittleEndian.Uint16(body[2:]))
			rate = int(binary.LittleEndian.Uint32(body[4:]))
		case "data":
			s := make([]int16, len(body)/2/ch)
			for k := range s {
				s[k] = int16(binary.LittleEndian.Uint16(body[k*2*ch:]))
			}
			return s, rate, nil
		}
		i += 8 + n + n%2
	}
	return nil, 0, fmt.Errorf("%s: no data", path)
}

func resample(s []int16, from, to int) []int16 {
	if from == to {
		return s
	}
	n := len(s) * to / from
	o := make([]int16, n)
	for k := range o {
		x := float64(k) * float64(from) / float64(to)
		j := int(x)
		if j+1 >= len(s) {
			o[k] = s[len(s)-1]
			continue
		}
		f := x - float64(j)
		o[k] = int16(float64(s[j])*(1-f) + float64(s[j+1])*f)
	}
	return o
}

func pcm(s []int16) []byte {
	b := make([]byte, 2*len(s))
	for k, v := range s {
		binary.LittleEndian.PutUint16(b[2*k:], uint16(v))
	}
	return b
}

func writeWav(path string, data []byte, rate int) error {
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(data)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(data)))
	return os.WriteFile(path, append(h, data...), 0o644)
}

func homeTools() []*genai.Tool {
	str := func(d string) *genai.Schema { return &genai.Schema{Type: genai.TypeString, Description: d} }
	return []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
		Name:        "home_air",
		Description: "The home's ventilation units (Prana recuperators): one in the lab, one in the bedroom. Switch them or change their mode.",
		Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{
			"device": str("the unit: 'lab ventilation' or 'bedroom ventilation'"),
			"intent": str("ON, OFF, NIGHT, BOOST or QUERY"),
		}, Required: []string{"device", "intent"}},
	}}}}
}

func main() {
	flag.Parse()
	key, err := os.ReadFile(expand(*keyFile))
	if err != nil {
		log.Fatal(err)
	}
	L := &logger{t0: time.Now(), out: []io.Writer{os.Stdout}}
	if *logFile != "" {
		f, err := os.Create(*logFile)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		L.out = append(L.out, f)
	}
	ctx := context.Background()
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: strings.TrimSpace(string(key)), Backend: genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{APIVersion: "v1beta"}})
	if err != nil {
		log.Fatal(err)
	}
	cfg := &genai.LiveConnectConfig{
		ResponseModalities:       []genai.Modality{genai.ModalityAudio},
		InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
		RealtimeInputConfig: &genai.RealtimeInputConfig{
			AutomaticActivityDetection: &genai.AutomaticActivityDetection{Disabled: *manual},
			ActivityHandling:           genai.ActivityHandlingStartOfActivityInterrupts,
		},
	}
	if *silence > 0 {
		ms := int32(*silence)
		cfg.RealtimeInputConfig.AutomaticActivityDetection.SilenceDurationMs = &ms
	}
	if *textOut {
		cfg.ResponseModalities = []genai.Modality{genai.ModalityText}
		cfg.OutputAudioTranscription = nil
	}
	if *vocab != "" {
		cfg.InputAudioTranscription.CustomVocabulary = strings.Split(*vocab, ",")
	}
	if *voice != "" {
		cfg.SpeechConfig = &genai.SpeechConfig{VoiceConfig: &genai.VoiceConfig{PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: *voice}}}
	}
	if *sysFile != "" {
		b, err := os.ReadFile(*sysFile)
		if err != nil {
			log.Fatal(err)
		}
		cfg.SystemInstruction = &genai.Content{Parts: []*genai.Part{{Text: string(b)}}}
	}
	if *tools {
		cfg.Tools = homeTools()
	}
	t := time.Now()
	sess, err := client.Live.Connect(ctx, *model, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer sess.Close()
	L.ev("connected", map[string]any{"ms": time.Since(t).Milliseconds(), "model": *model, "manual": *manual})

	var player io.WriteCloser
	if *play {
		cmd := exec.Command("aplay", "-q", "-f", "S16_LE", "-r", "24000", "-c", "1")
		player, _ = cmd.StdinPipe()
		if err := cmd.Start(); err != nil {
			log.Fatal(err)
		}
	}

	turnDone := make(chan struct{}, 8)
	var (
		mu        sync.Mutex
		turnAudio []byte
		firstOut  bool
		outText   strings.Builder
		inText    strings.Builder
		turnN     int
	)
	go func() {
		for {
			m, err := sess.Receive()
			if err != nil {
				L.ev("receive_error", map[string]any{"err": err.Error()})
				close(turnDone)
				return
			}
			if m.SetupComplete != nil {
				L.ev("setup_complete", nil)
			}
			if m.VoiceActivity != nil {
				L.ev("voice_activity", map[string]any{"type": m.VoiceActivity.VoiceActivityType})
			}
			if m.ToolCall != nil {
				var rs []*genai.FunctionResponse
				for _, fc := range m.ToolCall.FunctionCalls {
					L.ev("tool_call", map[string]any{"name": fc.Name, "args": fc.Args})
					rs = append(rs, &genai.FunctionResponse{ID: fc.ID, Name: fc.Name,
						Response: map[string]any{"status": "done", "text": fmt.Sprintf("%v: %v done", fc.Args["device"], fc.Args["intent"])}})
				}
				_ = sess.SendToolResponse(genai.LiveToolResponseInput{FunctionResponses: rs})
			}
			if m.ToolCallCancellation != nil {
				L.ev("tool_cancel", map[string]any{"ids": m.ToolCallCancellation.IDs})
			}
			if m.GoAway != nil {
				L.ev("go_away", map[string]any{"left": m.GoAway.TimeLeft.String()})
			}
			if m.UsageMetadata != nil {
				L.ev("usage", map[string]any{"prompt": m.UsageMetadata.PromptTokenCount, "response": m.UsageMetadata.ResponseTokenCount, "total": m.UsageMetadata.TotalTokenCount})
			}
			sc := m.ServerContent
			if sc == nil {
				continue
			}
			if sc.InterimInputTranscription != nil && sc.InterimInputTranscription.Text != "" {
				L.ev("interim_in", map[string]any{"text": sc.InterimInputTranscription.Text})
			}
			if sc.InputTranscription != nil && sc.InputTranscription.Text != "" {
				mu.Lock()
				inText.WriteString(sc.InputTranscription.Text)
				mu.Unlock()
				L.ev("in", map[string]any{"text": sc.InputTranscription.Text, "finished": sc.InputTranscription.Finished})
			}
			if sc.ModelTurn != nil {
				for _, p := range sc.ModelTurn.Parts {
					if p.InlineData != nil && len(p.InlineData.Data) > 0 {
						mu.Lock()
						if !firstOut {
							firstOut = true
							L.ev("first_audio", map[string]any{"mime": p.InlineData.MIMEType})
						}
						turnAudio = append(turnAudio, p.InlineData.Data...)
						mu.Unlock()
						if player != nil {
							player.Write(p.InlineData.Data)
						}
					}
					if p.Text != "" {
						L.ev("text_part", map[string]any{"text": p.Text, "thought": p.Thought})
					}
				}
			}
			if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
				mu.Lock()
				outText.WriteString(sc.OutputTranscription.Text)
				mu.Unlock()
			}
			if sc.Interrupted {
				L.ev("interrupted", nil)
			}
			if sc.GenerationComplete {
				L.ev("generation_complete", nil)
			}
			if sc.WaitingForInput {
				L.ev("waiting_for_input", nil)
			}
			if sc.TurnComplete {
				mu.Lock()
				audioS := float64(len(turnAudio)) / 48000
				L.ev("turn_complete", map[string]any{"reason": sc.TurnCompleteReason, "status": sc.InteractionStatus,
					"heard": strings.TrimSpace(inText.String()), "said": strings.TrimSpace(outText.String()), "audio_s": audioS})
				if *save != "" && len(turnAudio) > 0 {
					os.MkdirAll(*save, 0o755)
					writeWav(filepath.Join(*save, fmt.Sprintf("turn%02d.wav", turnN)), turnAudio, 24000)
				}
				turnN++
				turnAudio, firstOut = nil, false
				outText.Reset()
				inText.Reset()
				mu.Unlock()
				turnDone <- struct{}{}
			}
		}
	}()

	drain := func() {
		for {
			select {
			case <-turnDone:
			default:
				return
			}
		}
	}
	awaitTurn := func() bool {
		select {
		case _, ok := <-turnDone:
			return ok
		case <-time.After(*wait):
			L.ev("no_turn", map[string]any{"waited_s": wait.Seconds()})
			return true
		}
	}

	if *sayFirst != "" {
		L.setMark()
		tc := true
		L.ev("client_turn", map[string]any{"text": *sayFirst})
		sess.SendClientContent(genai.LiveClientContentInput{Turns: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: *sayFirst}}}}, TurnComplete: &tc})
		awaitTurn()
		time.Sleep(*gap)
	}

	if *mic {
		runMic(sess, L, flag.Args())
		time.Sleep(2 * time.Second)
		return
	}
	for _, f := range flag.Args() {
		if strings.HasSuffix(f, ".txt") { // one text turn per line: the transcript our ears made
			b, err := os.ReadFile(f)
			if err != nil {
				log.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				drain()
				tc := true
				L.setMark()
				L.ev("text_turn", map[string]any{"text": line})
				sess.SendClientContent(genai.LiveClientContentInput{Turns: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: line}}}}, TurnComplete: &tc})
				if !awaitTurn() {
					break
				}
				time.Sleep(*gap)
			}
			continue
		}
		s, rate, err := readWav(f)
		if err != nil {
			log.Fatal(err)
		}
		s = resample(s, rate, 16000)
		drain()
		// 300 ms of silence before and after: what a local detector's padding would send
		pad := make([]int16, 4800)
		s = append(append(append([]int16{}, pad...), s...), pad...)
		L.ev("utterance", map[string]any{"file": filepath.Base(f), "audio_s": float64(len(s)) / 16000})
		if *manual {
			sess.SendRealtimeInput(genai.LiveRealtimeInput{ActivityStart: &genai.ActivityStart{}})
		}
		tick := time.NewTicker(100 * time.Millisecond)
		for i := 0; i < len(s); i += 1600 {
			<-tick.C
			sess.SendRealtimeInput(genai.LiveRealtimeInput{Audio: &genai.Blob{Data: pcm(s[i:min(len(s), i+1600)]), MIMEType: "audio/pcm;rate=16000"}})
		}
		tick.Stop()
		if *manual {
			sess.SendRealtimeInput(genai.LiveRealtimeInput{ActivityEnd: &genai.ActivityEnd{}})
		} else {
			sess.SendRealtimeInput(genai.LiveRealtimeInput{AudioStreamEnd: true})
		}
		L.setMark()
		L.ev("activity_end", nil)
		if !awaitTurn() {
			break
		}
		time.Sleep(*gap)
	}
	time.Sleep(500 * time.Millisecond)
	if player != nil {
		player.Close()
	}
}
