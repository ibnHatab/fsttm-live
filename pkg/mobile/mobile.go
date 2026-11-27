// Package mobile is the voice's Android bridge (gomobile bind): the app's Java side feeds the
// mic's PCM in (Mic) and plays what comes back (Speaker); a page on 127.0.0.1:8770 is what the
// app's WebView shows. A persona embedded in the app registers its own checks (Register) from its
// package's init, so the wrapper knows no persona: it is bound together with one
// (gomobile bind github.com/ibnHatab/fsttm-live/pkg/mobile <persona's package>).
//
// Proof stage (2026-11-27): a level meter, a 3 s echo (mic → Go → speaker), the persona's checks,
// and the Go heap, to see what runs and what it costs on a 1 GB tablet.
package mobile

import (
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
)

// Rate is the PCM the bridge carries: 16 kHz, mono, s16le (the dialog's).
const Rate = 16000

// Speaker is implemented in Java (an AudioTrack): Play gets s16le mono PCM at Rate.
type Speaker interface {
	Play(pcm []byte)
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

// Start serves the page on 127.0.0.1:8770 and returns its URL; files is the app's files directory.
func Start(files string, sp Speaker) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if srv != nil {
		return "http://127.0.0.1:8770/", nil
	}
	dir, speaker = files, sp
	ln, err := net.Listen("tcp", "127.0.0.1:8770")
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
	return "http://127.0.0.1:8770/", nil
}

// Stop ends the page's server.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if srv != nil {
		srv.Close()
		srv = nil
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
