// radar reads the HLK-LD2410C mmWave radar on the Raspberry Pi's UART and says whether someone is
// in front of Nina's screen (the user, 2026-11-24): it publishes sensor.nina_viewer to Home Assistant
// (state present | absent; distance in m, trend approaching | departing | still, near, in_reach)
// and drives the kiosk's screen through its idle helpers: someone within reach lifts the black
// overlay (and keeps it lifted while they stay), nobody for -off puts it up. A touch still works as
// before (swayidle). The LD2410 gives distance only: no angle, so the face does not turn.
//
//	radar [-dev /dev/serial0] [-hass http://homeassistant.local:8123 -hass http://192.168.1.140:8123]
//
// Later the same pieces (internal/ld2410, internal/presence) become a plugin of Nina's own process.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/ld2410"
	"github.com/ibnHatab/fsttm-live/internal/presence"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(s string) error { *l = append(*l, s); return nil }

func main() {
	dev := flag.String("dev", "/dev/serial0", "the radar's UART")
	baud := flag.Int("baud", 256000, "its speed")
	token := flag.String("token", "~/.config/fsttm/hass.token", "Home Assistant's token file")
	entity := flag.String("entity", "sensor.nina_viewer", "the entity published")
	off := flag.Duration("off", 60*time.Second, "nobody this long: the screen's black overlay")
	wake := flag.String("wake-cmd", "/usr/local/lib/ha-pi-kiosk/idle-wake.sh", "lifts the overlay")
	blank := flag.String("off-cmd", "/usr/local/lib/ha-pi-kiosk/idle-off.sh", "puts it up")
	screen := flag.Bool("screen", true, "drive the screen")
	var hass list
	flag.Var(&hass, "hass", "a Home Assistant to publish to (repeat; default http://homeassistant.local:8123)")
	flag.Parse()
	log.SetFlags(log.Ltime)
	if len(hass) == 0 {
		hass = list{"http://homeassistant.local:8123"}
	}
	tf := *token
	if strings.HasPrefix(tf, "~/") {
		h, _ := os.UserHomeDir()
		tf = filepath.Join(h, tf[2:])
	}
	tok, err := os.ReadFile(tf)
	if err != nil {
		log.Fatal(err)
	}
	port, err := ld2410.Open(*dev, *baud)
	if err != nil {
		log.Fatal(err)
	}

	tr := &presence.Tracker{C: presence.Default}
	var mu sync.Mutex
	var st presence.State
	var lastFrame time.Time
	publish := func(s presence.State) {
		state := "absent"
		if s.Present {
			state = "present"
		}
		attrs := map[string]any{"friendly_name": "Nina's viewer", "icon": "mdi:account-eye", "distance": s.Distance,
			"trend": s.Trend, "near": s.Near, "in_reach": s.InReach, "moving": s.Moving, "still": s.Still,
			"since": s.Since.Format(time.RFC3339), "source": "ld2410c"}
		b, _ := json.Marshal(map[string]any{"state": state, "attributes": attrs})
		for _, h := range hass {
			req, _ := http.NewRequest("POST", strings.TrimRight(h, "/")+"/api/states/"+*entity, bytes.NewReader(b))
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
			req.Header.Set("Content-Type", "application/json")
			c := &http.Client{Timeout: 3 * time.Second}
			if resp, err := c.Do(req); err != nil {
				log.Printf("%s: %v", h, err)
			} else {
				resp.Body.Close()
			}
		}
	}
	go func() {
		err := ld2410.Scan(port, func(f ld2410.Frame) {
			now := time.Now()
			mu.Lock()
			s, changed := tr.Update(f, now)
			st, lastFrame = s, now
			mu.Unlock()
			if changed {
				log.Printf("%s %.2f m %s near=%v reach=%v", map[bool]string{true: "present", false: "absent"}[s.Present], s.Distance, s.Trend, s.Near, s.InReach)
				go publish(s)
			}
		})
		log.Fatalf("the radar: %v", err)
	}()

	// the screen, and a heartbeat for Home Assistant (its states are lost on a restart)
	overlay := func() bool { return exec.Command("pgrep", "-x", "swaylock").Run() == nil }
	run := func(cmd string) {
		if out, err := exec.Command(cmd).CombinedOutput(); err != nil {
			log.Printf("%s: %v %s", cmd, err, out)
		}
	}
	blanked := false
	beat := 0
	for range time.Tick(time.Second) {
		mu.Lock()
		s, lf := st, lastFrame
		mu.Unlock()
		if time.Since(lf) > 5*time.Second && !lf.IsZero() {
			log.Printf("no frames from the radar for %v", time.Since(lf).Round(time.Second))
		}
		if beat++; beat%30 == 0 {
			go publish(s)
		}
		if !*screen {
			continue
		}
		switch {
		case s.Present && s.InReach && overlay():
			log.Printf("someone %.2f m away: the screen wakes", s.Distance)
			run(*wake)
			blanked = false
		case !s.Present && !s.Since.IsZero() && time.Since(s.Since) >= *off && !blanked && !overlay():
			log.Printf("nobody for %v: the screen goes black", *off)
			run(*blank)
			blanked = true
		case s.Present:
			blanked = false
		}
	}
	fmt.Println()
}
