package dialog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/audio"
	"github.com/ibnHatab/fsttm-live/internal/live"
	"github.com/ibnHatab/fsttm/go/pkg/fsm"
)

type fakeEars struct {
	begun []int
	audio int
}

func (f *fakeEars) Begin(seq int) { f.begun = append(f.begun, seq) }
func (f *fakeEars) Audio([]byte)  { f.audio++ }
func (f *fakeEars) End()          {}

type fakeMind struct {
	turns     []string
	cancelled int
	closed    int
}

func (f *fakeMind) Turn(_ context.Context, t string) (int, error) {
	f.turns = append(f.turns, t)
	return len(f.turns), nil
}
func (f *fakeMind) Cancel(int)      { f.cancelled++ }
func (f *fakeMind) Close()          { f.closed++ }
func (f *fakeMind) Context(string)  {}
func (f *fakeMind) Connected() bool { return f.closed == 0 }

type fakeSpk struct {
	open    bool
	written int
	cuts    int
	ducked  bool
}

func (f *fakeSpk) Duck(on bool) { f.ducked = on }

func (f *fakeSpk) Write(p []byte) { f.open = true; f.written += len(p) }
func (f *fakeSpk) Finish() bool   { o := f.open; f.open = false; return o }
func (f *fakeSpk) Cut() (time.Duration, time.Duration) {
	f.cuts++
	f.open = false
	return time.Second, 2 * time.Second
}

type rig struct {
	e    *Engine
	ears *fakeEars
	mind *fakeMind
	spk  *fakeSpk
	evs  []string
}

func newRig(t *testing.T, cfg Config) *rig {
	r := &rig{ears: &fakeEars{}, mind: &fakeMind{}, spk: &fakeSpk{}}
	r.e = New(cfg, r.ears, r.mind, r.spk, nil, func(k string, f map[string]any) {
		r.evs = append(r.evs, k)
		t.Logf("%s %v", k, f)
	})
	r.e.ctx = context.Background()
	return r
}

func (r *rig) utter(text string) {
	r.e.onVad(audio.Event{Kind: audio.Start})
	seq := r.e.seq
	r.e.onHeard(live.Heard{Seq: seq, Text: text})
	r.e.onVad(audio.Event{Kind: audio.End, Voiced: 600 * time.Millisecond, Pitched: 500 * time.Millisecond})
	r.e.onHeard(live.Heard{Seq: seq, Text: text, Final: true})
}

// reply plays a whole reply from the mind and the speaker's end.
func (r *rig) reply(said string) {
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindSaid, Text: said})
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindAudio, PCM: make([]byte, 4800)})
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindDone})
	r.e.onSpkDone(true)
}

func (r *rig) has(ev string) bool {
	for _, e := range r.evs {
		if e == ev {
			return true
		}
	}
	return false
}

func TestWakeTurnReply(t *testing.T) {
	r := newRig(t, Config{FollowUp: 10 * time.Second, StartAsleep: true})
	r.utter("turn on the lab ventilation") // asleep, no name
	if len(r.mind.turns) != 0 || !r.has("asleep_ignored") {
		t.Fatalf("asleep: turns %v", r.mind.turns)
	}
	r.utter("Hey Nina, are you there?")
	if len(r.mind.turns) != 1 || r.e.Turn.State != fsm.System {
		t.Fatalf("wake: turns %v floor %s", r.mind.turns, r.e.Turn.State)
	}
	r.reply("Yes, I'm here.")
	if r.e.Turn.State != fsm.FreeS || r.e.replying || !r.e.Attn.Awake() {
		t.Fatalf("after reply: floor %s replying %v", r.e.Turn.State, r.e.replying)
	}
	r.utter("turn on the lab ventilation") // follow-up without the name
	if len(r.mind.turns) != 2 {
		t.Fatalf("follow-up not passed: %v", r.mind.turns)
	}
}

func TestEchoIsDropped(t *testing.T) {
	r := newRig(t, Config{StartAsleep: false})
	r.utter("Nina, what's on in the lab?")
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindSaid, Text: "The lab ventilation is on, in night mode."})
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindAudio, PCM: make([]byte, 4800)})
	r.utter("The lab ventilation is on in night mode") // her own voice through the mic
	if r.spk.cuts != 0 || len(r.mind.turns) != 1 || !r.has("echo") {
		t.Fatalf("echo taken as a turn: cuts %d turns %v", r.spk.cuts, r.mind.turns)
	}
	if r.e.Turn.State != fsm.System {
		t.Fatalf("echo moved the floor: %s", r.e.Turn.State)
	}
}

