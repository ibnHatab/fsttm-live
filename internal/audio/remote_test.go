package audio

import (
	"sync"
	"testing"
	"time"
)

// Her voice to a remote listener: every byte arrives, paced in real time (done after the reply's
// length, not at once); a cut tells the listener to drop what it holds (sink(nil)).
func TestRemoteVoice(t *testing.T) {
	s := NewSpeaker("", 24000)
	var mu sync.Mutex
	got, cuts := 0, 0
	s.SetRemote(func(p []byte) {
		mu.Lock()
		defer mu.Unlock()
		if p == nil {
			cuts++
			return
		}
		got += len(p)
	})
	t0 := time.Now()
	s.Write(make([]byte, 24000)) // 0.5 s
	s.Finish()
	if ok := <-s.Done; !ok {
		t.Fatal("a played reply ended as cut")
	}
	if d := time.Since(t0); d < 400*time.Millisecond {
		t.Fatalf("done after %v: not paced", d)
	}
	mu.Lock()
	if got != 24000 {
		t.Fatalf("%d bytes to the listener, want 24000", got)
	}
	mu.Unlock()
	s.Write(make([]byte, 240000)) // 5 s
	time.Sleep(100 * time.Millisecond)
	s.Cut()
	if ok := <-s.Done; ok {
		t.Fatal("a cut reply ended as played")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if cuts != 1 {
		t.Fatalf("the listener was told %d cuts", cuts)
	}
}
