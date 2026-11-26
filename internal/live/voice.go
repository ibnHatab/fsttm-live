package live

import (
	"context"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
)

// MindEvent is what the voice session gives back for one turn.
type MindKind int

const (
	MindAudio       MindKind = iota // PCM: 24 kHz s16le mono
	MindSaid                        // Text: a piece of the output transcript
	MindDone                        // the turn is complete
	MindInterrupted                 // the server interrupted the turn (a newer turn arrived)
	MindTool                        // Text: a tool call, for the log
	MindClosed                      // the session ended (it resumes on the next turn)
)

type MindEvent struct {
	Turn int // the id Turn returned
	Kind MindKind
	PCM  []byte
	Text string
}

// ToolFunc answers one function call (synchronous tools: 3.1 Flash Live waits for them).
type ToolFunc func(ctx context.Context, args map[string]any) map[string]any

// Now is the clock her turns carry (a test sets it).
var Now = time.Now

// Voice is the voice+mind: a native-audio Live session given text turns only (what the ears
// heard and our attention passed, or a NOTE from SOURCES). Every event carries the id of the
// turn it belongs to: the server answers turns in order and closes each with turnComplete
// (a turn sent while the previous is still generating interrupts it, which closes it too).
// Cancel stops delivering a turn's output (the speaker was cut); its tail is discarded here.
// The session resumes with its handle when it ends, so the conversation's context survives.
type Voice struct {
	Client    *genai.Client
	Model     string
	VoiceName string
	System    string
	Tools     []*genai.Tool
	Funcs     map[string]ToolFunc
	Compress  int64 // context tokens that trigger the sliding window (0: off)
	Out       chan<- MindEvent
	Log       func(string, map[string]any)

	mu     sync.Mutex
	sess   *genai.Session
	gen    int
	handle string
	goAway bool
	nextID int
	open   []*turnRec // sent and not yet closed, oldest first
	ctxq   []ctxLine  // what SOURCES reported since the last turn: sent with the next one
	// warming: a session being opened by Warm (closed when it is in place or failed); the lock is
	// not held while it dials (the dialog must not wait on the network: pero, 2026-11-26, 5.4 s)
	warming chan struct{}
}

type turnRec struct {
	id        int
	cancelled bool
}

func (v *Voice) config() *genai.LiveConnectConfig {
	cfg := &genai.LiveConnectConfig{
		ResponseModalities:       []genai.Modality{genai.ModalityAudio},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
		SystemInstruction:        &genai.Content{Parts: []*genai.Part{{Text: v.System}}},
		Tools:                    v.Tools,
		SessionResumption:        &genai.SessionResumptionConfig{Handle: v.handle},
	}
	if v.Compress > 0 {
		tr, tg := v.Compress, v.Compress/2
		cfg.ContextWindowCompression = &genai.ContextWindowCompressionConfig{TriggerTokens: &tr,
			SlidingWindow: &genai.SlidingWindow{TargetTokens: &tg}}
	}
	if v.VoiceName != "" {
		cfg.SpeechConfig = &genai.SpeechConfig{VoiceConfig: &genai.VoiceConfig{
			PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: v.VoiceName}}}
	}
	return cfg
}

// connect opens the session (or resumes it). Called with v.mu held.
func (v *Voice) connect(ctx context.Context) error {
	t := time.Now()
	s, err := v.Client.Live.Connect(ctx, v.Model, v.config())
	if err != nil && v.handle != "" {
		// the handle expired (or the setup changed too much to resume): a fresh session
		v.Log("voice_resume_failed", map[string]any{"err": err.Error()})
		v.handle = ""
		s, err = v.Client.Live.Connect(ctx, v.Model, v.config())
	}
	if err != nil {
		return err
	}
	v.gen++
	v.sess, v.goAway, v.open = s, false, nil
	v.Log("voice_connected", map[string]any{"ms": time.Since(t).Milliseconds(), "model": v.Model, "resumed": v.handle != ""})
	go v.receive(ctx, s, v.gen)
	return nil
}

