// Package config is nina-live's one file: the machine's settings (sound, models, dialog), the
// person's preferences (mic, voice, narration level: changed at run time by any plugin and
// written back here), and one block per plugin (web, gpio, …: each enabled or not, each with
// its own settings). Writing the preferences back keeps the rest of the file as it is, its
// comments included.
package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed default.yaml
var Default []byte

type Config struct {
	Key     string `yaml:"key"`     // the AI Studio key's file (never the key itself)
	Charter string `yaml:"charter"` // the voice session's system instruction file
	Models  struct {
		Ears  string   `yaml:"ears"`
		Mind  string   `yaml:"mind"`
		Vocab []string `yaml:"vocab"` // the ears' customVocabulary
	} `yaml:"models"`
	Audio struct {
		Device   string        `yaml:"device"`   // capture: ALSA device or pulse:SOURCE
		Channels int           `yaml:"channels"` // 6: the ReSpeaker's firmware
		Speech   int           `yaml:"speech"`   // the channel kept
		Ref      int           `yaml:"ref"`      // echo reference channel, -1 none
		Out      string        `yaml:"out"`      // playback: aplay -D, or pulse:SINK; empty: default
		Level    float64       `yaml:"level"`    // her speech out, dBFS RMS, at a 1 m bubble (scripts/calib); 0: as recorded
		Barge    float64       `yaml:"barge"`    // dB: a voice this close to the echo reference is the person's: she ducks at once (0: 12; -1 off)
		VADMode  int           `yaml:"vad_mode"`
		Hang     time.Duration `yaml:"hang"`
		MaxUtt   time.Duration `yaml:"max_utterance"`
	} `yaml:"audio"`
	Dialog struct {
		FollowUp    time.Duration `yaml:"follow_up"`
		AwakeFor    time.Duration `yaml:"awake_for"`
		StartAsleep bool          `yaml:"start_asleep"`
		NoteGap     time.Duration `yaml:"note_gap"`
		Wake        struct {
			Mode      string  `yaml:"mode"`   // cloud | shadow | local
			Engine    string  `yaml:"engine"` // kws (sherpa-onnx's keyword spotter, per utterance) | oww (openWakeWord, continuous)
			Model     string  `yaml:"model"`  // kws: the model's folder; oww: the folder of melspectrogram.onnx and embedding_model.onnx
			Keywords  string  `yaml:"keywords"`
			Word      string  `yaml:"word"`  // oww: the wake word's model (.onnx)
			Label     string  `yaml:"label"` // oww: its keyword's name in the log (NINA)
			Threshold float32 `yaml:"threshold"`
		} `yaml:"wake"`
	} `yaml:"dialog"`
	Prefs   Prefs                `yaml:"prefs"`
	Plugins map[string]yaml.Node `yaml:"plugins"`
	Sources map[string]yaml.Node `yaml:"sources"`

	path string
	doc  yaml.Node
	mu   sync.Mutex
}

// Prefs are what the person changes while Nina runs, from any plugin.
type Prefs struct {
	Mic       bool    `yaml:"mic" json:"mic"`               // Nina listens
	Voice     bool    `yaml:"voice" json:"voice"`           // Nina's sound (off: replies only shown)
	VoiceName string  `yaml:"voice_name" json:"voice_name"` // the Live voice ("" the model's)
	Local     string  `yaml:"local" json:"local"`           // "" the Live audio; rhvoice, paradee: a local voice speaks her transcript
	RHVoice   string  `yaml:"rhvoice" json:"rhvoice"`       // its profile
	Paradee   string  `yaml:"paradee" json:"paradee"`       // the Paradee socket's voice (a desktop's local voice)
	Narration string  `yaml:"narration" json:"narration"`   // the lowest NOTE level spoken: info, important, urgent, off
	Bubble    float64 `yaml:"bubble" json:"bubble"`         // the one sound knob: metres her voice (and the music) carries; 1 is audio.level
	// the distance calibration: comfortable points [metres, dB over the minimum] and the slope
	// fitted to them (dB per doubling of the distance; 0: 6, the free field's)
	BubbleSlope  float64      `yaml:"bubble_slope" json:"bubble_slope"`
	BubblePoints [][2]float64 `yaml:"bubble_points,flow,omitempty" json:"bubble_points"`
}

// Load reads path; a missing file is created from the default.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, Default, 0o644); err != nil {
			return nil, err
		}
		b = Default
	} else if err != nil {
		return nil, err
	}
	c := &Config{path: path}
	// the default first, the file over it: a key the file lacks keeps its default
	if err := yaml.Unmarshal(Default, c); err != nil {
		return nil, fmt.Errorf("default config: %w", err)
	}
	c.Plugins, c.Sources = nil, nil
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, &c.doc); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) Path() string { return c.path }

// Expand resolves ~/ and a path relative to the config file's directory.
func (c *Config) Expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	if p != "" && !filepath.IsAbs(p) && c.path != "" {
		return filepath.Join(filepath.Dir(c.path), p)
	}
	return p
}

// Plugin decodes a plugin's block into v and reports whether it is enabled.
func (c *Config) Plugin(name string, v any) (bool, error) {
	return block(c.Plugins, "plugins", name, v)
}

// Source decodes a source's block (sources.NAME) into v and reports whether it is enabled.
func (c *Config) Source(name string, v any) (bool, error) {
	return block(c.Sources, "sources", name, v)
}

func block(m map[string]yaml.Node, section, name string, v any) (bool, error) {
	n, ok := m[name]
	if !ok {
		return false, nil
	}
	var on struct {
		Enabled bool `yaml:"enabled"`
	}
	if err := n.Decode(&on); err != nil {
		return false, fmt.Errorf("%s.%s: %w", section, name, err)
	}
	if v != nil {
		if err := n.Decode(v); err != nil {
			return false, fmt.Errorf("%s.%s: %w", section, name, err)
		}
	}
	return on.Enabled, nil
}

// SavePrefs writes p into the file's prefs block, the rest of the file untouched.
func (c *Config) SavePrefs(p Prefs) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Prefs = p
	var pn yaml.Node
	if err := pn.Encode(p); err != nil {
		return err
	}
	root := &c.doc
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: not a mapping", c.path)
	}
	found := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "prefs" {
			old := root.Content[i+1]
			mergePrefs(old, &pn)
			found = true
		}
	}
	if !found {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "prefs"}, &pn)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&c.doc); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// mergePrefs sets the old block's values from the new one, keeping its keys' comments.
func mergePrefs(old, nw *yaml.Node) {
	if old.Kind != yaml.MappingNode {
		*old = *nw
		return
	}
	for j := 0; j+1 < len(nw.Content); j += 2 {
		k, v := nw.Content[j], nw.Content[j+1]
		set := false
		for i := 0; i+1 < len(old.Content); i += 2 {
			if old.Content[i].Value == k.Value {
				v.HeadComment, v.LineComment, v.FootComment = old.Content[i+1].HeadComment, old.Content[i+1].LineComment, old.Content[i+1].FootComment
				old.Content[i+1] = v
				set = true
			}
		}
		if !set {
			old.Content = append(old.Content, k, v)
		}
	}
}
