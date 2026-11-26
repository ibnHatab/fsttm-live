// wake-cycle: sleep and wake, tested live and silently (2026-11-26; the user: "where you have TTS
// running in Pascal … send this directly over socket … decide this go to sleep, wake up stuff").
//
// "The person" is Paradee's voice (Pascal's TTS socket) sent into her panel's remote microphone
// (/api/audio, as her screen on a phone does); her voice comes back over the same socket, so the
// room hears nothing. Her event stream (/api/events) says what happened:
//
//	name → kws → bare_name → turn → first_audio → reply_done (her words);  "Go to sleep." → asleep
//
// Per phrase: from the phrase's end to her first sound, her words, awake or asleep as wanted.
//
//	wake-cycle [-panel http://pero.local:8770] [-cycles 3] [-voice am_puck]
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type ev struct {
	at time.Time
	m  map[string]any
}

var (
	mu     sync.Mutex
	events []ev
	voiceN int       // her voice's binary messages received
	voiceT time.Time // the last one's arrival
)

// paradee: a phrase in a Paradee voice, 16 kHz s16le, and its trailing quiet.
func paradee(text, voice string) ([]byte, time.Duration, error) {
	c, err := net.Dial("unix", filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "paradee-tts.sock"))
	if err != nil {
		return nil, 0, err
	}
	fmt.Fprintf(c, "1.00 %s\n%s", voice, text)
	c.(*net.UnixConn).CloseWrite()
	b, err := io.ReadAll(c)
	c.Close()
	if err != nil {
		return nil, 0, err
	}
	i := bytes.Index(b, []byte("data")) + 8
	n := (len(b) - i) / 2
	x := make([]float64, n)
	peak := 0.0
	for k := 0; k < n; k++ {
		x[k] = float64(int16(binary.LittleEndian.Uint16(b[i+2*k:])))
		peak = math.Max(peak, math.Abs(x[k]))
	}
	// 24 kHz → 16 kHz (two of every three, each the mean of its neighbours), at -18 dBFS peak-ish
	out := new(bytes.Buffer)
	g := 0.4 * 32767 / math.Max(peak, 1)
	last := 0
	for k := 0; 3*k/2+1 < n; k++ {
		j := 3 * k / 2
		v := (x[j] + x[j+1]) / 2 * g
		binary.Write(out, binary.LittleEndian, int16(v))
		if math.Abs(v) > 0.01*32767 {
			last = k
		}
	}
	tail := time.Duration(out.Len()/2-last) * time.Second / 16000
	return out.Bytes(), tail, nil
}

