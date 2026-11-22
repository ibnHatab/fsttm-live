// Package audio is the runtime's sound: the microphone (arecord), the local endpointer
// (WebRTC VAD) and the speaker (aplay), all as plain processes so the same binary runs on a
// Raspberry Pi.
package audio

import (
	"encoding/binary"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

const (
	Rate      = 16000
	FrameLen  = 320 // 20 ms at 16 kHz, in samples
	frameByte = FrameLen * 2
)

// Frame is 20 ms of the speech channel and the level of the echo reference channel (the
// ReSpeaker's channel 5 is what the array is playing: non-zero means its echo canceller has a
// reference).
type Frame struct {
	PCM []byte // s16le mono, 16 kHz
	Ref int    // RMS of the reference channel, 0 if none
}

// Capture runs arecord and sends frames until the process ends.
type Capture struct {
	Device   string // ALSA device (hw:ArrayUAC10), or pulse:SOURCE
	Channels int    // 6 for the ReSpeaker (firmware 6ch), 1 for a plain mic
	Speech   int    // the channel to keep (0: the ReSpeaker's processed speech)
	Ref      int    // the echo reference channel, -1 for none
	cmd      *exec.Cmd
}

func (c *Capture) Run(out chan<- Frame) error {
	if src, ok := strings.CutPrefix(c.Device, "pulse:"); ok { // a PulseAudio source, e.g. an echo canceller's
		c.cmd = exec.Command("parec", "-d", src, "--rate="+strconv.Itoa(Rate), "--channels="+strconv.Itoa(c.Channels),
			"--format=s16le", "--latency-msec=20")
	} else {
		c.cmd = exec.Command("arecord", "-q", "-D", c.Device, "-c", strconv.Itoa(c.Channels), "-r", strconv.Itoa(Rate), "-f", "S16_LE", "-t", "raw")
	}
	r, err := c.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := c.cmd.Start(); err != nil {
		return err
	}
	defer c.cmd.Wait()
	buf := make([]byte, frameByte*c.Channels)
	stride := 2 * c.Channels
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return err
		}
		f := Frame{PCM: make([]byte, frameByte)}
		var ref float64
		for i := 0; i < FrameLen; i++ {
			copy(f.PCM[2*i:], buf[i*stride+2*c.Speech:i*stride+2*c.Speech+2])
			if c.Ref >= 0 {
				v := float64(int16(binary.LittleEndian.Uint16(buf[i*stride+2*c.Ref:])))
				ref += v * v
			}
		}
		if c.Ref >= 0 {
			f.Ref = int(math.Sqrt(ref / FrameLen))
		}
		out <- f
	}
}

func (c *Capture) Stop() {
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
}

// RMS of s16le PCM.
func RMS(b []byte) int {
	var s float64
	n := len(b) / 2
	if n == 0 {
		return 0
	}
	for i := 0; i+1 < len(b); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(b[i:])))
		s += v * v
	}
	return int(math.Sqrt(s / float64(n)))
}
