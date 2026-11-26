// Package dialog is the floor: FSTTM's turn-taking machine (N09-1071) and attention layer,
// driven by our endpointer, the ears' text and the voice session. It alone decides who holds
// the floor, whether an utterance was meant for Nina, and when a NOTE from SOURCES may be
// spoken. The models are never asked.
//
//	VAD start ─▶ user G      FREE → USER          (or, while Nina holds the floor: tentative)
//	VAD end   ─▶ user R      USER → FREEu
//	final text, addressed ─▶ system G  FREEu → SYSTEM, the turn goes to the voice
//	reply played ─▶ system R  SYSTEM → FREEs
//	novel words while SYSTEM ─▶ barge-in: cut locally, user G (BOTHs), system R (USER)
package dialog

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/audio"
	"github.com/ibnHatab/fsttm-live/internal/live"
	"github.com/ibnHatab/fsttm/go/pkg/attention"
	"github.com/ibnHatab/fsttm/go/pkg/fsm"
)

type Ears interface {
	Begin(seq int)
	Audio(pcm []byte)
	End()
}

type Mind interface {
	Turn(ctx context.Context, text string) (int, error)
	Cancel(turn int)
	Context(line string) // silent: she knows it, with her next turn
	Close()              // asleep: the Live API has no sleep, so the session is closed (it resumes on the next turn)
	Connected() bool
}

type Speaker interface {
	Write(pcm []byte)
	Finish() bool
	Cut() (heard, written time.Duration)
}

type TTS interface {
	Say(text string) ([]byte, error)
}

// Level of a NOTE from SOURCES (the narration port's quantisation).
type Level int

const (
	Info Level = iota
	Important
	Urgent
	Off // as a knob: no NOTE is spoken
)

func (l Level) String() string { return [...]string{"info", "important", "urgent", "off"}[l] }

func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(s) {
	case "info":
		return Info, true
	case "important":
		return Important, true
	case "urgent":
		return Urgent, true
	case "off":
		return Off, true
	}
	return Info, false
}

type Note struct {
	Level Level
	Text  string
}

type Config struct {
	// FollowUp: while awake, an utterance without Nina's name is meant for her only this long
	// after she last spoke (a conversation's next turn); 0 takes every utterance while awake.
	FollowUp     time.Duration
	AwakeFor     time.Duration // asleep again after this long without a turn
	ReplyTimeout time.Duration // no audio this long after a turn: the floor is released
	Knob         Level         // the lowest NOTE level spoken while awake (urgent also asleep; Off: none)
	NoteGap      time.Duration // a NOTE waits until the floor has been free this long
	LocalVoice   bool          // speak the mind's transcript with TTS, not its audio
	Silent       bool          // her sound off: replies are shown, not played
	StartAsleep  bool
	// Outlast: an utterance that began in her voice and ended this long after her reply did is
	// the person's (they answered over her tail): its words after her echo are a turn.
	Outlast time.Duration
	// Wake: who hears her name. "" or "cloud": the transcriber, every utterance streamed;
	// "shadow": the transcriber, and the local spotter alongside (its hits logged: a test);
	// "local": asleep, an utterance reaches the transcriber only once the spotter heard her
	// name in it (the room's talk stays on the machine).
	Wake    string
	Spotter Spotter
	// MindLinger: asleep, her session stays open this long before it is closed (a wake soon after
	// needs no new one: a session's setup took 0.5 s, now and then 5.4 s); 0: closed at once.
	// Only text turns go over it: nothing of the room.
	MindLinger time.Duration
}

// Spotter is the local wake word (kws): an utterance's audio in, a keyword's label out.
type Spotter interface {
	Begin()
	Feed(pcm []byte) string
	End() string
}

// Spotters: several spotters, the first one's keyword (openWakeWord hears a bare name warm, sherpa's
// spotter her name before a sentence: 2026-11-26, pero, "Nina, what time is it?" 0.02 to the first).
type Spotters []Spotter

func (s Spotters) Begin() {
	for _, k := range s {
		k.Begin()
	}
}

func (s Spotters) Feed(pcm []byte) string {
	hit := ""
	for _, k := range s {
		if h := k.Feed(pcm); hit == "" {
			hit = h
		}
	}
	return hit
}

func (s Spotters) End() string {
	hit := ""
	for _, k := range s {
		if h := k.End(); hit == "" {
			hit = h
		}
	}
	return hit
}

