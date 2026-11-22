// Package nina is the runtime: everything built from the config (the mic, the endpointer, the
// two Live sessions, the speaker, the floor), the preferences applied and saved, and the Host
// the plugins see.
package nina

import (
	"context"
	"fmt"
	"github.com/ibnHatab/fsttm-live/internal/kws"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/audio"
	"github.com/ibnHatab/fsttm-live/internal/config"
	"github.com/ibnHatab/fsttm-live/internal/dialog"
	"github.com/ibnHatab/fsttm-live/internal/evlog"
	"github.com/ibnHatab/fsttm-live/internal/live"
	"github.com/ibnHatab/fsttm-live/internal/paradee"
	"github.com/ibnHatab/fsttm-live/internal/plugin"
	"github.com/ibnHatab/fsttm-live/internal/rhvoice"
	"github.com/ibnHatab/fsttm-live/internal/sources"
	"google.golang.org/genai"
)

// LiveVoices are the Live API's prebuilt voices.
var LiveVoices = []string{"Puck", "Charon", "Kore", "Fenrir", "Aoede", "Leda", "Orus", "Zephyr",
	"Achernar", "Achird", "Algenib", "Algieba", "Alnilam", "Autonoe", "Callirrhoe", "Despina",
	"Enceladus", "Erinome", "Gacrux", "Iapetus", "Laomedeia", "Pulcherrima", "Rasalgethi",
	"Sadachbia", "Sadaltager", "Schedar", "Sulafat", "Umbriel", "Vindemiatrix", "Zubenelgenubi"}

// Tools is what SOURCES offer the voice session (declarations and their functions).
type Tools struct {
	Decls []*genai.Tool
	Funcs map[string]live.ToolFunc
}

type Nina struct {
	Cfg    *config.Config
	Ev     *evlog.Log
	Engine *dialog.Engine
	Mind   *live.Voice
	Ears   *live.Ears

	mu    sync.Mutex
	prefs config.Prefs
	muted atomic.Bool
	capt  *audio.Capture
}

