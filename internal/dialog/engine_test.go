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

type fakeEars struct{ begun []int }

func (f *fakeEars) Begin(seq int) { f.begun = append(f.begun, seq) }
func (f *fakeEars) Audio([]byte)  {}
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
}

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
	r.e.onVad(audio.Event{Kind: audio.End})
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