type Engine struct {
	cfg  Config
	Turn *fsm.Model
	Attn *attention.Attention
	ears Ears
	mind Mind
	spk  Speaker
	tts  TTS
	Log  func(string, map[string]any)

	Vad     chan audio.Event
	Heard   chan live.Heard
	MindEv  chan live.MindEvent
	SpkDone chan bool
	Notes   chan Note
	Typed   chan string
	Ctl     chan func(*Engine) // a plugin's action, run in the dialog's goroutine

	ctx context.Context

	// the utterance being heard
	seq     int
	open    bool
	uttT    time.Time
	outlast map[int]bool // tentative utterances that went on after her reply ended
	kwsHit  string       // the spotter's keyword in the open utterance
	holding bool         // asleep, local wake: the open utterance's audio is held, not sent
	held    [][]byte
	voiced  map[int]time.Duration
	pitched map[int]time.Duration
	uttEnd  time.Time
	tent    map[int]bool   // seq → started while Nina held the floor (echo until proven otherwise)
	linger  int            // the asleep close armed last (a wake disarms it)
	told    bool           // asleep because she was told (a sleep phrase, Sleep): the attention event says so
	after   string         // words that came with a bare name while she answered it: her next turn
	bare    map[int]bool   // seq → her name alone, woken to at its end (its transcript adds only the rest)
	echoOf  map[int]string // seq → what Nina was saying when its barge-in was confirmed
	interim string

	// the reply
	replying  bool // a turn is out to the mind and its reply not yet played
	turnID    int  // the mind's id of that turn
	mindDone  bool
	audioOut  bool
	sentT     time.Time
	firstT    time.Time
	said      strings.Builder
	prevSaid  string          // the reply before: a cut reply's tail may still be in the room
	localBuf  strings.Builder // LocalVoice: text not yet spoken
	cutNote   string
	lastSpoke time.Time
	freeT     time.Time // when the floor last became free

	pending   []Note
	awake     bool
	addressed time.Time // until then an utterance is meant for her without her name (a button's talk)
	status    atomic.Value

	idle     atomic.Bool  // for the test driver: the floor free, nothing heard or said
	speaking atomic.Int64 // unix ms when the current reply's audio started, 0 if none
	duckSeq  int          // the utterance her voice is ducked for (Barge), 0 none
}

func New(cfg Config, ears Ears, mind Mind, spk Speaker, tts TTS, log func(string, map[string]any)) *Engine {
	e := &Engine{cfg: cfg, ears: ears, mind: mind, spk: spk, tts: tts, Log: log,
		Turn:    fsm.New(),
		Attn:    attention.New(true, nil, nil, cfg.StartAsleep),
		Vad:     make(chan audio.Event, 256),
		Heard:   make(chan live.Heard, 256),
		MindEv:  make(chan live.MindEvent, 1024),
		SpkDone: make(chan bool, 8),
		Notes:   make(chan Note, 64),
		Typed:   make(chan string, 16),
		Ctl:     make(chan func(*Engine), 16),
		tent:    map[int]bool{},
		bare:    map[int]bool{},
		echoOf:  map[int]string{},
		outlast: map[int]bool{},
		voiced:  map[int]time.Duration{},
		pitched: map[int]time.Duration{},
		freeT:   time.Now(),
	}
	e.Attn.AwakeFor = cfg.AwakeFor
	e.awake = e.Attn.Awake()
	e.status.Store(Status{Floor: string(e.Turn.State), Awake: e.awake})
	e.Turn.OnChange = func(ev string, src, dst fsm.State) {
		e.Log("floor", map[string]any{"event": ev, "from": string(src), "to": string(dst)})
		if dst == fsm.FreeU || dst == fsm.FreeS {
			e.freeT = time.Now()
		}
	}
	return e
}

// Idle: the floor is free and nothing is being heard or said (the test driver waits on it).
func (e *Engine) Idle() bool { return e.idle.Load() }

// SpeakingSince: when the current reply's audio started (zero if Nina is silent).
func (e *Engine) SpeakingSince() time.Time {
	if ms := e.speaking.Load(); ms != 0 {
		return time.UnixMilli(ms)
	}
	return time.Time{}
}

func (e *Engine) Run(ctx context.Context) {
	e.ctx = ctx
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		e.tryNote()
		e.idle.Store(!e.replying && !e.open && e.isFree())
		e.status.Store(Status{Floor: string(e.Turn.State), Awake: e.Attn.Peek(), Hearing: e.open,
			Replying: e.replying, Speaking: e.speaking.Load() != 0, Mind: e.mind.Connected(), Pending: len(e.pending)})
		select {
		case <-ctx.Done():
			return
		case f := <-e.Ctl:
			f(e)
		case ev := <-e.Vad:
			e.onVad(ev)
		case h := <-e.Heard:
			e.onHeard(h)
		case m := <-e.MindEv:
			e.onMind(m)
		case ok := <-e.SpkDone:
			e.onSpkDone(ok)
		case n := <-e.Notes:
			e.onNote(n)
		case t := <-e.Typed:
			e.onTyped(t)
		case <-tick.C:
			e.onTick()
		}
	}
}

