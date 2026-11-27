// Package mobile is the voice's Android bridge (gomobile bind): the app's Java side feeds the
// mic's PCM in (Mic) and plays what comes back (Speaker); her panel (127.0.0.1:8770) is what the
// app's WebView shows. A persona embedded in the app registers its own checks (Register) from its
// package's init, so the wrapper knows no persona: it is bound together with one
// (gomobile bind github.com/ibnHatab/fsttm-live/pkg/mobile <persona's package>).
//
// The persona's dialogue (SetPersona) is fsttm-live's own: the app's mic is its remote microphone
// (RemoteFrame, on while the app is in front: Listen), her voice its remote listener (RemoteVoice,
// streamed to Speaker.Voice), her panel the web plugin's page on 127.0.0.1:8770. The proof page
// (127.0.0.1:8771: a level meter, a 3 s echo, the persona's checks, the Go heap) stays for checking.
package mobile

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/ibnHatab/fsttm-live/pkg/dialogue"
)

// Rate is the PCM the bridge carries: 16 kHz, mono, s16le (the dialog's).
const Rate = 16000

// Speaker is implemented in Java (AudioTracks): Play gets s16le mono PCM at Rate (the echo, one
// piece); Voice her speech as it streams, s16le mono at VoiceRate; Cut drops what Voice holds (she
// was interrupted).
type Speaker interface {
	Play(pcm []byte)
	Voice(pcm []byte)
	Cut()
}

// VoiceRate is her voice's rate (the Live model's audio).
const VoiceRate = 24000

// Persona starts the embedded persona's dialogue (its plugins started: the panel), files the app's
// files directory; the bridge runs it, feeds it the mic and plays its voice.
type Persona func(ctx context.Context, files string) (*dialogue.Dialogue, error)

var (
	persona  Persona
	dlg      *dialogue.Dialogue
	status   = "no persona"
	listenOn bool
	cancel   context.CancelFunc
)

// SetPersona registers the embedded persona (from its package's init).
func SetPersona(p Persona) {
	mu.Lock()
	defer mu.Unlock()
	persona, status = p, "not started"
}

// panelURL is the page the app opens once the persona runs: the web plugin's own panel unless the
// persona has a screen of its own (SetPanel: Nina's face, served from the plugin's static files).
var panelURL = "http://127.0.0.1:8770/"

// SetPanel sets the page the app shows once the persona runs (from its package's init).
func SetPanel(url string) {
	mu.Lock()
	defer mu.Unlock()
	panelURL = url
}

var (
	mu      sync.Mutex
	checks  = map[string]func(dir string) string{}
	dir     string
	speaker Speaker
	srv     *http.Server
	level   struct {
		RMS, Peak float64
		Frames    int64
		At        time.Time
	}
	rec struct {
		want int
		buf  []byte
		done chan struct{}
	}
)

// Register adds a persona's check, shown as a button on the page (called from its init).
func Register(name string, fn func(dir string) string) {
	mu.Lock()
	defer mu.Unlock()
	checks[name] = fn
}

// Start serves the proof page on 127.0.0.1:8771, starts the persona, and returns the page's URL;
// files is the app's files directory, tz the device's time zone (Go on Android is in UTC unless
// told: she said "five past six" at 20:05, 2026-11-27).
func Start(files, tz string, sp Speaker) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if srv != nil {
		return "http://127.0.0.1:8771/", nil
	}
	if loc, err := time.LoadLocation(tz); err == nil && tz != "" {
		time.Local = loc
	}
	dir, speaker = files, sp
	ln, err := net.Listen("tcp", "127.0.0.1:8771")
	if err != nil {
		return "", err
	}
	m := http.NewServeMux()
	m.HandleFunc("/", page)
	m.HandleFunc("/api/level", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		l := level
		mu.Unlock()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		writeJSON(w, map[string]any{"rms": l.RMS, "peak": l.Peak, "frames": l.Frames, "live": time.Since(l.At) < time.Second,
			"heap_mb": float64(ms.HeapAlloc) / 1e6, "sys_mb": float64(ms.Sys) / 1e6, "goroutines": runtime.NumGoroutine()})
	})
	m.HandleFunc("/api/echo", echo)
	m.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		writeJSON(w, map[string]any{"status": status, "panel": panelURL, "listening": listenOn})
	})
	m.HandleFunc("/api/checks", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		var names []string
		for n := range checks {
			names = append(names, n)
		}
		mu.Unlock()
		sort.Strings(names)
		writeJSON(w, names)
	})
	m.HandleFunc("/api/check", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fn := checks[r.URL.Query().Get("name")]
		d := dir
		mu.Unlock()
		if fn == nil {
			http.Error(w, "no such check", 404)
			return
		}
		writeJSON(w, map[string]string{"result": fn(d)})
	})
	srv = &http.Server{Handler: m}
	go srv.Serve(ln)
	if persona != nil {
		ctx, c := context.WithCancel(context.Background())
		cancel, status = c, "starting"
		go run(ctx, persona, files, sp)
	}
	return "http://127.0.0.1:8771/", nil
}