func New(ctx context.Context, cfg *config.Config, log *evlog.Log, tools Tools) (*Nina, error) {
	client, err := live.NewClient(ctx, cfg.Expand(cfg.Key))
	if err != nil {
		return nil, err
	}
	sys, err := os.ReadFile(cfg.Expand(cfg.Charter))
	if err != nil {
		return nil, err
	}
	n := &Nina{Cfg: cfg, Ev: log, prefs: cfg.Prefs}
	n.Mind = &live.Voice{Client: client, Model: cfg.Models.Mind, VoiceName: cfg.Prefs.VoiceName, System: string(sys),
		Tools: tools.Decls, Funcs: tools.Funcs, Compress: 24000, Log: log.Ev}
	n.Ears = &live.Ears{Client: client, Model: cfg.Models.Ears, Vocab: cfg.Models.Vocab,
		Settle: 200 * time.Millisecond, FinalWait: 1500 * time.Millisecond, MaxAge: 8 * time.Minute, Log: log.Ev}
	spk := audio.NewSpeaker(cfg.Audio.Out, 24000)
	knob, ok := dialog.ParseLevel(cfg.Prefs.Narration)
	if !ok {
		return nil, fmt.Errorf("prefs.narration: %q", cfg.Prefs.Narration)
	}
	n.Engine = dialog.New(dialog.Config{FollowUp: cfg.Dialog.FollowUp, AwakeFor: cfg.Dialog.AwakeFor,
		ReplyTimeout: 10 * time.Second, Knob: knob, NoteGap: cfg.Dialog.NoteGap,
		Silent: !cfg.Prefs.Voice, StartAsleep: cfg.Dialog.StartAsleep,
		Outlast: cfg.Audio.Hang + 600*time.Millisecond,
		Wake:    cfg.Dialog.Wake.Mode, Spotter: spotter(cfg, log.Ev)}, n.Ears, n.Mind, spk, nil, log.Ev)
	if t := localVoice(cfg.Prefs); t != nil {
		n.Engine.SetLocal(t)
	}
	n.Mind.Out, n.Ears.Out = n.Engine.MindEv, n.Engine.Heard
	log.Quiet["interim"], log.Quiet["said"] = true, true // the panel's; the file has heard and reply_done
	go func() {
		for ok := range spk.Done {
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
func (n *Nina) Run(ctx context.Context) error {
	n.Ears.Start(ctx)
	if err := n.Mind.Connect(ctx); err != nil {
		return err
	}
	a := n.Cfg.Audio
	frames := make(chan audio.Frame, 64)
	n.capt = &audio.Capture{Device: a.Device, Channels: a.Channels, Speech: a.Speech, Ref: a.Ref}
	go func() {
		if err := n.capt.Run(frames); err != nil && ctx.Err() == nil {
			n.Ev.Ev("capture_error", map[string]any{"err": err.Error()})
		}
		close(frames)
	}()
	defer n.capt.Stop()
	ep := &audio.Endpointer{Mode: a.VADMode, On: 6, Hang: a.Hang, PreRoll: 300 * time.Millisecond,
		MaxUtt: a.MaxUtt, Muted: n.muted.Load}
	go ep.Run(frames, n.Engine.Vad)
	n.Ev.Ev("ready", map[string]any{"mind": n.Cfg.Models.Mind, "ears": n.Cfg.Models.Ears, "config": n.Cfg.Path(), "prefs": n.Prefs()})
	n.Engine.Run(ctx)
	return nil
}

// ---- plugin.Host -----------------------------------------------------------------------

func (n *Nina) Prefs() config.Prefs { n.mu.Lock(); defer n.mu.Unlock(); return n.prefs }

// SetPrefs applies a change (checked first) and writes it to the config.
func (n *Nina) SetPrefs(f func(*config.Prefs)) (config.Prefs, error) {
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
	n.prefs = p
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
		e.SetSilent(!p.Voice)
		e.SetKnob(knob)
		e.SetLocal(tts)
	}
	n.Ev.Ev("prefs", map[string]any{"prefs": p})
	if err := n.Cfg.SavePrefs(p); err != nil {
		n.Ev.Ev("prefs_save_error", map[string]any{"err": err.Error()})
		return p, err
	}
	return p, nil
}

func (n *Nina) Status() plugin.Status {
	s := n.Engine.Status()
	return plugin.Status{Floor: s.Floor, Awake: s.Awake, Hearing: s.Hearing, Replying: s.Replying,
		Speaking: s.Speaking, Mind: s.Mind, Pending: s.Pending}
}

func (n *Nina) Subscribe() (<-chan map[string]any, func()) { return n.Ev.Subscribe() }

func (n *Nina) Log(kind string, f map[string]any) { n.Ev.Ev(kind, f) }

func (n *Nina) Do(action, arg string) error {
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

func (n *Nina) Choices() map[string][]string {
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

// Sources routes what SOURCES report: context to the voice session (silently), the rest to
// the floor as NOTEs (the knob and the floor decide when, and whether, she says them).
func (n *Nina) Sources(ctx context.Context, lines <-chan sources.Line) {
	for {
		select {
		case <-ctx.Done():
			return
		case l := <-lines:
			lv, ok := dialog.ParseLevel(l.Level)
			if !ok || lv == dialog.Off {
				n.Mind.ContextKey(l.Key, l.Text)
				n.Ev.Ev("source", map[string]any{"kind": l.Kind, "level": "context", "text": l.Text})
				continue
			}
			n.Ev.Ev("source", map[string]any{"kind": l.Kind, "level": l.Level, "text": l.Text})
			n.Engine.Notes <- dialog.Note{Level: lv, Text: l.Text} // filtered, it becomes context
		}
	}
}

// spotter loads the local wake word unless the mode is the cloud's; a model that does not load
// leaves the cloud's.
func spotter(cfg *config.Config, ev func(string, map[string]any)) dialog.Spotter {
	w := cfg.Dialog.Wake
	if w.Mode == "" || w.Mode == "cloud" {
		return nil
	}
	k, err := kws.New(cfg.Expand(w.Model), cfg.Expand(w.Keywords), w.Threshold)
	if err != nil {
		ev("kws_error", map[string]any{"err": err.Error()})
		return nil
	}
	ev("kws_ready", map[string]any{"mode": w.Mode})
	return k
}
