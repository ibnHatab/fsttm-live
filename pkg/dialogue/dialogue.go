// Package dialogue is how Nina talks, as a library: everything built from the config (the mic,
// the endpointer, the local wake word, the two Live sessions, the speaker, the floor), the
// preferences applied, and the Host the plugins see. fsttm-live's own cmd/nina-live is one
// program on it; nina-live (the agent) embeds it as her voice channel.
//
//	cfg, _ := dialogue.LoadConfig(path)
//	d, _ := dialogue.New(ctx, cfg, dialogue.NewEvents(w), dialogue.Tools{Decls: ..., Funcs: ...})
//	d.SavePrefs = myStore            // where a preferences change goes (default: the config file)
//	go d.Run(ctx)
//	d.Context(key, line)             // silent context for the voice session (a newer line replaces the older)
//	d.Note("important", text)        // a NOTE to the floor: said when the floor is free and the knob allows
//	ch, stop := d.Subscribe()        // the dialogue's events (heard, turn, said, cut, attention, …)
package dialogue

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/audio"
	"github.com/ibnHatab/fsttm-live/internal/config"
	"github.com/ibnHatab/fsttm-live/internal/dialog"
	"github.com/ibnHatab/fsttm-live/internal/evlog"
	"github.com/ibnHatab/fsttm-live/internal/kws"
	"github.com/ibnHatab/fsttm-live/internal/live"
	"github.com/ibnHatab/fsttm-live/internal/oww"
	"github.com/ibnHatab/fsttm-live/internal/paradee"
	"github.com/ibnHatab/fsttm-live/internal/plugin"
	"github.com/ibnHatab/fsttm-live/internal/rhvoice"
	"google.golang.org/genai"
)

// LiveVoices are the Live API's prebuilt voices.
var LiveVoices = []string{"Puck", "Charon", "Kore", "Fenrir", "Aoede", "Leda", "Orus", "Zephyr",
	"Achernar", "Achird", "Algenib", "Algieba", "Alnilam", "Autonoe", "Callirrhoe", "Despina",
	"Enceladus", "Erinome", "Gacrux", "Iapetus", "Laomedeia", "Pulcherrima", "Rasalgethi",
	"Sadachbia", "Sadaltager", "Schedar", "Sulafat", "Umbriel", "Vindemiatrix", "Zubenelgenubi"}

// ToolFunc runs one of the voice session's tools: its arguments in, its result out.
type ToolFunc = live.ToolFunc

// Tools is what SOURCES offer the voice session (declarations and their functions).
type Tools struct {
	Decls []*genai.Tool
	Funcs map[string]live.ToolFunc
}

// MindSession is her mind as the dialogue holds it: Gemini Live's voice (live.Voice), or a text mind
// (live.TextMind: an OpenAI-compatible model, no voice; headless, the SITL).
type MindSession interface {
	dialog.Mind
	Connect(ctx context.Context) error
	SetVoiceName(name string)
	ContextKey(key, line string)
}

// Config is the dialogue's configuration (audio, models, dialog, prefs, plugins); Prefs the
// person's preferences in it.
type (
	Config = config.Config
	Prefs  = config.Prefs
	Events = evlog.Log
)

// LoadConfig reads a config file (the defaults, overridden by the file).
func LoadConfig(path string) (*Config, error) { return config.Load(path) }

// NewEvents is the dialogue's event log over writers (JSONL lines), with subscribers.
func NewEvents(w ...io.Writer) *Events { return evlog.New(w...) }

type Dialogue struct {
	// SavePrefs keeps a preferences change (default: written back to the config file)
	SavePrefs func(Prefs) error
	// OnDuck: her voice was ducked (a person over her) or is back; Loudness has changed with it
	OnDuck func(on bool)
	// OnAction takes the panel's actions the dialogue does not know (the application's settings);
	// false: not one of its either
	OnAction func(action, arg string) (bool, error)

	Cfg    *config.Config
	Ev     *evlog.Log
	Engine *dialog.Engine
	Mind   MindSession
	Ears   *live.Ears
	spk    *audio.Speaker

	mu    sync.Mutex
	prefs config.Prefs
	muted atomic.Bool
	// a remote microphone and voice (her screen on a phone, through /api/audio)
	remoteMic atomic.Bool
	frames    chan audio.Frame
	fmu       sync.Mutex
	fclosed   bool
	rbuf      []byte
	capt      *audio.Capture
	wake      *oww.Stream // openWakeWord, fed every frame (wake.engine: oww)
}