func TestBargeIn(t *testing.T) {
	r := newRig(t, Config{FollowUp: 10 * time.Second})
	r.utter("Nina, tell me about the ventilation")
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindSaid, Text: "The lab unit is a Prana recuperator that swaps"})
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindAudio, PCM: make([]byte, 4800)})
	r.e.onVad(audio.Event{Kind: audio.Start})
	seq := r.e.seq
	r.e.onHeard(live.Heard{Seq: seq, Text: "The lab unit"}) // echo so far
	if r.spk.cuts != 0 {
		t.Fatal("cut on her own words")
	}
	r.e.onHeard(live.Heard{Seq: seq, Text: "The lab unit stop"})
	if r.spk.cuts != 1 || r.mind.cancelled != 1 || r.e.Turn.State != fsm.User {
		t.Fatalf("barge-in: cuts %d floor %s", r.spk.cuts, r.e.Turn.State)
	}
	r.e.onVad(audio.Event{Kind: audio.End})
	if r.e.Turn.State != fsm.FreeU {
		t.Fatalf("after the end: %s", r.e.Turn.State)
	}
	r.e.onHeard(live.Heard{Seq: seq, Text: "Stop, which one is on?", Final: true})
	if len(r.mind.turns) != 2 || !strings.HasPrefix(r.mind.turns[1], "(You were interrupted") {
		t.Fatalf("turn after barge-in: %q", r.mind.turns)
	}
	r.e.onSpkDone(false) // the cut player exiting must not release the new reply
	if !r.e.replying || r.e.Turn.State != fsm.System {
		t.Fatalf("stale speaker end released the floor: %s", r.e.Turn.State)
	}
}

func TestAsideAfterFollowUp(t *testing.T) {
	r := newRig(t, Config{FollowUp: time.Second})
	r.utter("Nina, hi")
	r.reply("Hi!")
	r.e.lastSpoke = time.Now().Add(-5 * time.Second)
	r.utter("I told him the meeting moved to Thursday")
	if len(r.mind.turns) != 1 || !r.has("aside") {
		t.Fatalf("aside passed on: %v", r.mind.turns)
	}
}

func TestSleep(t *testing.T) {
	r := newRig(t, Config{FollowUp: 10 * time.Second})
	r.utter("Nina, go to sleep")
	r.reply("Okay, call me when you need me.")
	if r.e.Attn.Awake() {
		t.Fatal("still awake")
	}
	r.e.awakeChanged()
	if r.mind.closed != 1 {
		t.Fatal("asleep, but her session stays open")
	}
	r.utter("turn on the lab ventilation")
	if len(r.mind.turns) != 1 {
		t.Fatalf("heard while asleep: %v", r.mind.turns)
	}
}

func TestNotes(t *testing.T) {
	r := newRig(t, Config{Knob: Important})
	r.e.onNote(Note{Level: Info, Text: "CO2 is 600 ppm"})
	r.e.onNote(Note{Level: Important, Text: "the bedroom is getting stuffy"})
	r.e.onVad(audio.Event{Kind: audio.Start}) // the person speaks: the note waits
	r.e.tryNote()
	if len(r.mind.turns) != 0 {
		t.Fatal("note cut in on the person")
	}
	r.e.onVad(audio.Event{Kind: audio.End})
	r.e.onHeard(live.Heard{Seq: r.e.seq, Final: true}) // nothing said
	r.e.tryNote()
	if len(r.mind.turns) != 1 || r.mind.turns[0] != "NOTE (important): the bedroom is getting stuffy" {
		t.Fatalf("notes: %v", r.mind.turns)
	}
	if r.e.Turn.State != fsm.System {
		t.Fatalf("floor: %s", r.e.Turn.State)
	}
}

func TestNovel(t *testing.T) {
	said := "The lab ventilation is on, in night mode."
	if n := Novel("the lab ventilaton is on", said); len(n) != 0 {
		t.Fatalf("typo counted: %v", n)
	}
	if n := Novel("turn it off please", said); !BargeIn(n) {
		t.Fatalf("not a barge-in: %v", n)
	}
	if HeardPart("one two three four", 0.5) != "one two …" {
		t.Fatal(HeardPart("one two three four", 0.5))
	}
}