func (e *Engine) isFree() bool { return e.Turn.State == fsm.FreeU || e.Turn.State == fsm.FreeS }

// ---- hearing -------------------------------------------------------------------------

func (e *Engine) onVad(ev audio.Event) {
	switch ev.Kind {
	case audio.Start:
		e.seq++
		e.open, e.uttT, e.interim = true, time.Now(), ""
		if e.replying {
			e.tent[e.seq] = true // Nina holds the floor: her own voice until words say otherwise
		} else {
			_ = e.Turn.UserAction('G')
		}
		e.kwsHit, e.holding, e.held = "", false, nil
		if k := e.cfg.Spotter; k != nil {
			k.Begin()
			e.spotted(k.Feed(ev.PCM))
		}
		if e.cfg.Wake == "local" && e.cfg.Spotter != nil && !e.Attn.Awake() && e.kwsHit == "" {
			e.holding, e.held = true, [][]byte{ev.PCM}
		} else {
			e.ears.Begin(e.seq)
			e.ears.Audio(ev.PCM)
		}
		e.Log("vad_start", map[string]any{"seq": e.seq, "tentative": e.tent[e.seq], "floor": string(e.Turn.State), "ref": ev.Ref})
	case audio.Audio:
		if !e.open {
			return
		}
		if k := e.cfg.Spotter; k != nil && e.kwsHit == "" {
			e.spotted(k.Feed(ev.PCM))
		}
		if e.holding {
			e.held = append(e.held, ev.PCM)
			if e.kwsHit != "" {
				e.release()
			}
		} else {
			e.ears.Audio(ev.PCM)
		}
	case audio.Barge: // a voice over hers, above her echo: she yields at once (ducks), the words decide
		if !e.firstT.IsZero() && time.Since(e.firstT) < bargeGrace {
			// her reply's first moments: a software canceller re-learns her echo after a silence (pero,
			// 2026-11-26: -32 dBFS in a reply's first 0.5 s, then under -55): no telling it from a person
			e.Log("barge_onset_ignored", map[string]any{"seq": e.seq, "since_audio_ms": time.Since(e.firstT).Milliseconds()})
			return
		}
		if d, ok := e.spk.(interface{ Duck(bool) }); ok && e.open && e.tent[e.seq] && e.speaking.Load() != 0 {
			d.Duck(true)
			e.duckSeq = e.seq
			e.Log("barge_duck", map[string]any{"seq": e.seq, "ref": ev.Ref})
		}
	case audio.End:
		if !e.open {
			return
		}
		if e.duckSeq == e.seq { // her voice back if the words do not say otherwise soon
			seq := e.seq
			time.AfterFunc(2500*time.Millisecond, func() { e.Ctl <- func(e *Engine) { e.unduck(seq, "timeout") } })
		}
		if k := e.cfg.Spotter; k != nil {
			if hit := k.End(); e.kwsHit == "" {
				e.spotted(hit)
			}
		}
		if e.kwsHit != "" && e.cfg.Wake == "local" && !e.Attn.Awake() && !e.tent[e.seq] &&
			ev.Voiced >= minNameVoiced && ev.Voiced <= maxBareName && ev.Pitched >= minNamePitched {
			// her name alone (the spotter's, a name's length of voice), asleep: she wakes at its end,
			// not when the transcriber has written it (a bare "Nina" it finalises ~1.5 s after the
			// end, waiting for more); its text, when it comes, adds only what is not the name
			e.bare[e.seq] = true
			e.Log("bare_name", map[string]any{"seq": e.seq, "voiced_s": ev.Voiced.Seconds(), "keyword": e.kwsHit})
		}
		switch {
		case e.holding && e.kwsHit != "":
			e.release()
			e.ears.End()
		case e.holding:
			// asleep and no name heard: nothing of it leaves the machine
			e.holding, e.held = false, nil
			e.Log("asleep_local", map[string]any{"seq": e.seq, "voiced_s": ev.Voiced.Seconds(), "pitched_s": ev.Pitched.Seconds()})
		default:
			e.ears.End()
		}
		e.open, e.uttEnd = false, time.Now()
		e.voiced[e.seq], e.pitched[e.seq] = ev.Voiced, ev.Pitched
		delete(e.voiced, e.seq-20)
		delete(e.pitched, e.seq-20)
		if e.tent[e.seq] && !e.replying && e.cfg.Outlast > 0 && e.lastSpoke.After(e.uttT) &&
			time.Since(e.lastSpoke) >= e.cfg.Outlast {
			e.outlast[e.seq] = true
		}
		if !e.tent[e.seq] {
			_ = e.Turn.UserAction('R')
		}
		e.Log("vad_end", map[string]any{"seq": e.seq, "dur_s": ev.Dur.Seconds(), "voiced_s": ev.Voiced.Seconds(), "pitched_s": ev.Pitched.Seconds(),
			"forced": ev.Forced, "ref": ev.Ref, "aec_ref": ev.Ref > 0})
		if e.bare[e.seq] {
			// what it means, for her: after a "go to sleep" a bare "Nina" was taken for more of the
			// goodbye ("Okay, call me when you need me" again; pero, 2026-11-26)
			e.cutNote = "(You were asleep; the person has just called your name to wake you. Say in a word or two that you are listening.)\n"
			e.dispatch(bareName, false) // the floor is free now: her turn
		}
	}
}