func New(ctx context.Context, cfg *config.Config, log *evlog.Log, tools Tools) (*Dialogue, error) {
	client, err := live.NewClient(ctx, cfg.Expand(cfg.Key))
	if err != nil {
		return nil, err
	}
	sys, err := os.ReadFile(cfg.Expand(cfg.Charter))
	if err != nil {
		return nil, err
	}
	n := &Dialogue{Cfg: cfg, Ev: log, prefs: cfg.Prefs, SavePrefs: cfg.SavePrefs}
	var voice *live.Voice
	var text *live.TextMind
	if m, ok := strings.CutPrefix(cfg.Models.Mind, "openrouter:"); ok { // a mind without a voice (headless: the SITL)
		key := cfg.Models.MindKey
		if key == "" {
			key = "~/.config/nina/openrouter.env"
		}
		text = &live.TextMind{URL: "https://openrouter.ai/api/v1/chat/completions", Model: m, KeyFile: cfg.Expand(key),
			System: string(sys), Tools: tools.Decls, Funcs: tools.Funcs, Log: log.Ev}
		n.Mind = text
	} else {
		voice = &live.Voice{Client: client, Model: cfg.Models.Mind, VoiceName: cfg.Prefs.VoiceName, System: string(sys),
			Tools: tools.Decls, Funcs: tools.Funcs, Compress: 24000, Log: log.Ev}
		n.Mind = voice
	}
	n.Ears = &live.Ears{Client: client, Model: cfg.Models.Ears, Vocab: cfg.Models.Vocab,
		Settle: 200 * time.Millisecond, FinalWait: 1500 * time.Millisecond, MaxAge: 8 * time.Minute, Log: log.Ev}
	spk := audio.NewSpeaker(cfg.Audio.Out, 24000)
	spk.Level = cfg.Audio.Level
	spk.OnDuck = func(on bool) {
		if f := n.OnDuck; f != nil {
			f(on)
		}
	}
	if cfg.Prefs.BubbleSlope > 0 {
		spk.SetSlope(cfg.Prefs.BubbleSlope)
	}
	spk.SetOffset(cfg.Prefs.BubbleOffset)
	if cfg.Prefs.Bubble > 0 {
		spk.SetBubble(cfg.Prefs.Bubble)
	}
	n.spk = spk
	knob, ok := dialog.ParseLevel(cfg.Prefs.Narration)
	if !ok {
		return nil, fmt.Errorf("prefs.narration: %q", cfg.Prefs.Narration)
	}
	n.Engine = dialog.New(dialog.Config{FollowUp: cfg.Dialog.FollowUp, AwakeFor: cfg.Dialog.AwakeFor,
		ReplyTimeout: 10 * time.Second, Knob: knob, NoteGap: cfg.Dialog.NoteGap,
		Silent: !cfg.Prefs.Voice, StartAsleep: cfg.Dialog.StartAsleep,
		Outlast: cfg.Audio.Hang + 600*time.Millisecond,
		Wake:    cfg.Dialog.Wake.Mode, Spotter: n.spotter(cfg, log.Ev), MindLinger: linger(cfg.Dialog.MindLinger)}, n.Ears, n.Mind, spk, nil, log.Ev)
	if t := localVoice(cfg.Prefs); t != nil {
		n.Engine.SetLocal(t)
	}
	if voice != nil {
		voice.Out = n.Engine.MindEv
	} else {
		text.Out = n.Engine.MindEv
	}
	n.Ears.Out = n.Engine.Heard
	log.Quiet["interim"], log.Quiet["said"] = true, true // the panel's; the file has heard and reply_done
	go func() {
		for ok := range spk.Done {
			if spk.Level != 0 { // the normaliser's own view of what it played (the live calibration reads it)
				est, target, r := spk.Stats()
				n.Ev.Ev("voice_level", map[string]any{"stream_db": math.Round(est*10) / 10, "target_db": math.Round(target*10) / 10, "radius_m": r})
			}
			n.Engine.SpkDone <- ok
		}
	}()
	n.muted.Store(!cfg.Prefs.Mic)
	return n, nil
}