func stream(panel string) {
	for {
		r, err := http.Get(panel + "/api/events")
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		sc := bufio.NewScanner(r.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		kind := ""
		for sc.Scan() {
			l := sc.Text()
			switch {
			case strings.HasPrefix(l, "event:"):
				kind = strings.TrimSpace(l[6:])
			case strings.HasPrefix(l, "data:") && kind == "ev":
				var m map[string]any
				if json.Unmarshal([]byte(l[5:]), &m) == nil {
					mu.Lock()
					events = append(events, ev{time.Now(), m})
					mu.Unlock()
				}
			}
		}
		r.Body.Close()
	}
}

func status(panel string) map[string]any {
	r, err := http.Get(panel + "/api/state")
	if err != nil {
		return nil
	}
	defer r.Body.Close()
	var d struct {
		Status map[string]any `json:"status"`
	}
	json.NewDecoder(r.Body).Decode(&d)
	return d.Status
}

func idle(panel string, limit time.Duration) map[string]any {
	end := time.Now().Add(limit)
	for time.Now().Before(end) {
		st := status(panel)
		if st != nil && st["replying"] == false && st["speaking"] == false && st["pending"] == float64(0) {
			mu.Lock()
			quiet := time.Since(voiceT) > 800*time.Millisecond
			mu.Unlock()
			if quiet {
				return st
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return status(panel)
}

func main() {
	panel := flag.String("panel", "http://pero.local:8770", "her panel")
	cycles := flag.Int("cycles", 3, "name / go to sleep rounds")
	voice := flag.String("voice", "am_puck", "Paradee's voice")
	flag.Parse()
	ws := "ws" + strings.TrimPrefix(*panel, "http") + "/api/audio"
	go stream(*panel)
	c, _, err := websocket.DefaultDialer.Dial(ws, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer c.Close()
	var wmu sync.Mutex
	send := func(mt int, b []byte) { wmu.Lock(); c.WriteMessage(mt, b); wmu.Unlock() }
	go func() { // her voice back
		for {
			mt, _, err := c.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.BinaryMessage {
				mu.Lock()
				voiceN++
				voiceT = time.Now()
				mu.Unlock()
			}
		}
	}()
	// the microphone: quiet, and the phrases in it, in real time
	feed := make(chan []byte)
	done := make(chan time.Time)
	go func() {
		silence := make([]byte, 640)
		for {
			select {
			case b := <-feed:
				for i := 0; i+640 <= len(b); i += 640 {
					send(websocket.BinaryMessage, b[i:i+640])
					time.Sleep(20 * time.Millisecond)
				}
				done <- time.Now()
			default:
				send(websocket.BinaryMessage, silence)
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()
	on, _ := json.Marshal(map[string]bool{"mic": true, "voice": true})
	send(websocket.TextMessage, on)
	defer func() {
		off, _ := json.Marshal(map[string]bool{"mic": false, "voice": false})
		send(websocket.TextMessage, off)
		time.Sleep(300 * time.Millisecond)
	}()
	time.Sleep(2 * time.Second)
	idle(*panel, 20*time.Second)

	type result struct {
		Said    string   `json:"said"`
		KWS     bool     `json:"kws"`
		Bare    bool     `json:"bare"`
		ToVoice *float64 `json:"to_voice_s"`
		Reply   string   `json:"reply"`
		Awake   any      `json:"awake"`
		OK      bool     `json:"ok"`
		Connect []string `json:"connects,omitempty"`
	}
	phrase := func(text string, wantAwake bool) result {
		idle(*panel, 30*time.Second)
		time.Sleep(1500 * time.Millisecond)
		pcm, tail, err := paradee(text, *voice)
		if err != nil {
			fmt.Fprintln(os.Stderr, "paradee:", err)
			os.Exit(1)
		}
		mu.Lock()
		n0, v0 := len(events), voiceN
		mu.Unlock()
		feed <- pcm
		tEnd := (<-done).Add(-tail)
		deadline := time.Now().Add(14 * time.Second)
		var r result
		r.Said = text
		for time.Now().Before(deadline) {
			mu.Lock()
			got := append([]ev(nil), events[n0:]...)
			mu.Unlock()
			fin := false
			for _, e := range got {
				k, _ := e.m["ev"].(string)
				fin = fin || k == "reply_done" || (!wantAwake && (k == "asleep_local" || k == "asleep_ignored"))
			}
			if fin {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		time.Sleep(500 * time.Millisecond)
		mu.Lock()
		got := append([]ev(nil), events[n0:]...)
		vn := voiceN
		mu.Unlock()
		replied := false
		for _, e := range got {
			switch e.m["ev"] {
			case "kws":
				r.KWS = true
			case "bare_name":
				r.Bare = true
			case "first_audio":
				if r.ToVoice == nil {
					s := math.Round(e.at.Sub(tEnd).Seconds()*100) / 100
					r.ToVoice = &s
				}
			case "reply_done":
				r.Reply, _ = e.m["said"].(string)
				replied = true
			case "voice_connected":
				r.Connect = append(r.Connect, fmt.Sprintf("%vms resumed=%v warm=%v", e.m["ms"], e.m["resumed"], e.m["warm"]))
			}
		}
		_ = vn - v0
		st := idle(*panel, 30*time.Second)
		r.Awake = st["awake"]
		r.OK = st["awake"] == wantAwake && (replied || !wantAwake)
		b, _ := json.Marshal(r)
		fmt.Println(string(b))
		return r
	}
	var all []result
	all = append(all, phrase("Go to sleep.", false))
	for i := 0; i < *cycles; i++ {
		all = append(all, phrase("Nina.", true), phrase("Go to sleep.", false))
	}
	all = append(all, phrase("Hey Nina.", true), phrase("Go to sleep.", false),
		phrase("Nina, what time is it?", true), phrase("Thank you, go to sleep.", false))
	ok, lat := 0, []string{}
	for _, r := range all {
		if r.OK {
			ok++
		}
		if r.ToVoice != nil && (strings.HasPrefix(r.Said, "Nina") || strings.HasPrefix(r.Said, "Hey")) {
			lat = append(lat, fmt.Sprintf("%.2f", *r.ToVoice))
		}
	}
	fmt.Printf("%d/%d as wanted; the name's end → her first sound (s): %s\n", ok, len(all), strings.Join(lat, " "))
}