func (e *Engine) onHeard(h live.Heard) {
	if !h.Final {
		if h.Seq == e.seq {
			e.interim = h.Text
		}
		if !e.tent[h.Seq] {
			e.Log("interim", map[string]any{"seq": h.Seq, "text": h.Text})
		}
		if e.tent[h.Seq] {
			e.checkBarge(h.Seq, h.Text)
		}
		return
	}
	since := time.Duration(0)
	if !e.uttEnd.IsZero() {
		since = time.Since(e.uttEnd)
	}
	e.Log("heard", map[string]any{"seq": h.Seq, "text": h.Text, "late": h.Late, "after_end_ms": since.Milliseconds(),
		"tentative": e.tent[h.Seq], "interim": h.Interim})
	if h.Late {
		return // the turn was taken (or dropped) on the final; logged for the measurements
	}
	if e.tent[h.Seq] {
		e.checkBarge(h.Seq, h.Text)
		if e.tent[h.Seq] {
			delete(e.tent, h.Seq)
			out := e.outlast[h.Seq]
			delete(e.outlast, h.Seq)
			if rest := StripEcho(h.Text, e.said.String()+" "+e.prevSaid); out && rest != "" {
				e.Log("outlasted", map[string]any{"seq": h.Seq, "heard": h.Text, "kept": rest})
				if !e.phantomName(h.Seq, rest) {
					e.dispatch(rest, false)
				}
				return
			}
			e.Log("echo", map[string]any{"seq": h.Seq, "text": h.Text})
			e.unduck(h.Seq, "echo")
			return
		}
	}
	delete(e.tent, h.Seq)
	delete(e.outlast, h.Seq)
	text := h.Text
	if e.bare[h.Seq] {
		delete(e.bare, h.Seq)
		var rest []string
		for _, w := range words(text) {
			if ok, _ := e.Attn.MatchWake(w); !ok && !greeting[w] {
				rest = append(rest, w)
			}
		}
		if len(rest) == 0 {
			return // the name she already woke to
		}
		text = strings.Join(rest, " ")
		if e.replying { // her "Yes?" is under way: the words are her next turn, not dropped as busy
			e.after = text
			e.Log("bare_rest_queued", map[string]any{"seq": h.Seq, "text": text})
			return
		}
	}
	if said, ok := e.echoOf[h.Seq]; ok {
		// a barge-in over her voice (or an utterance she spoke into): only the words she was
		// not saying are the person's
		delete(e.echoOf, h.Seq)
		if said == "" {
			said = e.said.String() + " " + e.prevSaid
			if !BargeIn(Novel(text, said)) {
				e.Log("echo", map[string]any{"seq": h.Seq, "text": text, "overlap": true})
				return
			}
		}
		text = StripEcho(text, said)
		e.Log("echo_stripped", map[string]any{"seq": h.Seq, "heard": h.Text, "kept": text})
	}
	if strings.TrimSpace(text) == "" || e.phantomName(h.Seq, text) {
		return
	}
	if !e.replying && !e.lastSpoke.IsZero() && time.Since(e.lastSpoke) < lateEcho && len(words(text)) >= 3 &&
		StripEcho(text, e.said.String()+" "+e.prevSaid) == "" {
		// her reply's words, in her order, just after it: its echo, cut into the next utterance
		// by the transcriber (its text arrives late; this utterance's own sound was a click)
		e.Log("late_echo", map[string]any{"seq": h.Seq, "text": text})
		return
	}
	e.dispatch(text, false)
}

