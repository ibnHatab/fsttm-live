// remote-probe: a phone for Nina's remote audio (/api/audio), from the command line: the
// microphone's on and her voice here, a WAV (16 kHz mono s16le) streamed in real time as what the
// phone hears, then her answer counted (and written to -out as a 24 kHz WAV).
//
//	remote-probe -url wss://nina-home.tail2ffd4d.ts.net/api/audio -wav question.wav -wait 15s -out answer.wav
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	url := flag.String("url", "ws://127.0.0.1:8770/api/audio", "her panel's /api/audio")
	wav := flag.String("wav", "", "what the phone's microphone hears (16 kHz mono s16le WAV)")
	wait := flag.Duration("wait", 15*time.Second, "how long to listen for her after it")
	out := flag.String("out", "", "her voice, as a 24 kHz WAV")
	flag.Parse()
	c, _, err := websocket.DefaultDialer.Dial(*url, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer c.Close()
	on, _ := json.Marshal(map[string]bool{"mic": true, "voice": true})
	c.WriteMessage(websocket.TextMessage, on)
	got, cuts, first := []byte{}, 0, time.Time{}
	t0 := time.Now()
	go func() {
		for {
			mt, d, err := c.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.TextMessage {
				cuts++
				continue
			}
			if first.IsZero() {
				first = time.Now()
			}
			got = append(got, d...)
		}
	}()
	var sent time.Time
	if *wav != "" {
		b, err := os.ReadFile(*wav)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		b = b[44:]
		silence := make([]byte, 640)
		for i := 0; i < 50; i++ { // a second of quiet first
			c.WriteMessage(websocket.BinaryMessage, silence)
			time.Sleep(20 * time.Millisecond)
		}
		for i := 0; i+640 <= len(b); i += 640 {
			c.WriteMessage(websocket.BinaryMessage, b[i:i+640])
			time.Sleep(20 * time.Millisecond)
		}
		sent = time.Now()
		for i := 0; i < int(*wait/(20*time.Millisecond)); i++ { // quiet while she answers
			c.WriteMessage(websocket.BinaryMessage, silence)
			time.Sleep(20 * time.Millisecond)
		}
	} else {
		time.Sleep(*wait)
	}
	off, _ := json.Marshal(map[string]bool{"mic": false, "voice": false})
	c.WriteMessage(websocket.TextMessage, off)
	time.Sleep(300 * time.Millisecond)
	lat := "-"
	if !first.IsZero() && !sent.IsZero() {
		lat = first.Sub(sent).Round(10 * time.Millisecond).String()
	}
	fmt.Printf("her voice: %.1f s received, first %s after the question ended, cuts %d, session %s\n",
		float64(len(got)/2)/24000, lat, cuts, time.Since(t0).Round(time.Second))
	if *out != "" && len(got) > 0 {
		h := make([]byte, 44)
		copy(h, "RIFF")
		binary.LittleEndian.PutUint32(h[4:], uint32(36+len(got)))
		copy(h[8:], "WAVEfmt ")
		binary.LittleEndian.PutUint32(h[16:], 16)
		binary.LittleEndian.PutUint16(h[20:], 1)
		binary.LittleEndian.PutUint16(h[22:], 1)
		binary.LittleEndian.PutUint32(h[24:], 24000)
		binary.LittleEndian.PutUint32(h[28:], 48000)
		binary.LittleEndian.PutUint16(h[32:], 2)
		binary.LittleEndian.PutUint16(h[34:], 16)
		copy(h[36:], "data")
		binary.LittleEndian.PutUint32(h[40:], uint32(len(got)))
		os.WriteFile(*out, append(h, got...), 0o644)
	}
}
