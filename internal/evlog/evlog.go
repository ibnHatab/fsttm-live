// Package evlog writes the runtime's events as JSON lines: one object per event, with its
// kind ("ev") and its time in ms since the start ("t").
package evlog

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

type Log struct {
	mu  sync.Mutex
	t0  time.Time
	out []io.Writer
}

func New(out ...io.Writer) *Log { return &Log{t0: time.Now(), out: out} }

// Ev logs one event; f may be nil. Safe from any goroutine; a nil *Log logs nothing.
func (l *Log) Ev(kind string, f map[string]any) {
	if l == nil {
		return
	}
	m := map[string]any{"ev": kind, "t": time.Since(l.t0).Milliseconds()}
	for k, v := range f {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, w := range l.out {
		fmt.Fprintln(w, string(b))
	}
}

// Func is the logging callback the packages take, so they need not import this one.
func (l *Log) Func() func(string, map[string]any) { return l.Ev }
