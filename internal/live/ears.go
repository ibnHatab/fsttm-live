// Package live holds the two Gemini Live sessions: the ears (a transcription session under
// our endpointing) and the voice (a native-audio session fed text turns).
package live

import (
	"context"
	"os"
	"strings"
	"time"
	"unicode"

	"google.golang.org/genai"
)

// NewClient reads the AI Studio key from a file (never from a config) and opens a v1beta
// client, the version the Live API needs.
func NewClient(ctx context.Context, keyFile string) (*genai.Client, error) {
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	return genai.NewClient(ctx, &genai.ClientConfig{APIKey: strings.TrimSpace(string(key)),
		Backend: genai.BackendGeminiAPI, HTTPOptions: genai.HTTPOptions{APIVersion: "v1beta"}})
}

// Heard is the ears' text for one utterance (Seq, given at Begin). Interim texts come while
// it is spoken; Final once after its end; Late if more text came after the final.
type Heard struct {
	Seq   int
	Text  string
	Final bool
	Late  bool
	// Interim, on the final: the last interim text, when the final differs (the log's)
	Interim string
	// Refused, on an empty final: the transcriber answered instead of transcribing (Refusal): someone
	// spoke, the words are not known
	Refused bool
}

// Ears is gemini-3.5-transcribe-live in manual activity: we send activityStart, the
// utterance's audio and activityEnd. The model only sends interim transcripts, accumulated
// over the whole session without separators, so the ears cut each utterance's part out of it
// and decide when it is final: after the end, at sentence punctuation that held for Settle,
// or at FinalWait with whatever there is.
type Ears struct {
	Client    *genai.Client
	Model     string
	Vocab     []string
	Settle    time.Duration
	FinalWait time.Duration
	MaxAge    time.Duration // reconnect between utterances after this (sessions last 10 min)
	Out       chan<- Heard
	Log       func(string, map[string]any)

	cmds chan earsCmd
}

type earsCmd struct {
	kind int // 0 begin, 1 audio, 2 end
	seq  int
	pcm  []byte
}

type earsText struct {
	gen  int
	text string
	err  error
}

func (e *Ears) Begin(seq int)    { e.cmds <- earsCmd{kind: 0, seq: seq} }
func (e *Ears) Audio(pcm []byte) { e.cmds <- earsCmd{kind: 1, pcm: pcm} }
func (e *Ears) End()             { e.cmds <- earsCmd{kind: 2} }

// Close ends the session (the mic is off); the next utterance opens a new one.
func (e *Ears) Close() { e.cmds <- earsCmd{kind: 3} }

func (e *Ears) Start(ctx context.Context) {
	e.cmds = make(chan earsCmd, 1024)
	go e.loop(ctx)
}

func (e *Ears) connect(ctx context.Context, gen int, texts chan<- earsText) (*genai.Session, error) {
	cfg := &genai.LiveConnectConfig{
		ResponseModalities:      []genai.Modality{genai.ModalityText},
		InputAudioTranscription: &genai.AudioTranscriptionConfig{CustomVocabulary: e.Vocab},
		RealtimeInputConfig: &genai.RealtimeInputConfig{
			AutomaticActivityDetection: &genai.AutomaticActivityDetection{Disabled: true}},
	}
	t := time.Now()
	s, err := e.Client.Live.Connect(ctx, e.Model, cfg)
	if err != nil {
		return nil, err
	}
	e.Log("ears_connected", map[string]any{"ms": time.Since(t).Milliseconds(), "model": e.Model})
	go func() {
		for {
			m, err := s.Receive()
			if err != nil {
				texts <- earsText{gen: gen, err: err}
				return
			}
			if m.GoAway != nil {
				e.Log("ears_go_away", map[string]any{"left": m.GoAway.TimeLeft.String()})
			}
			sc := m.ServerContent
			if sc == nil {
				continue
			}
			for _, tr := range []*genai.Transcription{sc.InterimInputTranscription, sc.InputTranscription} {
				if tr != nil && tr.Text != "" {
					texts <- earsText{gen: gen, text: tr.Text}
				}
			}
		}
	}()
	return s, nil
}

