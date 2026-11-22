package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadSavePrefs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nina.yaml")
	c, err := Load(p) // created from the default
	if err != nil {
		t.Fatal(err)
	}
	if c.Dialog.AwakeFor != 90*time.Second || !c.Prefs.Mic || c.Prefs.Narration != "important" {
		t.Fatalf("defaults: %+v %+v", c.Dialog, c.Prefs)
	}
	var web struct{ Addr string }
	if on, err := c.Plugin("web", &web); err != nil || !on || web.Addr != ":8770" {
		t.Fatalf("web plugin: %v %v %q", on, err, web.Addr)
	}
	if on, _ := c.Plugin("gpio", nil); on {
		t.Fatal("gpio on by default")
	}
	pr := c.Prefs
	pr.Mic, pr.VoiceName, pr.Narration = false, "Kore", "urgent"
	if err := c.SavePrefs(pr); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	for _, want := range []string{"voice_name: Kore", "narration: urgent", "mic: false",
		"# the lowest NOTE level spoken", "# the AI Studio key's file", "long_press: 1200ms"} {
		if !strings.Contains(s, want) {
			t.Errorf("saved file lacks %q", want)
		}
	}
	c2, err := Load(p)
	if err != nil || c2.Prefs != pr {
		t.Fatalf("reload: %v %+v", err, c2.Prefs)
	}
}

func TestPartialFileKeepsDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nina.yaml")
	os.WriteFile(p, []byte("audio:\n  device: pulse:x\nplugins:\n  gpio: {enabled: true}\n"), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Audio.Device != "pulse:x" || c.Audio.Channels != 6 || c.Models.Mind == "" {
		t.Fatalf("%+v %+v", c.Audio, c.Models)
	}
	if on, _ := c.Plugin("web", nil); on {
		t.Fatal("web enabled though the file's plugins leave it out")
	}
	if err := c.SavePrefs(Prefs{Mic: true, Narration: "info"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "narration: info") || !strings.Contains(string(b), "device: pulse:x") {
		t.Fatalf("saved:\n%s", b)
	}
}