func localVoice(p config.Prefs) dialog.TTS {
	switch p.Local {
	case "rhvoice":
		return rhvoice.Voice{Profile: p.RHVoice}
	case "paradee":
		return paradee.Voice{Name: p.Paradee}
	}
	return nil
}

// Run starts the sound and the sessions and runs the dialog until ctx ends.
func (n *Dialogue) Run(ctx context.Context) error {
	n.Ears.Start(ctx)
	if err := n.Mind.Connect(ctx); err != nil {
		return err
	}
	a := n.Cfg.Audio
	frames := make(chan audio.Frame, 64)
	local := make(chan audio.Frame, 64)
	n.frames = frames
	go func() { // the room's microphone, unless a remote one (her screen on a phone) is on
		for f := range local {
			if n.remoteMic.Load() {
				continue
			}
			select {
			case frames <- f:
			default:
			}
		}
		n.fmu.Lock()
		n.fclosed = true
		close(frames)
		n.fmu.Unlock()
	}()
	n.capt = &audio.Capture{Device: a.Device, Channels: a.Channels, Speech: a.Speech, Ref: a.Ref}
	go func() {
		// the microphone again whenever it goes (the ReSpeaker unplugged, the device busy at a
		// restart): a failure is logged once per kind, retried with a backoff to 30 s
		wait, last := 2*time.Second, ""
		for ctx.Err() == nil {
			t0 := time.Now()
			err := n.capt.Run(local)
			if ctx.Err() != nil {
				break
			}
			msg := "the capture ended"
			if err != nil {
				msg = err.Error()
			}
			if msg != last {
				n.Ev.Ev("capture_error", map[string]any{"err": msg, "retry_s": wait.Seconds()})
				last = msg
			}
			if time.Since(t0) > time.Minute {
				wait, last = 2*time.Second, ""
			}
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
			if wait *= 2; wait > 30*time.Second {
				wait = 30 * time.Second
			}
		}
		close(local)
	}()
	defer n.capt.Stop()
	margin := a.Barge
	switch {
	case a.Ref < 0 || margin < 0: // no reference: no telling her echo from a voice by level
		margin = 0
	case margin == 0:
		margin = 12 // scripts/calib: her echo ~22 dB under the reference, a person ~3 dB
	}
	ep := &audio.Endpointer{Mode: a.VADMode, On: 6, Hang: a.Hang, PreRoll: 500 * time.Millisecond, BargeMargin: margin, MinLevel: a.MinLevel, BargeLevel: a.BargeLevel,
		MaxUtt: a.MaxUtt, Muted: func() bool { return n.muted.Load() && !n.remoteMic.Load() }}
	epIn := frames
	if n.wake != nil { // the wake word hears every frame, utterance or not (not while the mic is off)
		epIn = make(chan audio.Frame, 64)
		go func(out chan<- audio.Frame) {
			for f := range frames {
				if !n.muted.Load() || n.remoteMic.Load() {
					n.wake.Hear(f.PCM)
				}
				out <- f
			}
			close(out)
		}(epIn)
	}
	go ep.Run(epIn, n.Engine.Vad)
	n.Ev.Ev("ready", map[string]any{"mind": n.Cfg.Models.Mind, "ears": n.Cfg.Models.Ears, "config": n.Cfg.Path(), "prefs": n.Prefs()})
	n.Engine.Run(ctx)
	return nil
}

// ---- plugin.Host -----------------------------------------------------------------------

func (n *Dialogue) Prefs() config.Prefs { n.mu.Lock(); defer n.mu.Unlock(); return n.prefs }

