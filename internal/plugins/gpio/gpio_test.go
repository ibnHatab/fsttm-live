package gpio

import (
	"sync"
	"testing"
	"time"
)

func TestKey(t *testing.T) {
	var mu sync.Mutex
	var got []string
	k := NewKey(Button{Line: 17, Press: "talk", Long: "sleep"}, 60*time.Millisecond, func(a, _ string) {
		mu.Lock()
		got = append(got, a)
		mu.Unlock()
	})
	k.Edge(true)
	time.Sleep(10 * time.Millisecond)
	k.Edge(false) // short
	k.Edge(true)
	time.Sleep(100 * time.Millisecond) // held: long fires while held
	k.Edge(false)
	k.Edge(false) // a bounce: nothing
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "talk" || got[1] != "sleep" {
		t.Fatalf("got %v", got)
	}
}

func TestCheck(t *testing.T) {
	g := &GPIO{Buttons: []Button{{Line: 1, Press: "dance"}}}
	if g.check() == nil {
		t.Fatal("unknown action accepted")
	}
	g = &GPIO{Buttons: []Button{{Line: 1, Press: "talk", Long: "sleep"}}, Lights: []Light{{Line: 5, Show: "awake"}}}
	if err := g.check(); err != nil {
		t.Fatal(err)
	}
}