// phantomName: her name alone ("Nina.", "Hey Nina.") from a sound too short or too long to be
// someone saying it. The transcriber's vocabulary bias writes "Nina" for a knock, a cough or
// speech it can't make out; a spoken name has 0.3–0.7 s of voice, most of it with a pitch.
func (e *Engine) phantomName(seq int, text string) bool {
	var rest []string
	for _, w := range words(text) {
		if !greeting[w] {
			rest = append(rest, w)
		}
	}
	if len(rest) != 1 {
		return false
	}
	if ok, _ := e.Attn.MatchWake(rest[0]); !ok {
		return false
	}
	v, ok := e.voiced[seq]
	pv := e.pitched[seq]
	if !ok || (v >= minNameVoiced && v <= maxNameVoiced && pv >= minNamePitched) {
		return false
	}
	e.Log("phantom_name", map[string]any{"seq": seq, "text": text, "voiced_s": v.Seconds(), "pitched_s": pv.Seconds()})
	return true
}

// spotted logs the spotter's keyword in the open utterance (the first).
func (e *Engine) spotted(hit string) {
	if hit == "" || e.kwsHit != "" {
		return
	}
	e.kwsHit = hit
	e.Log("kws", map[string]any{"seq": e.seq, "keyword": hit, "held": e.holding, "since_start_ms": time.Since(e.uttT).Milliseconds()})
	if w, ok := e.mind.(interface{ Warm(context.Context) }); ok && e.holding {
		w.Warm(e.ctx) // her session opens while the utterance ends (it had closed asleep: ~0.5 s, now and then 5.4)
		if e.cfg.MindLinger > 0 {
			e.closeAsleep() // a name the words then deny: closed again later
		}
	}
}

// release: her name was heard in a held utterance: it goes to the transcriber from its start.
func (e *Engine) release() {
	e.ears.Begin(e.seq)
	for _, p := range e.held {
		e.ears.Audio(p)
	}
	e.holding, e.held = false, nil
}

const lateEcho = 10 * time.Second

const minNameVoiced, maxNameVoiced = 250 * time.Millisecond, 2 * time.Second

// maxBareName: the voice of a name said alone ("Nina", "Hey Nina"); longer, the words go to the transcriber
// bargeGrace: no duck this long after her reply's first audio (her echo while the canceller adapts)
const bargeGrace = 700 * time.Millisecond

const maxBareName = 800 * time.Millisecond // names alone: 0.34-0.56 s; "Nina, what time is it?" from Paradee 1.0 s

// bareName: what a bare name is taken to have said (she wakes on it)
const bareName = "Nina"

// a spoken "Nina" is voiced throughout (n-i-n-a): ~0.4 s with a pitch; clicks have none
const minNamePitched = 150 * time.Millisecond

var greeting = map[string]bool{"hey": true, "hi": true, "hello": true, "ok": true, "okay": true, "oh": true}

// checkBarge: words in a tentative utterance that Nina is not saying are the person's.
func (e *Engine) checkBarge(seq int, text string) {
	novel := Novel(text, e.said.String()+" "+e.prevSaid)
	if !BargeIn(novel) {
		return
	}
	delete(e.tent, seq)
	e.echoOf[seq] = e.said.String() + " " + e.prevSaid
	e.Log("barge_in", map[string]any{"seq": seq, "heard": text, "novel": novel, "replying": e.replying})
	if e.replying {
		e.cut()
	} else if seq == e.seq && e.open {
		_ = e.Turn.UserAction('G') // she had finished: the utterance is simply the person's
	}
	if seq == e.seq && !e.open {
		_ = e.Turn.UserAction('R') // it has already ended
	}
}

// cut is the local barge-in: the speaker stops now, the rest of the turn is dropped, the floor
// goes to the person (SYSTEM →(K,G) BOTHs →(R,K) USER), and the next turn tells the mind
// what was heard of its reply.
// unduck brings her voice back after a duck for seq that was not a barge-in.
func (e *Engine) unduck(seq int, why string) {
	if e.duckSeq != seq || seq == 0 {
		return
	}
	e.duckSeq = 0
	if d, ok := e.spk.(interface{ Duck(bool) }); ok {
		d.Duck(false)
		e.Log("barge_unduck", map[string]any{"seq": seq, "why": why})
	}
}

func (e *Engine) cut() {
	e.duckSeq = 0
	heard, written := e.spk.Cut()
	e.mind.Cancel(e.turnID)
	frac := 0.0
	if written > 0 {
		frac = heard.Seconds() / written.Seconds()
	}
	said := strings.TrimSpace(e.said.String())
	if part := HeardPart(said, frac); said != "" {
		e.cutNote = fmt.Sprintf("(You were interrupted; the person heard only: \"%s\")\n", part)
	}
	e.Log("cut", map[string]any{"heard_s": heard.Seconds(), "written_s": written.Seconds(), "said": said})
	e.replying, e.mindDone = false, false
	e.speaking.Store(0)
	_ = e.Turn.UserAction('G')
	_ = e.Turn.SystemAction('R')
	e.lastSpoke = time.Now()
}