// SetPrefs applies a change (checked first) and writes it to the config.
func (n *Dialogue) SetPrefs(f func(*config.Prefs)) (config.Prefs, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	old, p := n.prefs, n.prefs
	f(&p)
	knob, ok := dialog.ParseLevel(p.Narration)
	if !ok {
		return old, fmt.Errorf("narration: %q (info, important, urgent, off)", p.Narration)
	}
	if p.Local != "" && p.Local != "rhvoice" && p.Local != "paradee" {
		return old, fmt.Errorf("local: %q (\"\", rhvoice or paradee)", p.Local)
	}
	if p.Local == "paradee" && !paradee.Available() {
		return old, fmt.Errorf("local: no Paradee socket at %s", paradee.DefaultSocket())
	}
	if p.VoiceName != "" && !contains(LiveVoices, p.VoiceName) {
		return old, fmt.Errorf("voice_name: %q", p.VoiceName)
	}
	if p.BubbleSlope != old.BubbleSlope && p.BubbleSlope > 0 {
		n.spk.SetSlope(p.BubbleSlope)
	}
	if p.BubbleOffset != old.BubbleOffset {
		n.spk.SetOffset(p.BubbleOffset)
	}
	if p.Bubble != 0 && (p.Bubble < 0.5 || p.Bubble > n.spk.MaxBubble()+1e-9) {
		return old, fmt.Errorf("bubble: %v m (0.5 to %.1f)", p.Bubble, n.spk.MaxBubble())
	}
	n.prefs = p
	if p.Bubble != old.Bubble && p.Bubble > 0 {
		n.spk.SetBubble(p.Bubble)
	}
	if p.Mic != old.Mic {
		n.muted.Store(!p.Mic)
		if !p.Mic {
			n.Ears.Close()
		}
	}
	if p.VoiceName != old.VoiceName {
		n.Mind.SetVoiceName(p.VoiceName)
	}
	tts := localVoice(p)
	n.Engine.Ctl <- func(e *dialog.Engine) {
		e.SetSilent(!p.Voice && !n.spk.Remote()) // a remote listener hears her whatever the room's switch
		e.SetKnob(knob)
		e.SetLocal(tts)
	}
	n.Ev.Ev("prefs", map[string]any{"prefs": p})
	if err := n.SavePrefs(p); err != nil {
		n.Ev.Ev("prefs_save_error", map[string]any{"err": err.Error()})
		return p, err
	}
	return p, nil
}

func (n *Dialogue) Status() plugin.Status {
	s := n.Engine.Status()
	return plugin.Status{Floor: s.Floor, Awake: s.Awake, Hearing: s.Hearing, Replying: s.Replying,
		Speaking: s.Speaking, Mind: s.Mind, Pending: s.Pending}
}

func (n *Dialogue) Subscribe() (<-chan map[string]any, func()) { return n.Ev.Subscribe() }

func (n *Dialogue) Log(kind string, f map[string]any) { n.Ev.Ev(kind, f) }

func (n *Dialogue) Do(action, arg string) error {
	e := n.Engine
	switch action {
	case "talk":
		e.Do((*dialog.Engine).Talk)
	case "wake":
		e.Do((*dialog.Engine).Wake)
	case "sleep":
		e.Do((*dialog.Engine).Sleep)
	case "stop":
		e.Do((*dialog.Engine).Stop)
	case "mic", "voice":
		on, err := onOff(arg, action == "mic" && n.Prefs().Mic || action == "voice" && n.Prefs().Voice)
		if err != nil {
			return err
		}
		_, err = n.SetPrefs(func(p *config.Prefs) {
			if action == "mic" {
				p.Mic = on
			} else {
				p.Voice = on
			}
		})
		return err
	case "louder", "quieter": // the one sound knob a step, and her last words again at the new level
		step := 1
		if action == "quieter" {
			step = -1
		}
		if err := n.Knob(step); err != nil {
			return err
		}
		n.spk.Sample()
	case "sample": // her last words again, at the bubble's level; nothing said yet (a restart): a line of hers
		if n.Engine.Status().Speaking {
			return fmt.Errorf("sample: she is speaking")
		}
		if !n.spk.Sample() {
			n.Engine.Do(func(e *dialog.Engine) { e.TypeLine("/say " + sampleLine) })
		}
	case "bubble_here": // the distance calibration: arg the person's distance in metres
		d, err := strconv.ParseFloat(strings.TrimSpace(arg), 64)
		if err != nil {
			return fmt.Errorf("bubble_here: %q (metres)", arg)
		}
		return n.Here(d)
	case "bubble_reset":
		_, err := n.SetPrefs(func(p *config.Prefs) { p.BubblePoints, p.BubbleSlope, p.BubbleOffset, p.Bubble = nil, 0, 0, 1 })
		return err
	case "ui": // the screen's mode (nina: her own; ha: Home Assistant's full interface), to the pages
		n.Log("ui", map[string]any{"mode": arg})
	case "type":
		e.Do(func(e *dialog.Engine) { e.TypeLine(arg) })
	case "say":
		e.Do(func(e *dialog.Engine) { e.TypeLine("/say " + arg) })
	case "note":
		lv, text, _ := strings.Cut(arg, " ")
		l, ok := dialog.ParseLevel(lv)
		if !ok || l == dialog.Off || text == "" {
			return fmt.Errorf("note: \"LEVEL text\", LEVEL info, important or urgent")
		}
		e.Do(func(e *dialog.Engine) { e.Note(dialog.Note{Level: l, Text: text}) })
	default:
		if n.OnAction != nil { // the application's own actions (Nina's settings: name, persona, temperament)
			if ok, err := n.OnAction(action, arg); ok {
				return err
			}
		}
		return fmt.Errorf("no action %q", action)
	}
	return nil
}

