// Package web is the panel plugin: Nina's preferences (mic, voice, the Live voice, the local
// voice, the narration knob), her state (the floor, attention) and the conversation as it
// happens, in one page: on a desktop, or a Raspberry Pi's screen in kiosk mode.
//
//	GET  /              the panel
//	GET  /api/state     {prefs, status, choices}
//	POST /api/prefs     a partial prefs object: applied and saved in the config
//	POST /api/do        {action, arg}: talk, wake, sleep, stop, mic, voice, type, say, note
//	GET  /api/events    the event stream (server-sent events), status included
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/config"
	"github.com/ibnHatab/fsttm-live/internal/plugin"
)

//go:embed static
var static embed.FS

type Web struct {
	Addr string `yaml:"addr"`
	// AllowOrigin: the pages of these origins may use the API from their own (Home Assistant's
	// dashboard, http://homeassistant.local:8123: its Nina card reads the state and the events and
	// sets the prefs); "" none, "*" any
	AllowOrigin []string `yaml:"allow_origin"`
	// Static: more files served, URL prefix → directory (acasa's ha-ui www/nina at /nina/: Nina's
	// own screen, her card and personas, up before Home Assistant is)
	Static map[string]string `yaml:"static"`

	mu     sync.Mutex
	facts  map[string]map[string]any // the last event of each sticky kind (persona), for /api/state
	recent []map[string]any          // what she did lately (recentKinds), for /api/recent: the activity card
}

// recentKinds are what the activity card shows: her tool calls, her mood, who is there, surprises.
var recentKinds = map[string]bool{"tool": true, "persona": true, "presence": true, "surprises": true, "ui": true}

// sticky are the event kinds whose last one a page loading now needs (Nina's persona).
var sticky = map[string]bool{"persona": true, "surprises": true, "settings": true}

// cors lets the allowed origins' pages call the API (a preflight answered here).
func (w *Web) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
			for _, a := range w.AllowOrigin {
				if a == "*" || a == o {
					rw.Header().Set("Access-Control-Allow-Origin", o)
					rw.Header().Set("Vary", "Origin")
					rw.Header().Set("Access-Control-Allow-Methods", "GET, POST")
					rw.Header().Set("Access-Control-Allow-Headers", "Content-Type")
					break
				}
			}
			if r.Method == http.MethodOptions {
				rw.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(rw, r)
	})
}

func init() {
	plugin.Register("web", func(c *config.Config) (plugin.Plugin, bool, error) {
		w := &Web{Addr: ":8770"}
		on, err := c.Plugin("web", w)
		return w, on, err
	})
}

// shown are the events the panel draws (the rest stay in the log file).
var shown = map[string]bool{"heard": true, "interim": true, "turn": true, "reply_done": true, "barge_in": true,
	"cut": true, "echo": true, "aside": true, "asleep_ignored": true, "attention": true, "note_queued": true,
	"note_filtered": true, "tool": true, "prefs": true, "wake": true, "sleep": true, "stop": true, "typed": true,
	"mind_error": true, "no_reply": true, "first_audio": true, "said": true, "plugin": true, "plugin_error": true,
	"persona": true, "ui": true, "surprises": true, "presence": true, "settings": true}

func (w *Web) Run(ctx context.Context, h plugin.Host) error {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServerFS(sub))
	for prefix, dir := range w.Static {
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		if strings.HasPrefix(dir, "~/") {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, dir[2:])
		}
		mux.Handle("GET "+prefix, http.StripPrefix(prefix, http.FileServer(http.Dir(dir))))
	}
	w.facts = map[string]map[string]any{}
	evs0, cancel0 := h.Subscribe() // the sticky kinds' last events
	go func() {
		defer cancel0()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-evs0:
				if !ok {
					return
				}
				k, _ := e["ev"].(string)
				w.mu.Lock()
				if sticky[k] {
					w.facts[k] = e
				}
				if recentKinds[k] {
					r := map[string]any{"at": time.Now().Unix()}
					for kk, v := range e {
						r[kk] = v
					}
					w.recent = append(w.recent, r)
					if len(w.recent) > 80 {
						w.recent = w.recent[len(w.recent)-80:]
					}
				}
				w.mu.Unlock()
			}
		}
	}()
	mux.HandleFunc("GET /api/recent", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		out := append([]map[string]any{}, w.recent...)
		w.mu.Unlock()
		writeJSON(rw, out)
	})
	mux.HandleFunc("GET /api/state", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		facts := map[string]any{}
		for k, v := range w.facts {
			facts[k] = v
		}
		w.mu.Unlock()
		writeJSON(rw, map[string]any{"prefs": h.Prefs(), "status": h.Status(), "choices": h.Choices(), "facts": facts})
	})
	mux.HandleFunc("POST /api/prefs", func(rw http.ResponseWriter, r *http.Request) {
		var patch map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		// the patch is checked on a copy first: a bad one changes nothing
		apply := func(p *config.Prefs) error {
			b, _ := json.Marshal(p)
			var m map[string]json.RawMessage
			json.Unmarshal(b, &m)
			for k, v := range patch {
				if _, ok := m[k]; !ok {
					return fmt.Errorf("no preference %q", k)
				}
				m[k] = v
			}
			b, _ = json.Marshal(m)
			return json.Unmarshal(b, p)
		}
		trial := h.Prefs()
		if err := apply(&trial); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		p, err := h.SetPrefs(func(p *config.Prefs) { apply(p) })
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(rw, p)
	})
	mux.HandleFunc("POST /api/do", func(rw http.ResponseWriter, r *http.Request) {
		var a struct{ Action, Arg string }
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		if err := h.Do(a.Action, a.Arg); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(rw, h.Status())
	})
	mux.HandleFunc("GET /api/events", func(rw http.ResponseWriter, r *http.Request) {
		fl, ok := rw.(http.Flusher)
		if !ok {
			http.Error(rw, "no streaming", http.StatusInternalServerError)
			return
		}
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.Header().Set("Cache-Control", "no-cache")
		evs, cancel := h.Subscribe()
		defer cancel()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		var last plugin.Status
		send := func(kind string, v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", kind, b)
			fl.Flush()
		}
		send("status", h.Status())
		for {
			select {
			case <-r.Context().Done():
				return
			case e, ok := <-evs:
				if !ok {
					return
				}
				if k, _ := e["ev"].(string); shown[k] {
					send("ev", e)
				}
			case <-tick.C:
				if s := h.Status(); s != last {
					last = s
					send("status", s)
				}
			}
		}
	})
	ln, err := net.Listen("tcp", w.Addr)
	if err != nil {
		return err
	}
	h.Log("web", map[string]any{"addr": ln.Addr().String()})
	srv := &http.Server{Handler: w.cors(mux)}
	go func() { <-ctx.Done(); srv.Close() }()
	return srv.Serve(ln)
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(v)
}
