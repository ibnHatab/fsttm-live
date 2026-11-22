// Package paradee is a local voice on a desktop: the Paradee service of xsel-to-speech
// (paradee-tts.socket, the model kept loaded by systemd), spoken to over its Unix socket. A
// request is one line "SPEED VOICE", then the text; closing the sending side ends it; a WAV
// (24 kHz s16le mono) streams back. ~0.13 s for 7 s of speech on Pascal.
package paradee

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

var Voices = []string{"af_heart", "am_puck", "bf_isabella", "bm_fable"}

type Voice struct {
	Socket string  // default $XDG_RUNTIME_DIR/paradee-tts.sock
	Name   string  // af_heart, am_puck, …
	Speed  float64 // 1.0 normal
}

func DefaultSocket() string {
	return filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "paradee-tts.sock")
}

// Available: the socket exists (systemd starts the service on the first connection).
func Available() bool {
	fi, err := os.Stat(DefaultSocket())
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// Say returns s16le mono PCM at 24 kHz.
func (v Voice) Say(text string) ([]byte, error) {
	sock := v.Socket
	if sock == "" {
		sock = DefaultSocket()
	}
	speed := v.Speed
	if speed <= 0 {
		speed = 1
	}
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("paradee: %w", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := fmt.Fprintf(c, "%.2f %s\n%s", speed, v.Name, text); err != nil {
		return nil, err
	}
	c.(*net.UnixConn).CloseWrite()
	b, err := io.ReadAll(c)
	if err != nil {
		return nil, err
	}
	// a streamed WAV: its sizes are unknown when the header is written, so the data
	// starts after the "data" chunk header whatever length it claims
	for i := 12; i+8 <= len(b); {
		if string(b[i:i+4]) == "data" {
			return b[i+8:], nil
		}
		n := int(uint32(b[i+4]) | uint32(b[i+5])<<8 | uint32(b[i+6])<<16 | uint32(b[i+7])<<24)
		i += 8 + n + n%2
	}
	return nil, fmt.Errorf("paradee: no audio (%d bytes)", len(b))
}