// onOff: "on"/"off" sets, "" toggles cur.
func onOff(arg string, cur bool) (bool, error) {
	switch arg {
	case "":
		return !cur, nil
	case "on", "true", "1":
		return true, nil
	case "off", "false", "0":
		return false, nil
	}
	return false, fmt.Errorf("%q: on or off", arg)
}

func (n *Dialogue) Choices() map[string][]string {
	rh := []string{}
	if ds, err := os.ReadDir("/usr/share/RHVoice/voices"); err == nil {
		for _, d := range ds {
			rh = append(rh, d.Name())
		}
	}
	sort.Strings(rh)
	return map[string][]string{
		"voice_name": append([]string{""}, LiveVoices...),
		"local":      {"", "rhvoice", "paradee"},
		"paradee":    paradee.Voices,
		"rhvoice":    rh,
		"narration":  {"off", "urgent", "important", "info"},
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// DefaultConfigPath is ~/.config/fsttm-live/config.yaml.
func DefaultConfigPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "fsttm-live", "config.yaml")
}

// Context is silent context for the voice session: she knows it and says nothing; a newer line
// with the same key replaces the older.
func (n *Dialogue) Context(key, line string) { n.Mind.ContextKey(key, line) }

// SetGain sets her voice's loudness (1: as recorded), e.g. from the person's distance.
func (n *Dialogue) SetGain(g float64) { n.spk.SetGain(g) }

// SetReach is the person's distance in metres (the radar): with a calibrated level (audio.level)
// her voice is aimed at them, within the bubble (prefs.bubble).
func (n *Dialogue) SetReach(m float64) { n.spk.SetReach(m) }

// Loudness is the level her speech is aimed at now, dBFS RMS on the speakers' feed (the
// calibrated level at the radius: the person within the bubble); 0 uncalibrated.
func (n *Dialogue) Loudness() float64 {
	if n.spk.Level == 0 {
		return 0
	}
	l := n.spk.Level + n.spk.Gain(n.spk.Radius())
	if n.spk.Ducked() { // a person over her: she is lower, and the music under her with her (live, 2026-11-25:
		// the radio stayed 3-4 dB over her ducked voice)
		l += 20 * math.Log10(audio.DuckGain)
	}
	return l
}

// Volume is her voice's level at the knob (the bubble's edge), dBFS RMS on the speakers' feed;
// 0 uncalibrated. The music follows it (the one knob).
func (n *Dialogue) Volume() float64 {
	if n.spk.Level == 0 {
		return 0
	}
	b := n.Prefs().Bubble
	if b <= 0 {
		b = 1
	}
	return n.spk.Level + n.spk.Gain(b)
}

// Knob moves the one sound knob a step (+1 louder, -1 quieter: the bubble times or over the
// square root of 2, 3 dB), within 0.5 to MaxBubble metres; her voice and the music follow.
func (n *Dialogue) Knob(step int) error {
	_, err := n.SetPrefs(func(p *config.Prefs) {
		b := p.Bubble
		if b <= 0 {
			b = 1
		}
		b *= math.Pow(math.Sqrt2, float64(step))
		p.Bubble = math.Round(math.Max(0.5, math.Min(n.spk.MaxBubble(), b))*100) / 100
	})
	return err
}