// Warm opens the session ahead of a turn (her name heard: the turn follows in a second), in the
// background and without holding the lock while it dials; Turn waits for it. A session already
// open, or being opened, is left as it is.
func (v *Voice) Warm(ctx context.Context) {
	v.mu.Lock()
	if v.sess != nil || v.warming != nil {
		v.mu.Unlock()
		return
	}
	done := make(chan struct{})
	v.warming = done
	cfg, resumed := v.config(), v.handle != ""
	v.mu.Unlock()
	go func() {
		defer close(done)
		t := time.Now()
		s, err := v.Client.Live.Connect(ctx, v.Model, cfg)
		if err != nil && resumed {
			v.Log("voice_resume_failed", map[string]any{"err": err.Error(), "warm": true})
			v.mu.Lock()
			v.handle = ""
			cfg, resumed = v.config(), false
			v.mu.Unlock()
			s, err = v.Client.Live.Connect(ctx, v.Model, cfg)
		}
		v.mu.Lock()
		defer v.mu.Unlock()
		v.warming = nil
		if err != nil {
			v.Log("voice_warm_failed", map[string]any{"err": err.Error()})
			return
		}
		if v.sess != nil { // opened meanwhile: this one is not needed
			s.Close()
			return
		}
		v.gen++
		v.sess, v.goAway, v.open = s, false, nil
		v.Log("voice_connected", map[string]any{"ms": time.Since(t).Milliseconds(), "model": v.Model, "resumed": resumed, "warm": true})
		go v.receive(ctx, s, v.gen)
	}()
}

func (v *Voice) Connect(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.connect(ctx)
}

// Turn sends one text turn and returns its id.
func (v *Voice) Turn(ctx context.Context, text string) (int, error) {
	for { // a session Warm is opening: wait for it, then use it
		v.mu.Lock()
		w := v.warming
		if w == nil {
			break
		}
		v.mu.Unlock()
		<-w
	}
	defer v.mu.Unlock()
	// a session told to go away is replaced between turns (the handle keeps the context)
	if v.sess == nil || (v.goAway && len(v.open) == 0) {
		if v.sess != nil {
			v.sess.Close()
			v.sess = nil
		}
		if err := v.connect(ctx); err != nil {
			return 0, err
		}
	}
	v.nextID++
	tc := true
	turns := []*genai.Content{}
	// the clock, with every turn: she had none (2026-11-26, pero: "what time is it?" was a web
	// search answered in UTC; a timer's time left reckoned wrong)
	now := "NOW (the local time, for you; say it only when it is asked for or needed): " + Now().Format("Monday, 2 January 2006, 15:04 MST")
	if len(v.ctxq) > 0 {
		// silent context: what the home's systems reported meanwhile, ahead of the turn
		var ls []string
		for _, c := range v.ctxq {
			ls = append(ls, c.text)
		}
		turns = append(turns, &genai.Content{Role: "user", Parts: []*genai.Part{{Text: now + "\nHOME (reported by the home's systems since your last turn; for you, not to announce):\n" + strings.Join(ls, "\n")}}})
		v.Log("voice_context", map[string]any{"lines": len(v.ctxq)})
		v.ctxq = nil
	} else {
		turns = append(turns, &genai.Content{Role: "user", Parts: []*genai.Part{{Text: now}}})
	}
	turns = append(turns, &genai.Content{Role: "user", Parts: []*genai.Part{{Text: text}}})
	if err := v.sess.SendClientContent(genai.LiveClientContentInput{Turns: turns, TurnComplete: &tc}); err != nil {
		return 0, err
	}
	v.open = append(v.open, &turnRec{id: v.nextID})
	return v.nextID, nil
}

// Cancel stops delivering the output of turn id (the speaker was cut).
func (v *Voice) Cancel(id int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, t := range v.open {
		if t.id == id {
			t.cancelled = true
		}
	}
}

