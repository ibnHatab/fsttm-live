// Package rhvoice is the local voice: RHVoice (RHVoice-test), 24 kHz mono like the Live
// API's audio, ~90 ms for a sentence on a laptop. Gemini's TTS models allow 10 requests a day
// on the free tier, so on a small machine the spoken words come either from the Live voice
// session's own audio or from RHVoice speaking that session's transcript.
package rhvoice

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Voice struct {
	Profile string // slt, clb, alan, bdl
	Rate    int    // speech rate percent (0: default)
}

// Say returns s16le mono PCM at 24 kHz.
func (v Voice) Say(text string) ([]byte, error) {
	f, err := os.CreateTemp("", "rhvoice-*.wav")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	args := []string{"-p", v.Profile, "-R", "24000", "-o", name}
	if v.Rate > 0 {
		args = append(args, "-r", fmt.Sprint(v.Rate))
	}
	cmd := exec.Command("RHVoice-test", args...)
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("RHVoice-test: %v: %s", err, bytes.TrimSpace(out))
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return wavData(b)
}

func wavData(b []byte) ([]byte, error) {
	for i := 12; i+8 <= len(b); {
		id := string(b[i : i+4])
		n := int(binary.LittleEndian.Uint32(b[i+4:]))
		if id == "data" {
			return b[i+8 : min(len(b), i+8+n)], nil
		}
		i += 8 + n + n%2
	}
	return nil, fmt.Errorf("rhvoice: no data chunk")
}