// Here is the distance calibration's point: at d metres the knob now is comfortable. The slope
// (dB per doubling) is fitted to all the points through the minimum (1 m, 0 dB), and the bubble
// set to d: the room's space is hers to that distance.
func (n *Dialogue) Here(d float64) error {
	if d < 0.5 || d > 12 {
		return fmt.Errorf("bubble_here: %v m (0.5 to 12)", d)
	}
	g := n.spk.Gain(n.spk.Radius())
	if b := n.Prefs().Bubble; b > 0 {
		g = n.spk.Gain(b)
	}
	_, err := n.SetPrefs(func(p *config.Prefs) {
		p.BubblePoints = append(append([][2]float64{}, p.BubblePoints...), [2]float64{d, math.Round(g*10) / 10})
		p.BubbleSlope, p.BubbleOffset = FitBubble(p.BubblePoints, firstNonZero(p.BubbleSlope, 6))
		max := math.Pow(2, (audio.MaxGain-p.BubbleOffset)/p.BubbleSlope)
		p.Bubble = math.Round(math.Max(0.5, math.Min(d, max))*100) / 100
	})
	n.Log("bubble", map[string]any{"here": d, "gain_db": g, "prefs": n.Prefs()})
	return err
}

// FitBubble fits the distance calibration to its points [metres, dB over the minimum]: the slope (dB
// a doubling, 2 to 9) and the offset (dB at 1 m), so that each point's loudness is kept at its distance.
// Points spread over less than a doubling (one, or all at about one distance) keep the slope given and
// set the offset alone: the last point's loudness exactly.
func FitBubble(pts [][2]float64, slope float64) (float64, float64) {
	if len(pts) == 0 {
		return slope, 0
	}
	var sx, sy float64
	for _, q := range pts {
		sx += math.Log2(q[0])
		sy += q[1]
	}
	n := float64(len(pts))
	mx, my := sx/n, sy/n
	var sxx, sxy float64
	for _, q := range pts {
		x := math.Log2(q[0]) - mx
		sxx += x * x
		sxy += x * (q[1] - my)
	}
	r1 := func(v float64) float64 { return math.Round(v*10) / 10 }
	if n >= 2 && sxx/n >= 0.25 {
		slope = math.Max(2, math.Min(9, sxy/sxx))
		return r1(slope), r1(math.Max(-audio.MaxGain, math.Min(audio.MaxGain, my-slope*mx)))
	}
	last := pts[len(pts)-1]
	return r1(slope), r1(math.Max(-audio.MaxGain, math.Min(audio.MaxGain, last[1]-slope*math.Log2(last[0]))))
}

func firstNonZero(a, b float64) float64 {
	if a != 0 {
		return a
	}
	return b
}

// RemoteMic: a remote microphone (her screen on a phone: the user, 2026-11-25, talk from the phone
// while the room's is off, or from the room) is what she hears while on; turned on, she wakes, as
// at the talk button. Off: the room's microphone again (as its switch says).
func (n *Dialogue) RemoteMic(on bool) {
	if n.remoteMic.Swap(on) == on {
		return
	}
	n.fmu.Lock()
	n.rbuf = nil
	n.fmu.Unlock()
	n.Ev.Ev("remote", map[string]any{"mic": on})
	if on {
		n.Engine.Do((*dialog.Engine).Wake)
	}
}

// RemoteFrame is the remote microphone's sound: s16le mono 16 kHz, any length.
func (n *Dialogue) RemoteFrame(pcm []byte) {
	if !n.remoteMic.Load() {
		return
	}
	n.fmu.Lock()
	defer n.fmu.Unlock()
	if n.fclosed || n.frames == nil {
		return
	}
	n.rbuf = append(n.rbuf, pcm...)
	const fb = audio.FrameLen * 2
	for len(n.rbuf) >= fb {
		f := audio.Frame{PCM: append([]byte(nil), n.rbuf[:fb]...)}
		n.rbuf = n.rbuf[fb:]
		select {
		case n.frames <- f:
		default:
		}
	}
}