func (v *Voice) receive(ctx context.Context, s *genai.Session, gen int) {
	for {
		m, err := s.Receive()
		v.mu.Lock()
		if gen != v.gen {
			v.mu.Unlock()
			return
		}
		var evs []MindEvent
		if err != nil {
			v.sess = nil
			v.Log("voice_closed", map[string]any{"err": err.Error(), "open_turns": len(v.open)})
			for _, t := range v.open {
				if !t.cancelled {
					evs = append(evs, MindEvent{Turn: t.id, Kind: MindClosed, Text: err.Error()})
				}
			}
			v.open = nil
			v.mu.Unlock()
			for _, e := range evs {
				v.Out <- e
			}
			return
		}
		if u := m.SessionResumptionUpdate; u != nil && u.Resumable && u.NewHandle != "" {
			v.handle = u.NewHandle
		}
		if m.GoAway != nil {
			v.goAway = true
			v.Log("voice_go_away", map[string]any{"left": m.GoAway.TimeLeft.String()})
		}
		var cur *turnRec
		if len(v.open) > 0 {
			cur = v.open[0]
		}
		deliver := func(e MindEvent) {
			if cur != nil && !cur.cancelled {
				e.Turn = cur.id
				evs = append(evs, e)
			}
		}
		if tc := m.ToolCall; tc != nil {
			for _, fc := range tc.FunctionCalls {
				deliver(MindEvent{Kind: MindTool, Text: fc.Name})
				go v.callTool(ctx, s, fc) // answered even for a cancelled turn: it was asked
			}
		}
		if m.UsageMetadata != nil {
			v.Log("voice_usage", map[string]any{"prompt": m.UsageMetadata.PromptTokenCount, "total": m.UsageMetadata.TotalTokenCount})
		}
		if sc := m.ServerContent; sc != nil {
			if sc.ModelTurn != nil {
				for _, p := range sc.ModelTurn.Parts {
					if p.InlineData != nil && len(p.InlineData.Data) > 0 {
						deliver(MindEvent{Kind: MindAudio, PCM: p.InlineData.Data})
					}
				}
			}
			if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
				deliver(MindEvent{Kind: MindSaid, Text: sc.OutputTranscription.Text})
			}
			if sc.Interrupted {
				deliver(MindEvent{Kind: MindInterrupted})
			}
			if sc.TurnComplete {
				deliver(MindEvent{Kind: MindDone})
				if cur != nil {
					v.Log("voice_turn_closed", map[string]any{"turn": cur.id, "cancelled": cur.cancelled, "interrupted": sc.Interrupted})
					v.open = v.open[1:]
				} else {
					v.Log("voice_turn_closed", map[string]any{"turn": nil})
				}
			}
		}
		v.mu.Unlock()
		for _, e := range evs {
			v.Out <- e
		}
	}
}

func (v *Voice) callTool(ctx context.Context, s *genai.Session, fc *genai.FunctionCall) {
	t := time.Now()
	var res map[string]any
	if f := v.Funcs[fc.Name]; f != nil {
		res = f(ctx, fc.Args)
	} else {
		res = map[string]any{"error": "no such tool"}
	}
	v.Log("tool", map[string]any{"name": fc.Name, "args": fc.Args, "result": res, "ms": time.Since(t).Milliseconds()})
	s.SendToolResponse(genai.LiveToolResponseInput{FunctionResponses: []*genai.FunctionResponse{
		{ID: fc.ID, Name: fc.Name, Response: res}}})
}

// Close ends the session now (asleep); the next turn resumes it with the handle.
func (v *Voice) Close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sess != nil {
		v.gen++ // its receiver exits without reporting
		v.sess.Close()
		v.sess = nil
		v.open = nil
		v.Log("voice_session_closed", map[string]any{"handle": v.handle != ""})
	}
}

func (v *Voice) Connected() bool { v.mu.Lock(); defer v.mu.Unlock(); return v.sess != nil }

// SetVoiceName takes effect at the next turn: the session is replaced between turns and
// resumed with the new voice.
func (v *Voice) SetVoiceName(name string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if name == v.VoiceName {
		return
	}
	v.VoiceName = name
	v.goAway = true
}

type ctxLine struct{ key, text string }

// Context queues a line SOURCES reported; it reaches the session with the next turn (a
// content sent alone would leave a turn open, and one sent while she speaks would cut her).
func (v *Voice) Context(line string) { v.ContextKey("", line) }

// ContextKey queues a keyed line (an entity's state): a newer one replaces the older.
func (v *Voice) ContextKey(key, line string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if key != "" {
		for i, c := range v.ctxq {
			if c.key == key {
				v.ctxq = append(v.ctxq[:i], v.ctxq[i+1:]...)
				break
			}
		}
	}
	v.ctxq = append(v.ctxq, ctxLine{key, line})
	if len(v.ctxq) > 80 {
		v.ctxq = v.ctxq[len(v.ctxq)-80:]
	}
}