// run starts the persona's dialogue and runs it until the app stops it.
func run(ctx context.Context, p Persona, files string, sp Speaker) {
	d, err := p(ctx, files)
	mu.Lock()
	if err != nil {
		status = "error: " + err.Error()
		mu.Unlock()
		return
	}
	dlg, status = d, "running"
	on := listenOn
	mu.Unlock()
	d.RemoteVoice(func(pcm []byte) {
		if pcm == nil {
			sp.Cut()
			return
		}
		sp.Voice(pcm)
	})
	if on {
		go func() { time.Sleep(time.Second); d.RemoteMic(true) }() // once its engine runs
	}
	err = d.Run(ctx)
	mu.Lock()
	dlg = nil
	if err != nil {
		status = "error: " + err.Error()
	} else {
		status = "stopped"
	}
	mu.Unlock()
}

// Listen: the app in front (on) or not: the persona hears the mic only while it is in front.
func Listen(on bool) {
	mu.Lock()
	listenOn = on
	d := dlg
	mu.Unlock()
	if d != nil {
		d.RemoteMic(on)
	}
}

// Status is the persona's state: no persona, not started, starting, running, stopped, error: ….
func Status() string { mu.Lock(); defer mu.Unlock(); return status }

// Stop ends the page's server.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if srv != nil {
		srv.Close()
		srv = nil
	}
	if cancel != nil {
		cancel()
		cancel = nil
	}
}

// Mic takes a chunk of the mic's PCM (s16le mono at Rate): its level, and the echo's recording.
func Mic(pcm []byte) {
	n := len(pcm) / 2
	if n == 0 {
		return
	}
	var sum, peak float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
		sum += v * v
		peak = math.Max(peak, math.Abs(v))
	}
	mu.Lock()
	d := dlg
	if rec.want > 0 { // not while the echo records
		d = nil
	}
	mu.Unlock()
	if d != nil { // the persona hears it, in order
		d.RemoteFrame(pcm)
	}
	mu.Lock()
	defer mu.Unlock()
	level.RMS, level.Peak, level.Frames, level.At = math.Sqrt(sum/float64(n)), peak, level.Frames+int64(n), time.Now()
	if rec.want > 0 {
		rec.buf = append(rec.buf, pcm...)
		if len(rec.buf) >= rec.want {
			rec.want = 0
			close(rec.done)
		}
	}
}

// echo records 3 s from the mic and plays it back through the speaker.
func echo(w http.ResponseWriter, _ *http.Request) {
	mu.Lock()
	if rec.want > 0 {
		mu.Unlock()
		http.Error(w, "already recording", 409)
		return
	}
	rec.want, rec.buf, rec.done = 3*Rate*2, nil, make(chan struct{})
	done, sp := rec.done, speaker
	mu.Unlock()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		mu.Lock()
		rec.want = 0
		mu.Unlock()
		http.Error(w, "no mic audio (is the app in front, the mic permitted?)", 504)
		return
	}
	mu.Lock()
	pcm := rec.buf
	mu.Unlock()
	if sp != nil {
		sp.Play(pcm)
	}
	writeJSON(w, map[string]any{"played_s": float64(len(pcm)) / 2 / Rate})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, pageHTML)
}
