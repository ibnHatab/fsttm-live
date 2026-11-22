// Package evlog writes the runtime's events as JSON lines: one object per event, with its
// kind ("ev") and its time in ms since the start ("t"). Plugins subscribe to the same stream.
package evlog

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

type Log struct {
	mu   sync.Mutex
	t0   time.Time
	out  []io.Writer
	subs map[chan map[string]any]bool
	// Quiet kinds go to subscribers only (the ears' interim text: the panel's, not the file's)
	Quiet map[string]bool
}

func New(out ...io.Writer) *Log {
	return &Log{t0: time.Now(), out: out, subs: map[chan map[string]any]bool{}, Quiet: map[string]bool{}}
}

// Ev logs one event; f may be nil. Safe from any goroutine; a nil *Log logs nothing.
func (l *Log) Ev(kind string, f map[string]any) {
	if l == nil {
		return
	}
	m := map[string]any{"ev": kind, "t": time.Since(l.t0).Milliseconds()}
	for k, v := range f {
		m[k] = v
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.Quiet[kind] {
		b, _ := json.Marshal(m)
		for _, w := range l.out {
			fmt.Fprintln(w, string(b))
		}
	}
	for c := range l.subs {
		select {
		case c <- m:
		default: // a slow subscriber loses events rather than stalling the dialog
		}
	}
}

// Subscribe returns the event stream and its cancel.
func (l *Log) Subscribe() (<-chan map[string]any, func()) {
	c := make(chan map[string]any, 256)
	l.mu.Lock()
	l.subs[c] = true
	l.mu.Unlock()
	return c, func() {
		l.mu.Lock()
		if l.subs[c] {
			delete(l.subs, c)
			close(c)
		}
		l.mu.Unlock()
	}
}
