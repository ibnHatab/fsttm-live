// Package plugin is how the world reaches Nina: the web panel, a Raspberry Pi's buttons and
// lights, anything else. A plugin is instantiated from its block of the config (plugins.NAME,
// enabled or not); several run together (set her up on the panel, drive her with the buttons).
// Each sees the same Host: the preferences (changed by any plugin, saved in the config), the
// status, the event stream and the actions.
package plugin

import (
	"context"
	"fmt"
	"sort"

	"github.com/ibnHatab/fsttm-live/internal/config"
)

// Status is the dialog's state, as a plugin shows it.
type Status struct {
	Floor    string `json:"floor"`    // FSTTM: FREEu, FREEs, USER, SYSTEM, BOTHs, BOTHu
	Awake    bool   `json:"awake"`    // attention
	Hearing  bool   `json:"hearing"`  // an utterance is open
	Replying bool   `json:"replying"` // a turn is out to the voice
	Speaking bool   `json:"speaking"` // her sound is playing
	Mind     bool   `json:"mind"`     // the voice session is connected
	Pending  int    `json:"pending"`  // NOTEs waiting for the floor
}

// Actions a plugin may ask for (Do):
//
//	talk         wake her, or stop her if she is speaking (a button's press)
//	wake, sleep  attention; sleep closes her voice session
//	stop         cut what she is saying
//	mic, voice   toggle (arg "on"/"off" sets)
//	type TEXT    the person's words, typed
//	note "LEVEL TEXT", say TEXT
type Host interface {
	Prefs() config.Prefs
	SetPrefs(func(*config.Prefs)) (config.Prefs, error)
	Status() Status
	Subscribe() (<-chan map[string]any, func())
	Do(action, arg string) error
	Choices() map[string][]string // the values each preference may take
	Log(kind string, fields map[string]any)
}

type Plugin interface {
	Run(ctx context.Context, h Host) error
}

// Factory makes a plugin from the config; enabled false: not configured to run.
type Factory func(c *config.Config) (p Plugin, enabled bool, err error)

var registry = map[string]Factory{}

func Register(name string, f Factory) { registry[name] = f }

func Names() []string {
	var n []string
	for k := range registry {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Start runs every enabled plugin; a plugin's error is logged, not fatal to the others.
func Start(ctx context.Context, c *config.Config, h Host) error {
	for name := range c.Plugins {
		if _, ok := registry[name]; !ok {
			return fmt.Errorf("plugins.%s: no such plugin (have %v)", name, Names())
		}
	}
	for _, name := range Names() {
		p, on, err := registry[name](c)
		if err != nil {
			return err
		}
		if !on {
			continue
		}
		h.Log("plugin", map[string]any{"name": name})
		go func(name string, p Plugin) {
			if err := p.Run(ctx, h); err != nil && ctx.Err() == nil {
				h.Log("plugin_error", map[string]any{"name": name, "err": err.Error()})
			}
		}(name, p)
	}
	return nil
}