// dispatch is the attention gate, then the turn. typed lines skip the name check.
func (e *Engine) dispatch(text string, typed bool) {
	if !e.Attn.Awake() {
		if ok, _ := e.Attn.MatchWake(text); !ok && !typed {
			e.Log("asleep_ignored", map[string]any{"text": text})
			return
		}
		e.Attn.OnUtterance(text)
		e.awakeChanged()
		e.send(text)
		return
	}
	named, _ := e.Attn.MatchWake(text)
	followUp := e.cfg.FollowUp == 0 || (!e.lastSpoke.IsZero() && time.Since(e.lastSpoke) < e.cfg.FollowUp) ||
		time.Now().Before(e.addressed)
	if !named && !followUp && !typed {
		e.Log("aside", map[string]any{"text": text, "since_spoke_s": time.Since(e.lastSpoke).Seconds()})
		return
	}
	// a sleep phrase meant for her ("go to sleep" with her name, or in the conversation) puts
	// her to sleep after this reply; attention takes it only with the name, so it is given
	if !named {
		e.Attn.OnUtterance("nina " + text)
	} else {
		e.Attn.OnUtterance(text)
	}
	e.told = e.awake && !e.Attn.Awake() // a sleep phrase (her screens close what they show; a timeout leaves it)
	e.send(text)                        // first: a sleep phrase's goodbye is a reply, and her session stays for it
	e.awakeChanged()                    // closed after the goodbye has played (finishReply)
}

// send gives the floor to SYSTEM and the turn to the mind.
func (e *Engine) send(text string) {
	if e.replying {
		e.Log("busy", map[string]any{"text": text})
		return
	}
	if err := e.Turn.SystemAction('G'); err != nil {
		e.Log("floor_refused", map[string]any{"err": err.Error(), "text": text})
		return
	}
	note := e.cutNote // for her, not the person's words: the log's "context", not its "text" (her screens draw the text)
	turn := note + text
	e.cutNote = ""
	e.beginReply()
	id, err := e.mind.Turn(e.ctx, turn)
	if err != nil {
		e.Log("mind_error", map[string]any{"err": err.Error()})
		e.say("Sorry, I can't reach my voice right now.")
		return
	}
	e.turnID = id
	d := map[string]any{"turn": id, "text": text, "after_end_ms": time.Since(e.uttEnd).Milliseconds()}
	if note != "" {
		d["context"] = strings.TrimSpace(note)
	}
	e.Log("turn", d)
}

func (e *Engine) beginReply() {
	if e.open && !e.tent[e.seq] {
		// an utterance still open when she takes the floor will carry her voice: what it
		// heard is checked against what she said, like a barge-in's
		e.echoOf[e.seq] = ""
	}
	e.replying, e.mindDone, e.audioOut = true, false, false
	e.sentT, e.firstT = time.Now(), time.Time{}
	if s := strings.TrimSpace(e.said.String()); s != "" {
		e.prevSaid = s
	}
	e.said.Reset()
	e.localBuf.Reset()
}

// say is a local phrase (TTS), used when the mind cannot be reached.
func (e *Engine) say(text string) {
	if e.tts == nil {
		e.finishReply()
		return
	}
	pcm, err := e.tts.Say(text)
	if err != nil {
		e.Log("tts_error", map[string]any{"err": err.Error()})
		e.finishReply()
		return
	}
	e.said.WriteString(text)
	e.out(pcm)
	e.mindDone = true
	if !e.spk.Finish() {
		e.finishReply()
	}
}

func (e *Engine) out(pcm []byte) {
	if e.cfg.Silent {
		return // shown, not played: the reply ends when the mind's turn does
	}
	if !e.audioOut {
		e.audioOut, e.firstT = true, time.Now()
		e.speaking.Store(e.firstT.UnixMilli())
		e.Log("first_audio", map[string]any{"after_turn_ms": e.firstT.Sub(e.sentT).Milliseconds(),
			"after_end_ms": e.firstT.Sub(e.uttEnd).Milliseconds()})
	}
	e.spk.Write(pcm)
}

// ---- the voice -----------------------------------------------------------------------

