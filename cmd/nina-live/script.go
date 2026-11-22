package main

// The acoustic test driver: the "person" is a WAV played from the PC's speaker into the
// room, heard by the ReSpeaker like a real voice. One command per line:
//
//	play FILE            wait until the floor is free and quiet for -settle, then play it
//	barge SECONDS FILE   wait until Nina speaks, SECONDS later play FILE over her
//	type TEXT            the person's words, typed
//	note LEVEL TEXT      a NOTE from SOURCES
//	wait SECONDS         pause
//	idle                 wait until the floor is free and quiet
//	# comment

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/dialog"
	"github.com/ibnHatab/fsttm-live/internal/evlog"
)

func runScript(ctx context.Context, path string, eng *dialog.Engine, L *evlog.Log) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dir := filepath.Dir(path)
	time.Sleep(2 * time.Second) // the sessions and the mic settle
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fs := strings.Fields(line)
		L.Ev("script", map[string]any{"line": line})
		switch fs[0] {
		case "play":
			if err := waitIdle(ctx, eng, 1500*time.Millisecond, 40*time.Second); err != nil {
				return err
			}
			play(L, filepath.Join(dir, fs[1]))
		case "barge":
			d, _ := strconv.ParseFloat(fs[1], 64)
			deadline := time.Now().Add(20 * time.Second)
			for eng.SpeakingSince().IsZero() {
				if time.Now().After(deadline) {
					return fmt.Errorf("barge: Nina never spoke")
				}
				time.Sleep(20 * time.Millisecond)
			}
			time.Sleep(time.Duration(d * float64(time.Second)))
			play(L, filepath.Join(dir, fs[2]))
		case "type":
			eng.Typed <- strings.TrimSpace(strings.TrimPrefix(line, "type"))
		case "note":
			eng.Typed <- "/" + line
		case "wait":
			d, _ := strconv.ParseFloat(fs[1], 64)
			time.Sleep(time.Duration(d * float64(time.Second)))
		case "idle":
			if err := waitIdle(ctx, eng, 1500*time.Millisecond, 40*time.Second); err != nil {
				return err
			}
		default:
			return fmt.Errorf("script: %q", line)
		}
	}
	return waitIdle(ctx, eng, 3*time.Second, 40*time.Second)
}

func waitIdle(ctx context.Context, eng *dialog.Engine, settle, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	quietSince := time.Time{}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if eng.Idle() {
			if quietSince.IsZero() {
				quietSince = time.Now()
			}
			if time.Since(quietSince) >= settle {
				return nil
			}
		} else {
			quietSince = time.Time{}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("never idle for %s", settle)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func play(L *evlog.Log, file string) {
	L.Ev("play", map[string]any{"file": filepath.Base(file)})
	args := []string{file}
	if *personSink != "" { // not the echo canceller's sink: the person is not Nina's echo
		args = []string{"-d", *personSink, file}
	}
	exec.Command("paplay", args...).Run()
	L.Ev("play_end", map[string]any{"file": filepath.Base(file)})
}
