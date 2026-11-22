// Package live holds the two Gemini Live sessions: the ears (a transcription session under
// our endpointing) and the voice (a native-audio session fed text turns).
package live

import (
	"context"
	"os"
	"strings"
	"time"

	"google.golang.org/genai"
)

// NewClient reads the AI Studio key from a file (never from a config) and opens a v1beta
// client, the version the Live API needs.
func NewClient(ctx context.Context, keyFile string) (*genai.Client, error) {
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	return genai.NewClient(ctx, &genai.ClientConfig{APIKey: strings.TrimSpace(string(key)),
		Backend: genai.BackendGeminiAPI, HTTPOptions: genai.HTTPOptions{APIVersion: "v1beta"}})
}

// Heard is the ears' text for one utterance (Seq, given at Begin). Interim texts come while
// it is spoken; Final once after its end; Late if more text came after the final.
type Heard struct {
	Seq   int
	Text  string
	Final bool
	Late  bool
}

// Ears is gemini-3.5-transcribe-live in manual activity: we send activityStart, the
// utterance's audio and activityEnd. The model only sends interim transcripts, accumulated
// over the whole session without separators, so the ears cut each utterance's part out of it
// and decide when it is final: after the end, at sentence punctuation that held for Settle,
// or at FinalWait with whatever there is.
type Ears struct {
	Client    *genai.Client
	Model     string
	Vocab     []string
	Settle    time.Duration
	FinalWait time.Duration
	MaxAge    time.Duration // reconnect between utterances after this (sessions last 10 min)
	Out       chan<- Heard
	Log       func(string, map[string]any)

	cmds chan earsCmd
}

type earsCmd struct {
	kind int // 0 begin, 1 audio, 2 end
	seq  int
	pcm  []byte
}

type earsText struct {
	gen  int
	text string
	err  error
}

func (e *Ears) Begin(seq int)    { e.cmds <- earsCmd{kind: 0, seq: seq} }
func (e *Ears) Audio(pcm []byte) { e.cmds <- earsCmd{kind: 1, pcm: pcm} }
func (e *Ears) End()             { e.cmds <- earsCmd{kind: 2} }

// Close ends the session (the mic is off); the next utterance opens a new one.
func (e *Ears) Close() { e.cmds <- earsCmd{kind: 3} }

func (e *Ears) Start(ctx context.Context) {
	e.cmds = make(chan earsCmd, 1024)
	go e.loop(ctx)
}

func (e *Ears) connect(ctx context.Context, gen int, texts chan<- earsText) (*genai.Session, error) {
	cfg := &genai.LiveConnectConfig{
		ResponseModalities:      []genai.Modality{genai.ModalityText},
		InputAudioTranscription: &genai.AudioTranscriptionConfig{CustomVocabulary: e.Vocab},
		RealtimeInputConfig: &genai.RealtimeInputConfig{
			AutomaticActivityDetection: &genai.AutomaticActivityDetection{Disabled: true}},
	}
	t := time.Now()
	s, err := e.Client.Live.Connect(ctx, e.Model, cfg)
	if err != nil {
		return nil, err
	}
	e.Log("ears_connected", map[string]any{"ms": time.Since(t).Milliseconds(), "model": e.Model})
	go func() {
		for {
			m, err := s.Receive()
			if err != nil {
				texts <- earsText{gen: gen, err: err}
				return
			}
			if m.GoAway != nil {
				e.Log("ears_go_away", map[string]any{"left": m.GoAway.TimeLeft.String()})
			}
			sc := m.ServerContent
			if sc == nil {
				continue
			}
			for _, tr := range []*genai.Transcription{sc.InterimInputTranscription, sc.InputTranscription} {
				if tr != nil && tr.Text != "" {
					texts <- earsText{gen: gen, text: tr.Text}
				}
			}
		}
	}()
	return s, nil
}

func (e *Ears) loop(ctx context.Context) {
	texts := make(chan earsText, 256)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var (
		sess       *genai.Session
		gen        int
		born       time.Time
		full, base string // the session's accumulated text; its length at this utterance's start
		seq        = -1
		open       bool // between Begin and End
		ended      time.Time
		changed    time.Time
		final      string
		finalSent  bool
		lastSeg    string
	)
	seg := func() string {
		if strings.HasPrefix(full, base) {
			return strings.TrimSpace(full[len(base):])
		}
		n := 0
		for n < len(full) && n < len(base) && full[n] == base[n] {
			n++
		}
		return strings.TrimSpace(full[n:])
	}
	ensure := func() bool {
		if sess != nil && time.Since(born) < e.MaxAge {
			return true
		}
		if sess != nil {
			sess.Close()
		}
		gen++
		s, err := e.connect(ctx, gen, texts)
		if err != nil {
			e.Log("ears_error", map[string]any{"err": err.Error()})
			sess = nil
			return false
		}
		sess, born, full, base = s, time.Now(), "", ""
		return true
	}
	emitFinal := func() {
		final, finalSent = seg(), true
		e.Out <- Heard{Seq: seq, Text: final, Final: true}
	}
	for {
		select {
		case <-ctx.Done():
			if sess != nil {
				sess.Close()
			}
			return
		case c := <-e.cmds:
			switch c.kind {
			case 0:
				if open && !finalSent {
					emitFinal()
				}
				if !ensure() {
					seq, open, finalSent = c.seq, false, true
					e.Out <- Heard{Seq: c.seq, Final: true} // nothing will be heard
					continue
				}
				seq, open, finalSent, lastSeg, base = c.seq, true, false, "", full
				ended, changed = time.Time{}, time.Now()
				sess.SendRealtimeInput(genai.LiveRealtimeInput{ActivityStart: &genai.ActivityStart{}})
			case 1:
				if open && sess != nil {
					sess.SendRealtimeInput(genai.LiveRealtimeInput{Audio: &genai.Blob{Data: c.pcm, MIMEType: "audio/pcm;rate=16000"}})
				}
			case 2:
				if open && sess != nil {
					sess.SendRealtimeInput(genai.LiveRealtimeInput{ActivityEnd: &genai.ActivityEnd{}})
				}
				open, ended = false, time.Now()
			case 3:
				if open && !finalSent {
					emitFinal()
				}
				if sess != nil {
					gen++ // its receiver's error is not news
					sess.Close()
					sess = nil
					e.Log("ears_session_closed", nil)
				}
				open = false
			}
		case t := <-texts:
			if t.gen != gen {
				continue
			}
			if t.err != nil {
				e.Log("ears_closed", map[string]any{"err": t.err.Error(), "age_s": time.Since(born).Seconds()})
				sess = nil
				if !finalSent && seq >= 0 {
					emitFinal()
				}
				continue
			}
			full = t.text
			s := seg()
			if s == lastSeg {
				continue
			}
			lastSeg, changed = s, time.Now()
			switch {
			case !finalSent:
				e.Out <- Heard{Seq: seq, Text: s}
			case s != final:
				final = s
				e.Out <- Heard{Seq: seq, Text: s, Final: true, Late: true}
			}
		case <-tick.C:
			if finalSent || seq < 0 || ended.IsZero() || open {
				continue
			}
			s := seg()
			// a sentence that ended and held still; text from before our end may lack its tail
			settled := s != "" && strings.ContainsAny(s[len(s)-1:], ".?!") && time.Since(changed) >= e.Settle &&
				(changed.After(ended) || time.Since(ended) >= 2*e.Settle)
			if settled || time.Since(ended) >= e.FinalWait {
				emitFinal()
			}
		}
	}
}
