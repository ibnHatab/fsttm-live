// Package ld2410 reads the HLK-LD2410C mmWave radar's report frames (UART, 256000 baud): the
// target state, the moving and the stationary target's distance (cm) and energy (0–100), and the
// module's combined detection distance. Distance only: the LD2410 gives no direction.
//
// A report frame: F4 F3 F2 F1, the data's length (2 bytes, little endian), the data (type 0x02
// basic or 0x01 engineering, 0xAA, the fields, 0x55 0x00), F8 F7 F6 F5. Written for Nina (2026-11-24)
// instead of github.com/asger-noer/ld2410c, which needs Go 1.27 and a UART adapter (the Pi has 1.26).
package ld2410

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// Frame is one report.
type Frame struct {
	Target                   uint8  // 0 none, 1 moving, 2 stationary, 3 both; 4–6 calibrating
	MovingCM, StationaryCM   uint16 // distances in cm
	MovingEnergy, StatEnergy uint8  // 0–100
	DetectionCM              uint16 // the module's combined range to the nearest target
	Engineering              bool   // the frame carried per-gate energies (not decoded)
}

// Detected: a person (moving, stationary or both); the calibration codes 4–6 are not.
func (f Frame) Detected() bool { return f.Target >= 1 && f.Target <= 3 }

// Moving and Stationary are the target state's two flags.
func (f Frame) Moving() bool     { return f.Target == 1 || f.Target == 3 }
func (f Frame) Stationary() bool { return f.Target == 2 || f.Target == 3 }

// Nearest is the distance to the nearest target, in cm (0 when none).
func (f Frame) Nearest() uint16 {
	if !f.Detected() {
		return 0
	}
	if f.DetectionCM > 0 {
		return f.DetectionCM
	}
	d := uint16(0)
	if f.Moving() {
		d = f.MovingCM
	}
	if f.Stationary() && (d == 0 || f.StationaryCM < d) {
		d = f.StationaryCM
	}
	return d
}

var head = []byte{0xF4, 0xF3, 0xF2, 0xF1}
var foot = []byte{0xF8, 0xF7, 0xF6, 0xF5}

// Parse is a report frame's data (between the length and the footer).
func Parse(data []byte) (Frame, error) {
	if len(data) < 13 {
		return Frame{}, fmt.Errorf("ld2410: short report (%d bytes)", len(data))
	}
	if data[0] != 0x01 && data[0] != 0x02 {
		return Frame{}, fmt.Errorf("ld2410: report type %#x", data[0])
	}
	if data[1] != 0xAA || data[len(data)-2] != 0x55 {
		return Frame{}, fmt.Errorf("ld2410: bad report markers")
	}
	le := binary.LittleEndian
	return Frame{Target: data[2], MovingCM: le.Uint16(data[3:]), MovingEnergy: data[5],
		StationaryCM: le.Uint16(data[6:]), StatEnergy: data[8], DetectionCM: le.Uint16(data[9:]),
		Engineering: data[0] == 0x01}, nil
}

// Scan reads report frames from r (the serial port) until it fails; out gets each one. Anything
// else on the line (command acks, noise) is skipped by syncing to the next header.
func Scan(r io.Reader, out func(Frame)) error {
	br := bufio.NewReaderSize(r, 4096)
	win := make([]byte, 0, 4)
	for {
		b, err := br.ReadByte()
		if err != nil {
			return err
		}
		win = append(win, b)
		if len(win) > 4 {
			win = win[1:]
		}
		if !bytes.Equal(win, head) {
			continue
		}
		win = win[:0]
		var lb [2]byte
		if _, err := io.ReadFull(br, lb[:]); err != nil {
			return err
		}
		n := int(binary.LittleEndian.Uint16(lb[:]))
		if n > 1024 {
			continue // not a report: resync
		}
		data := make([]byte, n+4)
		if _, err := io.ReadFull(br, data); err != nil {
			return err
		}
		if !bytes.Equal(data[n:], foot) {
			continue
		}
		if f, err := Parse(data[:n]); err == nil {
			out(f)
		}
	}
}