// VocabEcho tells a transcript that is only the custom vocabulary read back ("Nina, Prana, lab,
// bedroom, …"): the model's answer to noise or speech it can't make out. Three or more entries
// and nothing else but "and" and punctuation.
func VocabEcho(s string, vocab []string) bool {
	rest := " " + strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			return unicode.ToLower(r)
		}
		return ' '
	}, s) + " "
	n := 0
	for _, v := range vocab { // longest first would matter only for nested entries
		w := " " + strings.ToLower(v) + " "
		for strings.Contains(rest, w) {
			rest = strings.Replace(rest, w, " ", 1)
			n++
		}
	}
	return n >= 3 && strings.Trim(strings.ReplaceAll(rest, " and ", " "), " ") == ""
}

// Refusal tells a transcript that is the transcriber answering instead of transcribing ("I'm just a
// language model and can't help with that.": 2026-11-26, "Nina, show me the album Nina's gallery."
// twice, no interim before it): not the person's words. Short, and a model's stock refusal.
func Refusal(s string) bool {
	l := strings.ToLower(strings.ReplaceAll(s, "’", "'"))
	if len(l) > 160 {
		return false
	}
	for _, p := range []string{"i'm just a language model", "i am just a language model", "i'm only a language model",
		"as a language model", "as an ai language model", "i'm an ai language model", "i'm a text-based ai",
		"i can't help with that, as i'm only", "i'm not able to help with that, as i'm only"} {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}

// Cut is the utterance's part of the session's text (full), its text at the utterance's start
// being base. The transcriber accumulated a session's utterances (base, then the new words after
// it), revising the end of what it had written now and then; since 2026-11-26 it begins each
// utterance anew: full shares at most a few letters with base ("Next video." after "Nina, what
// time is it?": "N"), and cut there had lost the first letters ("ext video."). fresh: anew.
func Cut(full, base string) (s string, fresh bool) {
	if strings.HasPrefix(full, base) {
		return strings.TrimSpace(full[len(base):]), false
	}
	n := 0
	for n < len(full) && n < len(base) && full[n] == base[n] {
		n++
	}
	if len(base)-n > 2 { // not a revision of base's last letters (its "?" rewritten "."): a new text ("Cancel the
		// timer." after "Cancel the alarm.": 11 letters shared, then other words)
		return strings.TrimSpace(full), true
	}
	return strings.TrimSpace(strings.TrimLeft(full[n:], " .,?!")), false // the revised end's punctuation is not the new words'
}

func (e *Ears) loop(ctx context.Context) {
	texts := make(chan earsText, 256)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var (
		sess       *genai.Session
		gen        int
		born       time.Time
		full, base string // the session's accumulated text; its length at this utterance's start
		seq        = -1
		open       bool // between Begin and End
		ended      time.Time
		changed    time.Time
		final      string
		finalSent  bool
		lastSeg    string
		lastEcho   string
		refused    bool // the utterance's text so far is a refusal
		freshMode  bool // this session's transcriber begins each utterance anew: its text is all of it
	)
	seg := func() string {
		var s string
		if freshMode {
			s = strings.TrimSpace(full)
		} else {
			var fresh bool
			if s, fresh = Cut(full, base); fresh {
				freshMode = true // anew: anew for the rest of this utterance (base kept: the next one cuts at full)
			}
		}
		refused = Refusal(s)
		if VocabEcho(s, e.Vocab) || refused {
			if s != lastEcho {
				lastEcho = s
				e.Log(map[bool]string{true: "vocab_echo", false: "ears_refusal"}[VocabEcho(s, e.Vocab)], map[string]any{"text": s})
			}
			return ""
		}
		return s
	}
	ensure := func() bool {
		if sess != nil && time.Since(born) < e.MaxAge {
			return true
		}
		if sess != nil {
			sess.Close()
		}
		gen++
		s, err := e.connect(ctx, gen, texts)
		if err != nil {
			e.Log("ears_error", map[string]any{"err": err.Error()})
			sess = nil
			return false
		}
		sess, born, full, base, freshMode = s, time.Now(), "", "", false
		return true
	}
	emitFinal := func() {
		final, finalSent = seg(), true
		h := Heard{Seq: seq, Text: final, Final: true, Refused: final == "" && refused}
		if final != lastSeg {
			h.Interim = lastSeg
		}
		if final == "" && lastSeg != "" {
			// the transcriber took back what it had written (its accumulated text shrank or was
			// rewritten): what it heard while the person spoke stands
			final = lastSeg
			h.Text = final
			tail := func(s string) string { return s[max(0, len(s)-120):] }
			e.Log("final_from_interim", map[string]any{"seq": seq, "text": final,
				"base_len": len(base), "full_len": len(full), "base_tail": tail(base), "full_tail": tail(full)})
		}
		e.Out <- h
	}
	for {
		select {
		case <-ctx.Done():
			if sess != nil {
				sess.Close()
			}
			return
		case c := <-e.cmds:
			switch c.kind {
			case 0:
				if open && !finalSent {
					emitFinal()
				}
				if !ensure() {
					seq, open, finalSent = c.seq, false, true
					e.Out <- Heard{Seq: c.seq, Final: true} // nothing will be heard
					continue
				}
				// fresh mode is the utterance's, not the session's: once on for good, an accumulating
				// transcriber's later utterances each carried all the session's text before them (the
				// Fire 7, 2026-11-28: "Practically nothing… | Practically nothing… Maybe tomorrow…")
				seq, open, finalSent, lastSeg, base, refused, freshMode = c.seq, true, false, "", full, false, false
				ended, changed = time.Time{}, time.Now()
				sess.SendRealtimeInput(genai.LiveRealtimeInput{ActivityStart: &genai.ActivityStart{}})
			case 1:
				if open && sess != nil {
					sess.SendRealtimeInput(genai.LiveRealtimeInput{Audio: &genai.Blob{Data: c.pcm, MIMEType: "audio/pcm;rate=16000"}})
				}
			case 2:
				if open && sess != nil {
					sess.SendRealtimeInput(genai.LiveRealtimeInput{ActivityEnd: &genai.ActivityEnd{}})
				}
				open, ended = false, time.Now()
			case 3:
				if open && !finalSent {
					emitFinal()
				}
				if sess != nil {
					gen++ // its receiver's error is not news
					sess.Close()
					sess = nil
					e.Log("ears_session_closed", nil)
				}
				open = false
			}
		case t := <-texts:
			if t.gen != gen {
				continue
			}
			if t.err != nil {
				e.Log("ears_closed", map[string]any{"err": t.err.Error(), "age_s": time.Since(born).Seconds()})
				sess = nil
				if !finalSent && seq >= 0 {
					emitFinal()
				}
				continue
			}
			full = t.text
			s := seg()
			if s == lastSeg {
				continue
			}
			lastSeg, changed = s, time.Now()
			switch {
			case !finalSent:
				e.Out <- Heard{Seq: seq, Text: s}
			case s != final:
				final = s
				e.Out <- Heard{Seq: seq, Text: s, Final: true, Late: true}
			}
		case <-tick.C:
			if finalSent || seq < 0 || ended.IsZero() || open {
				continue
			}
			s := seg()
			// a sentence that ended and held still; text from before our end may lack its tail
			settled := s != "" && strings.ContainsAny(s[len(s)-1:], ".?!") && time.Since(changed) >= e.Settle &&
				(changed.After(ended) || time.Since(ended) >= 2*e.Settle)
			if settled || time.Since(ended) >= e.FinalWait {
				emitFinal()
			}
		}
	}
}
