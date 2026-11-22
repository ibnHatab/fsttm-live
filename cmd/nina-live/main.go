// nina-live: Nina's dialog runtime on the Gemini Live API. The ReSpeaker (or any mic) feeds
// our endpointer; the ears (gemini-3.5-transcribe-live) turn each utterance into text; the
// floor machine and attention decide what reaches the voice+mind session
// (gemini-3.1-flash-live-preview), whose audio is played locally and cut on barge-in.
//
// Everything comes from one config file (-config; created from the default if missing): the
// machine's settings, the preferences (saved back when a plugin changes them), the plugins
// (web panel, GPIO buttons and lights; any number at once).
//
// Lines typed on stdin: plain text is the person's words; "/note LEVEL text" a NOTE from
// SOURCES; "/say text" spoken verbatim. -script runs an acoustic test (see script.go).
package main

import (
	"bufio"
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ibnHatab/fsttm-live/internal/config"
	"github.com/ibnHatab/fsttm-live/internal/evlog"
	"github.com/ibnHatab/fsttm-live/internal/nina"
	"github.com/ibnHatab/fsttm-live/internal/plugin"
	_ "github.com/ibnHatab/fsttm-live/internal/plugins/gpio"
	_ "github.com/ibnHatab/fsttm-live/internal/plugins/web"
)

var (
	cfgPath    = flag.String("config", nina.DefaultConfigPath(), "the config file")
	logFile    = flag.String("log", "", "JSONL event log (also on stdout)")
	script     = flag.String("script", "", "acoustic test script")
	quiet      = flag.Bool("q", false, "events only to the log file")
	personSink = flag.String("person-sink", "", "-script: the PulseAudio sink the person's WAVs play on")
	stdin      = flag.Bool("stdin", true, "read typed lines from stdin")
)

func main() {
	flag.Parse()
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	var outs []io.Writer
	if !*quiet {
		outs = append(outs, os.Stdout)
	}
	if *logFile != "" {
		f, err := os.Create(*logFile)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		outs = append(outs, f)
	}
	L := evlog.New(outs...)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	home := newHome()
	n, err := nina.New(ctx, cfg, L, nina.Tools{Decls: home.tools(), Funcs: home.funcs()})
	if err != nil {
		log.Fatal(err)
	}
	if err := plugin.Start(ctx, cfg, n); err != nil {
		log.Fatal(err)
	}
	if *stdin {
		go func() {
			sc := bufio.NewScanner(os.Stdin)
			for sc.Scan() {
				n.Engine.Typed <- sc.Text()
			}
		}()
	}
	if *script != "" {
		go func() {
			if err := runScript(ctx, *script, n.Engine, L); err != nil {
				L.Ev("script_error", map[string]any{"err": err.Error()})
			}
			stop()
		}()
	}
	if err := n.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