func (e *Engine) onMind(m live.MindEvent) {
	if !e.replying || m.Turn != e.turnID {
		return // an earlier turn's tail
	}
	switch m.Kind {
	case live.MindAudio:
		if !e.cfg.LocalVoice {
			e.out(m.PCM)
		}
	case live.MindSaid:
		e.said.WriteString(m.Text)
		e.Log("said", map[string]any{"text": m.Text})
		if e.cfg.LocalVoice {
			e.localBuf.WriteString(m.Text)
			e.speakLocal(false)
		}
	case live.MindTool:
		e.Log("mind_tool", map[string]any{"name": m.Text})
	case live.MindInterrupted:
		e.Log("mind_interrupted", nil)
	case live.MindDone, live.MindClosed:
		if m.Kind == live.MindClosed {
			e.Log("mind_closed", map[string]any{"err": m.Text})
		}
		if e.cfg.LocalVoice {
			e.speakLocal(true)
		}
		e.mindDone = true
		if !e.spk.Finish() {
			e.finishReply() // nothing was played
		}
	}
}

// speakLocal sends whole sentences of the transcript to TTS (all of it when flush).
func (e *Engine) speakLocal(flush bool) {
	s := e.localBuf.String()
	cut := strings.LastIndexAny(s, ".?!")
	if flush {
		cut = len(s) - 1
	}
	if cut < 0 || strings.TrimSpace(s[:cut+1]) == "" {
		return
	}
	text := s[:cut+1]
	e.localBuf.Reset()
	e.localBuf.WriteString(s[cut+1:])
	pcm, err := e.tts.Say(strings.TrimSpace(text))
	if err != nil {
		e.Log("tts_error", map[string]any{"err": err.Error()})
		return
	}
	e.out(pcm)
}

func (e *Engine) onSpkDone(played bool) {
	if !e.replying || !e.mindDone {
		return // a cut reply's player exiting
	}
	e.finishReply()
}

// finishReply: the reply has been played; SYSTEM releases the floor.
func (e *Engine) finishReply() {
	e.replying = false
	e.speaking.Store(0)
	_ = e.Turn.SystemAction('R')
	e.Attn.Touch()
	e.lastSpoke = time.Now()
	total := time.Since(e.sentT)
	e.Log("reply_done", map[string]any{"said": strings.TrimSpace(e.said.String()), "audio": e.audioOut, "total_s": total.Seconds()})
	e.awakeChanged()
	if t := e.after; t != "" {
		e.after = ""
		e.dispatch(t, false)
	}
}

// ---- notes from SOURCES ----------------------------------------------------------------

func (e *Engine) onNote(n Note) {
	if e.cfg.Knob == Off || (n.Level < Urgent && (n.Level < e.cfg.Knob || !e.Attn.Awake())) {
		e.Log("note_filtered", map[string]any{"level": n.Level.String(), "text": n.Text, "awake": e.Attn.Awake()})
		e.mind.Context(n.Text) // not spoken, still known
		return
	}
	e.pending = append(e.pending, n)
	e.Log("note_queued", map[string]any{"level": n.Level.String(), "text": n.Text})
}

// tryNote: a NOTE takes a free floor (transition 5, G from FREE), never the person's. An
// urgent one goes as soon as the floor is free; the others wait NoteGap, the person's chance
// to go on.
func (e *Engine) tryNote() {
	if len(e.pending) == 0 || e.replying || e.open || !e.isFree() {
		return
	}
	n := e.pending[0]
	for _, p := range e.pending[1:] {
		n.Level = max(n.Level, p.Level)
	}
	if n.Level < Urgent && time.Since(e.freeT) < e.cfg.NoteGap {
		return
	}
	// what came together is said together: one turn for all that waits
	if len(e.pending) > 1 {
		var ts []string
		for _, p := range e.pending {
			ts = append(ts, p.Text)
		}
		n.Text = strings.Join(ts, "\n")
	}
	e.pending = nil
	if err := e.Turn.SystemAction('G'); err != nil {
		return
	}
	e.beginReply()
	turn := fmt.Sprintf("NOTE (%s): %s", n.Level, n.Text)
	id, err := e.mind.Turn(e.ctx, turn)
	if err != nil {
		e.Log("mind_error", map[string]any{"err": err.Error()})
		e.finishReply()
		return
	}
	e.turnID = id
	e.uttEnd = time.Now()
	e.Log("turn", map[string]any{"turn": id, "text": turn, "note": true})
}

// ---- typed lines, the clock -------------------------------------------------------------