func TestControls(t *testing.T) {
	r := newRig(t, Config{FollowUp: time.Second, StartAsleep: true})
	r.e.Talk() // the button: awake, the next utterance addressed without her name
	r.utter("turn on the lab ventilation")
	if len(r.mind.turns) != 1 {
		t.Fatalf("talk button: %v", r.mind.turns)
	}
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindAudio, PCM: make([]byte, 4800)})
	r.e.Talk() // while she speaks: stop
	if r.spk.cuts != 1 || r.e.replying || !r.e.isFree() {
		t.Fatalf("talk while speaking: cuts %d floor %s", r.spk.cuts, r.e.Turn.State)
	}
	r.e.SetKnob(Off)
	r.e.onNote(Note{Level: Urgent, Text: "smoke"})
	if len(r.e.pending) != 0 {
		t.Fatal("knob off, a note queued")
	}
	r.e.SetSilent(true)
	r.utter("Nina, what time is it")
	r.reply("It's noon.")
	if r.spk.written != 4800 || r.e.replying {
		t.Fatalf("silent: written %d replying %v", r.spk.written, r.e.replying)
	}
	r.e.Sleep()
	if r.e.Attn.Awake() || r.mind.closed != 1 {
		t.Fatalf("sleep: awake %v closed %d", r.e.Attn.Awake(), r.mind.closed)
	}
}

func TestReplyIntoOpenUtterance(t *testing.T) {
	r := newRig(t, Config{FollowUp: 10 * time.Second})
	r.e.onVad(audio.Event{Kind: audio.Start}) // the room: an utterance opens
	seq := r.e.seq
	r.e.onTyped("Nina, what is the bedroom unit doing?") // she takes the floor while it is open
	r.reply("It's still running. Anything you'd like to change?")
	r.e.onVad(audio.Event{Kind: audio.End})
	r.e.onHeard(live.Heard{Seq: seq, Text: "It's still running. Anything you'd like to change?", Final: true})
	if len(r.mind.turns) != 1 {
		t.Fatalf("her echo became a turn: %v", r.mind.turns)
	}
	if BargeIn(Novel("the lab unit for Nina", "The lab unit is a Prana")) {
		t.Fatal("filler and her name barged in")
	}
}

func TestEchoJoinedAndPrevious(t *testing.T) {
	if BargeIn(Novel("Okay, good night.", "Okay, goodnight.")) {
		t.Fatal("good night is goodnight")
	}
	r := newRig(t, Config{FollowUp: 10 * time.Second})
	r.utter("Nina, what happened?")
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindSaid, Text: "I've set the lab ventilation to night mode for you."})
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindAudio, PCM: make([]byte, 4800)})
	r.e.onVad(audio.Event{Kind: audio.Start}) // her voice, tentative
	seq := r.e.seq
	r.e.onTyped("Nina, what did the rules do?") // cuts her, a new reply begins with the utterance open
	r.e.onVad(audio.Event{Kind: audio.End})
	r.e.onHeard(live.Heard{Seq: seq, Text: "I've set the lab ventilation to night mode.", Final: true})
	if len(r.mind.turns) != 2 {
		t.Fatalf("the cut reply's echo became a turn: %q", r.mind.turns)
	}
}

func TestNotesTogether(t *testing.T) {
	r := newRig(t, Config{Knob: Info})
	r.e.onNote(Note{Level: Info, Text: "the bedroom light was already off"})
	r.e.onNote(Note{Level: Important, Text: "the lab ventilation is in night mode"})
	r.e.tryNote()
	if len(r.mind.turns) != 1 || !strings.HasPrefix(r.mind.turns[0], "NOTE (important): the bedroom light") {
		t.Fatalf("%q", r.mind.turns)
	}
}

// the person answers while her question is still playing: one utterance, her voice then theirs
func TestAnswerOverHerTail(t *testing.T) {
	r := newRig(t, Config{Outlast: 50 * time.Millisecond})
	r.utter("Nina, what is the status of my home ventilation?")
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindSaid, Text: "Are you thinking about the lab or the bedroom?"})
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindAudio, PCM: make([]byte, 4800)})
	r.e.onVad(audio.Event{Kind: audio.Start}) // in her voice: tentative
	seq := r.e.seq
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindDone})
	r.e.onSpkDone(true)
	time.Sleep(80 * time.Millisecond) // the person goes on after her
	r.e.onVad(audio.Event{Kind: audio.End})
	r.e.onHeard(live.Heard{Seq: seq, Text: "the bedroom? In the lab.", Final: true})
	if len(r.mind.turns) != 2 || r.mind.turns[1] != "in the lab" {
		t.Fatalf("the answer was lost: %q", r.mind.turns)
	}
}