// RemoteVoice sends her voice to a remote listener (sink: 24 kHz s16le chunks; nil a cut) instead
// of the room's speakers, from her next reply on; nil: the speakers again. While a listener has
// it, she speaks even with the room's voice switched off.
func (n *Dialogue) RemoteVoice(sink func([]byte)) {
	if (sink != nil) == n.spk.Remote() {
		n.spk.SetRemote(sink) // a new listener in its place
		return
	}
	n.spk.SetRemote(sink)
	silent := !n.Prefs().Voice && sink == nil
	n.Engine.Ctl <- func(e *dialog.Engine) { e.SetSilent(silent) }
	n.Ev.Ev("remote", map[string]any{"voice": sink != nil})
}

// Calibrated: her loudness is the calibrated level's (audio.level), the bubble and the reach.
func (n *Dialogue) Calibrated() bool { return n.spk.Level != 0 }

// Note is a NOTE to the floor at a level (info, important, urgent): said when the floor is free
// and the narration knob lets it through; filtered, it becomes context.
func (n *Dialogue) Note(level, text string) error {
	lv, ok := dialog.ParseLevel(level)
	if !ok || lv == dialog.Off || text == "" {
		return fmt.Errorf("note: level info, important or urgent, and a text")
	}
	n.Engine.Notes <- dialog.Note{Level: lv, Text: text}
	return nil
}

// sampleLine: what she says for the bubble's replay when she has said nothing yet
const sampleLine = "This is how loud I am from here. Is it comfortable?"

// linger: how long her session stays open asleep (10 min unless set).
func linger(d time.Duration) time.Duration {
	if d <= 0 {
		return 10 * time.Minute
	}
	return d
}

// spotter loads the local wake word unless the mode is the cloud's; a model that does not load
// leaves the cloud's.
func (n *Dialogue) spotter(cfg *config.Config, ev func(string, map[string]any)) dialog.Spotter {
	w := cfg.Dialog.Wake
	if w.Mode == "" || w.Mode == "cloud" {
		return nil
	}
	if w.Engine == "oww" {
		d, err := oww.New(cfg.Expand(w.Model), cfg.Expand(w.Word))
		if err != nil {
			ev("kws_error", map[string]any{"err": err.Error()})
			fmt.Fprintf(os.Stderr, "wake: openWakeWord did not load (%v): her name is the transcriber's\n", err) // before the log's subscribers
			return nil
		}
		th, label := w.Threshold, w.Label
		if th <= 0 {
			th = 0.5
		}
		if label == "" {
			label = "NINA"
		}
		n.wake = oww.NewStream(d, label, th, ev)
		ev("kws_ready", map[string]any{"mode": w.Mode, "engine": "oww", "threshold": th})
		if w.KWS != "" { // sherpa's spotter too, per utterance: her name before a sentence
			kt := w.KWSThreshold
			if kt <= 0 {
				kt = 0.25
			}
			k, err := kws.New(cfg.Expand(w.KWS), cfg.Expand(w.Keywords), kt)
			if err != nil {
				fmt.Fprintf(os.Stderr, "wake: sherpa's spotter did not load (%v): openWakeWord alone\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "wake: openWakeWord, %s, threshold %.2f, listening continuously; and sherpa's spotter per utterance (threshold %.2f)\n", cfg.Expand(w.Word), th, kt)
				return dialog.Spotters{n.wake, k}
			}
		}
		fmt.Fprintf(os.Stderr, "wake: openWakeWord, %s, threshold %.2f, listening continuously\n", cfg.Expand(w.Word), th)
		return n.wake
	}
	k, err := kws.New(cfg.Expand(w.Model), cfg.Expand(w.Keywords), w.Threshold)
	if err != nil {
		ev("kws_error", map[string]any{"err": err.Error()})
		return nil
	}
	ev("kws_ready", map[string]any{"mode": w.Mode})
	return k
}

// StartPlugins starts the plugins the config enables (the web panel, the buttons), on this
// dialogue as their host.
func (n *Dialogue) StartPlugins(ctx context.Context) error { return plugin.Start(ctx, n.Cfg, n) }