// onTyped: "/note LEVEL text" is a NOTE; "/say text" is spoken verbatim; anything else is the
// person's words (meant for Nina).
func (e *Engine) onTyped(t string) {
	t = strings.TrimSpace(t)
	switch {
	case t == "":
	case strings.HasPrefix(t, "/note "):
		f := strings.SplitN(t, " ", 3)
		lv, ok := ParseLevel(f[1])
		if !ok || len(f) < 3 {
			e.Log("typed_error", map[string]any{"line": t})
			return
		}
		e.onNote(Note{Level: lv, Text: f[2]})
	case strings.HasPrefix(t, "/say "):
		e.Wake() // her narration: the person may answer it, by voice
		e.send("SAY: " + strings.TrimPrefix(t, "/say "))
	default:
		if e.replying {
			e.cut()
		}
		if e.Turn.State != fsm.User {
			_ = e.Turn.UserAction('G')
		}
		_ = e.Turn.UserAction('R')
		e.uttEnd = time.Now()
		e.Log("typed", map[string]any{"text": t})
		e.Wake() // typing to her is talking to her (the user, 2026-11-24): awake for the follow-up, by voice too
		e.dispatch(t, true)
	}
}

func (e *Engine) onTick() {
	if e.replying && !e.audioOut && e.cfg.ReplyTimeout > 0 && time.Since(e.sentT) > e.cfg.ReplyTimeout {
		e.Log("no_reply", map[string]any{"waited_s": time.Since(e.sentT).Seconds()})
		e.mind.Cancel(e.turnID)
		e.finishReply()
	}
	e.awakeChanged()
}

func (e *Engine) awakeChanged() {
	if a := e.Attn.Awake(); a != e.awake {
		e.awake = a
		d := map[string]any{"awake": a}
		if !a && e.told {
			d["told"] = true
		}
		e.Log("attention", d)
	}
	e.told = false
	if e.awake {
		e.linger++ // a close armed while asleep: not any more
		return
	}
	if !e.replying {
		e.closeAsleep()
	}
}

// closeAsleep: asleep and her goodbye played: her session is closed (the Live API has no sleep),
// after cfg.MindLinger; a wake or an urgent NOTE resumes it with its handle, the conversation's
// context intact.
func (e *Engine) closeAsleep() {
	e.linger++
	n := e.linger
	shut := func(e *Engine) {
		if n == e.linger && !e.Attn.Awake() && !e.replying && e.mind.Connected() {
			e.mind.Close()
			e.Log("mind_closed_asleep", nil)
		}
	}
	if e.cfg.MindLinger <= 0 {
		shut(e)
		return
	}
	time.AfterFunc(e.cfg.MindLinger, func() { e.Ctl <- shut })
}

// ---- control (plugins) ----------------------------------------------------------------

// Status is the snapshot plugins read (any goroutine).
type Status struct {
	Floor    string
	Awake    bool
	Hearing  bool
	Replying bool
	Speaking bool
	Mind     bool
	Pending  int
}

func (e *Engine) Status() Status { return e.status.Load().(Status) }

// Do runs f in the dialog's goroutine and waits for it.
func (e *Engine) Do(f func(*Engine)) {
	done := make(chan struct{})
	e.Ctl <- func(e *Engine) { f(e); close(done) }
	<-done
}

// Wake: awake, and the next utterance is meant for her without her name.
func (e *Engine) Wake() {
	if !e.Attn.Awake() {
		e.Attn.OnUtterance("nina") // the wake word, as if said
	}
	e.Attn.Touch()
	e.addressed = time.Now().Add(max(e.cfg.FollowUp, 8*time.Second))
	e.Log("wake", nil)
	e.awakeChanged()
}

// Sleep: what she says is cut, attention asleep, her session closed.
func (e *Engine) Sleep() {
	if e.replying {
		e.cut()
		_ = e.Turn.UserAction('R')
	}
	e.told = e.awake
	e.Attn.State = attention.Asleep
	e.addressed = time.Time{}
	e.Log("sleep", nil)
	e.awakeChanged()
}

// Stop cuts what she is saying; the floor goes free.
func (e *Engine) Stop() {
	if !e.replying {
		return
	}
	e.cut()
	e.cutNote = "" // stopped on purpose: nothing to explain to her
	_ = e.Turn.UserAction('R')
	e.Log("stop", nil)
}

// Talk is a button: stop her if she speaks, else wake her.
func (e *Engine) Talk() {
	if e.replying {
		e.Stop()
		return
	}
	e.Wake()
}

func (e *Engine) SetSilent(on bool) {
	if on && e.replying && e.audioOut {
		e.Stop()
	}
	e.cfg.Silent = on
}

func (e *Engine) SetKnob(l Level) { e.cfg.Knob = l }

// SetLocal switches the voice between the mind's audio (tts nil) and a local TTS.
func (e *Engine) SetLocal(tts TTS) {
	e.tts = tts
	e.cfg.LocalVoice = tts != nil
}

func (e *Engine) Note(n Note)       { e.onNote(n) }
func (e *Engine) TypeLine(t string) { e.onTyped(t) }