func TestStripEcho(t *testing.T) {
	for _, c := range [][3]string{
		{"the bedroom? In the lab.", "Are you thinking about the lab or the bedroom?", "in the lab"},
		{"In the lab.", "Are you thinking about the lab or the bedroom?", "In the lab."},
		{"about the lab or the bedroom", "Are you thinking about the lab or the bedroom?", ""},
		{"No, just boost it.", "Would you like me to boost it, or switch it back to normal?", "No, just boost it."},
		{"Okay, now switch to normal.", "Okay, I've boosted the lab ventilation.", "Okay, now switch to normal."},
		{"switch it back to normal? No, just boost it.", "Would you like me to boost it, or switch it back to normal?", "no just boost it"},
		{", I've stopped the ventilation for you.", "No problem, I've stopped the ventilation for you.", ""},
	} {
		if got := StripEcho(c[0], c[1]); got != c[2] {
			t.Errorf("StripEcho(%q) = %q, want %q", c[0], got, c[2])
		}
	}
}

func TestLateEchoAndSleepInConversation(t *testing.T) {
	r := newRig(t, Config{FollowUp: 30 * time.Second})
	r.utter("Nina, please stop ventilation.")
	r.reply("No problem, I've stopped the ventilation for you.")
	r.utter(", I've stopped the ventilation for you.") // her echo's tail, late
	if len(r.mind.turns) != 1 || !r.has("late_echo") {
		t.Fatalf("late echo taken: %q", r.mind.turns)
	}
	r.utter("No, go to sleep.") // in the conversation, without her name
	r.reply("Okay, call me when you need me.")
	if r.e.Attn.Awake() {
		t.Fatalf("still awake after go to sleep")
	}
}

func TestPhantomName(t *testing.T) {
	r := newRig(t, Config{StartAsleep: true})
	say := func(text string, voiced time.Duration) {
		r.e.onVad(audio.Event{Kind: audio.Start})
		seq := r.e.seq
		r.e.onVad(audio.Event{Kind: audio.End, Voiced: voiced, Pitched: voiced * 8 / 10})
		r.e.onHeard(live.Heard{Seq: seq, Text: text, Final: true})
	}
	say("Nina.", 120*time.Millisecond)  // a knock
	say("Nina.", 3700*time.Millisecond) // speech it could not make out
	if len(r.mind.turns) != 0 || r.e.Attn.Awake() || !r.has("phantom_name") {
		t.Fatalf("a phantom name woke her: %v", r.mind.turns)
	}
	r.e.onVad(audio.Event{Kind: audio.Start}) // clicks: 1.4 s voiced, no pitch
	r.e.onVad(audio.Event{Kind: audio.End, Voiced: 1400 * time.Millisecond})
	r.e.onHeard(live.Heard{Seq: r.e.seq, Text: "Nina.", Final: true})
	if r.e.Attn.Awake() {
		t.Fatalf("clicks woke her")
	}
	say("Hey Nina.", 560*time.Millisecond)
	if len(r.mind.turns) != 1 {
		t.Fatalf("a real call missed: %v", r.mind.turns)
	}
	r.reply("Yes?")
	say("Nina, turn off the lab ventilation.", 3*time.Second) // more than the name: not checked
	if len(r.mind.turns) != 2 {
		t.Fatalf("a command missed: %v", r.mind.turns)
	}
}

type fakeSpotter struct{ on map[string]bool } // a keyword when the PCM is one of these

func (f *fakeSpotter) Begin() {}
func (f *fakeSpotter) Feed(p []byte) string {
	if f.on[string(p)] {
		return "NINA"
	}
	return ""
}
func (f *fakeSpotter) End() string { return "" }

// asleep with the local wake word: only an utterance with her name reaches the transcriber,
// from its start; awake, all of them
func TestLocalWake(t *testing.T) {
	sp := &fakeSpotter{on: map[string]bool{"nina": true}}
	r := newRig(t, Config{StartAsleep: true, Wake: "local", Spotter: sp})
	r.e.onVad(audio.Event{Kind: audio.Start, PCM: []byte("room")})
	r.e.onVad(audio.Event{Kind: audio.Audio, PCM: []byte("talk")})
	r.e.onVad(audio.Event{Kind: audio.End})
	if len(r.ears.begun) != 0 || r.ears.audio != 0 || !r.has("asleep_local") {
		t.Fatalf("room talk sent: begun %v audio %d", r.ears.begun, r.ears.audio)
	}
	r.e.onVad(audio.Event{Kind: audio.Start, PCM: []byte("pre")})
	r.e.onVad(audio.Event{Kind: audio.Audio, PCM: []byte("x")})
	r.e.onVad(audio.Event{Kind: audio.Audio, PCM: []byte("nina")})
	if len(r.ears.begun) != 1 || r.ears.audio != 3 || !r.has("kws") {
		t.Fatalf("her name: begun %v audio %d (want the pre-roll and all)", r.ears.begun, r.ears.audio)
	}
	r.e.onVad(audio.Event{Kind: audio.Audio, PCM: []byte("turn on the lab")})
	r.e.onVad(audio.Event{Kind: audio.End, Voiced: time.Second, Pitched: time.Second})
	r.e.onHeard(live.Heard{Seq: r.e.seq, Text: "Nina, turn on the lab.", Final: true})
	if len(r.mind.turns) != 1 || r.ears.audio != 4 {
		t.Fatalf("the call: turns %v audio %d", r.mind.turns, r.ears.audio)
	}
	r.reply("Done.")
	r.e.onVad(audio.Event{Kind: audio.Start, PCM: []byte("awake")}) // awake: streamed
	if len(r.ears.begun) != 2 {
		t.Fatalf("awake, not streamed: %v", r.ears.begun)
	}
}

