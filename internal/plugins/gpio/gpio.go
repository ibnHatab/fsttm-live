// Package gpio is the buttons-and-lights plugin for a Raspberry Pi: each button line does an
// action on a press and another on a long press (talk / sleep, mic, voice, stop, wake…: the
// Host's actions); each light line shows a state (awake, speaking, hearing, replying, mic,
// voice). It runs beside the web panel: set her up on the screen, drive her with the buttons.
// Lines are requested through the GPIO character device (/dev/gpiochipN), no root, no cgo.
package gpio

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/config"
	"github.com/ibnHatab/fsttm-live/internal/plugin"
	"github.com/warthog618/go-gpiocdev"
)

type Button struct {
	Line  int    `yaml:"line"`
	Press string `yaml:"press"` // an action (plugin.Host.Do)
	Long  string `yaml:"long"`  // on a long press ("" : the press action)
	Arg   string `yaml:"arg"`
}

type Light struct {
	Line int    `yaml:"line"`
	Show string `yaml:"show"` // awake, speaking, hearing, replying, mic, voice
}

type GPIO struct {
	Chip       string        `yaml:"chip"`
	Debounce   time.Duration `yaml:"debounce"`
	LongPress  time.Duration `yaml:"long_press"`
	ActiveHigh bool          `yaml:"active_high"` // buttons to 3V3 (default: to ground, pull-up)
	Buttons    []Button      `yaml:"buttons"`
	Lights     []Light       `yaml:"lights"`
}

func init() {
	plugin.Register("gpio", func(c *config.Config) (plugin.Plugin, bool, error) {
		g := &GPIO{Chip: "gpiochip0", Debounce: 30 * time.Millisecond, LongPress: 1200 * time.Millisecond}
		on, err := c.Plugin("gpio", g)
		if err == nil && on {
			err = g.check()
		}
		return g, on, err
	})
}

var shows = map[string]func(plugin.Status, config.Prefs) bool{
	"awake":    func(s plugin.Status, _ config.Prefs) bool { return s.Awake },
	"speaking": func(s plugin.Status, _ config.Prefs) bool { return s.Speaking },
	"hearing":  func(s plugin.Status, _ config.Prefs) bool { return s.Hearing },
	"replying": func(s plugin.Status, _ config.Prefs) bool { return s.Replying },
	"mic":      func(_ plugin.Status, p config.Prefs) bool { return p.Mic },
	"voice":    func(_ plugin.Status, p config.Prefs) bool { return p.Voice },
}

var actions = map[string]bool{"talk": true, "wake": true, "sleep": true, "stop": true, "mic": true, "voice": true, "say": true, "type": true}

func (g *GPIO) check() error {
	for _, b := range g.Buttons {
		if !actions[b.Press] || (b.Long != "" && !actions[b.Long]) {
			return fmt.Errorf("plugins.gpio: line %d: actions are talk, wake, sleep, stop, mic, voice, say, type", b.Line)
		}
	}
	for _, l := range g.Lights {
		if shows[l.Show] == nil {
			return fmt.Errorf("plugins.gpio: line %d: show one of awake, speaking, hearing, replying, mic, voice", l.Line)
		}
	}
	return nil
}

func (g *GPIO) Run(ctx context.Context, h plugin.Host) error {
	var lines []*gpiocdev.Line
	defer func() {
		for _, l := range lines {
			l.Close()
		}
	}()
	for _, b := range g.Buttons {
		k := NewKey(b, g.LongPress, func(action, arg string) {
			if err := h.Do(action, arg); err != nil {
				h.Log("gpio_error", map[string]any{"line": b.Line, "err": err.Error()})
			} else {
				h.Log("gpio", map[string]any{"line": b.Line, "action": action})
			}
		})
		opts := []gpiocdev.LineReqOption{gpiocdev.AsInput, gpiocdev.WithBothEdges, gpiocdev.WithDebounce(g.Debounce),
			gpiocdev.WithEventHandler(func(ev gpiocdev.LineEvent) {
				down := ev.Type == gpiocdev.LineEventFallingEdge
				if g.ActiveHigh {
					down = !down
				}
				k.Edge(down)
			})}
		if g.ActiveHigh {
			opts = append(opts, gpiocdev.WithPullDown)
		} else {
			opts = append(opts, gpiocdev.WithPullUp)
		}
		l, err := gpiocdev.RequestLine(g.Chip, b.Line, opts...)
		if err != nil {
			return fmt.Errorf("gpio button line %d: %w", b.Line, err)
		}
		lines = append(lines, l)
	}
	var outs []*gpiocdev.Line
	for _, li := range g.Lights {
		l, err := gpiocdev.RequestLine(g.Chip, li.Line, gpiocdev.AsOutput(0))
		if err != nil {
			return fmt.Errorf("gpio light line %d: %w", li.Line, err)
		}
		lines = append(lines, l)
		outs = append(outs, l)
	}
	h.Log("gpio_ready", map[string]any{"chip": g.Chip, "buttons": len(g.Buttons), "lights": len(g.Lights)})
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	last := make([]int, len(outs))
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			s, p := h.Status(), h.Prefs()
			for i, li := range g.Lights {
				v := 0
				if shows[li.Show](s, p) {
					v = 1
				}
				if v != last[i] {
					outs[i].SetValue(v)
					last[i] = v
				}
			}
		}
	}
}

// Key turns a button's edges into actions: the press action on release, the long action as
// soon as it has been held LongPress (and nothing on its release).
type Key struct {
	b     Button
	long  time.Duration
	do    func(action, arg string)
	mu    sync.Mutex
	down  bool
	fired bool
	timer *time.Timer
}

func NewKey(b Button, long time.Duration, do func(action, arg string)) *Key {
	return &Key{b: b, long: long, do: do}
}

func (k *Key) Edge(down bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if down == k.down {
		return
	}
	k.down = down
	if down {
		k.fired = false
		if k.b.Long != "" {
			k.timer = time.AfterFunc(k.long, func() {
				k.mu.Lock()
				fire := k.down && !k.fired
				k.fired = fire || k.fired
				k.mu.Unlock()
				if fire {
					k.do(k.b.Long, k.b.Arg)
				}
			})
		}
		return
	}
	if k.timer != nil {
		k.timer.Stop()
	}
	if !k.fired {
		k.fired = true
		go k.do(k.b.Press, k.b.Arg)
	}
}