// A voice over hers (Barge): she ducks at once; her own echo's words bring her back; a person's
// words cut her.
func TestBargeDuck(t *testing.T) {
	r := newRig(t, Config{})
	r.utter("Nina, tell me about the moon.")
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindSaid, Text: "The moon is reaching perigee today, its closest point to Earth."})
	r.e.onMind(live.MindEvent{Turn: r.e.turnID, Kind: live.MindAudio, PCM: make([]byte, 4800)})
	r.e.onVad(audio.Event{Kind: audio.Start})
	r.e.onVad(audio.Event{Kind: audio.Barge})
	if !r.spk.ducked {
		t.Fatal("not ducked on a voice over hers")
	}
	seq := r.e.seq
	r.e.onVad(audio.Event{Kind: audio.End})
	r.e.onHeard(live.Heard{Seq: seq, Text: "its closest point", Final: true})
	if r.spk.ducked {
		t.Fatal("still ducked after her own echo")
	}
	r.e.onVad(audio.Event{Kind: audio.Start})
	r.e.onVad(audio.Event{Kind: audio.Barge})
	seq = r.e.seq
	r.e.onVad(audio.Event{Kind: audio.End})
	r.e.onHeard(live.Heard{Seq: seq, Text: "wait, stop, what about the weather", Final: true})
	if r.spk.cuts != 1 {
		t.Fatalf("a barge-in in words did not cut her: %d cuts", r.spk.cuts)
	}
}

// Her name alone (the spotter's hit, a name's length of voice): she wakes at the utterance's end,
// not when the transcriber writes "Nina" (~1.5 s later); that text is then dropped, and words
// beyond the name are still a turn.
func TestBareName(t *testing.T) {
	sp := &fakeSpotter{on: map[string]bool{"nina": true}}
	r := newRig(t, Config{StartAsleep: true, Wake: "local", Spotter: sp})
	r.e.onVad(audio.Event{Kind: audio.Start, PCM: []byte("pre")})
	r.e.onVad(audio.Event{Kind: audio.Audio, PCM: []byte("nina")})
	r.e.onVad(audio.Event{Kind: audio.End, Voiced: 500 * time.Millisecond, Pitched: 400 * time.Millisecond})
	if len(r.mind.turns) != 1 || !r.has("bare_name") {
		t.Fatalf("not woken at the end: turns %v", r.mind.turns)
	}
	r.e.onHeard(live.Heard{Seq: r.e.seq, Text: "Hey, Nina.", Final: true})
	if len(r.mind.turns) != 1 {
		t.Fatalf("the name's text made a second turn: %v", r.mind.turns)
	}
	r.reply("Yes?")
	// a longer one: the transcriber's text decides, as before
	r2 := newRig(t, Config{StartAsleep: true, Wake: "local", Spotter: sp})
	r2.e.onVad(audio.Event{Kind: audio.Start, PCM: []byte("pre")})
	r2.e.onVad(audio.Event{Kind: audio.Audio, PCM: []byte("nina")})
	r2.e.onVad(audio.Event{Kind: audio.End, Voiced: 2 * time.Second, Pitched: 2 * time.Second})
	if len(r2.mind.turns) != 0 || r2.has("bare_name") {
		t.Fatalf("a sentence taken as a bare name: %v", r2.mind.turns)
	}
	r2.e.onHeard(live.Heard{Seq: r2.e.seq, Text: "Nina, what time is it?", Final: true})
	if len(r2.mind.turns) != 1 {
		t.Fatalf("the sentence lost: %v", r2.mind.turns)
	}
}
