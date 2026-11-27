//go:build !android

// Package kws is Nina's local wake word: sherpa-onnx's keyword spotter (a 3.3M-parameter
// zipformer, open vocabulary: the keywords are written as the model's BPE tokens) on the
// utterances the endpointer cuts. Asleep, an utterance goes to the cloud transcriber only when
// her name is heard in it here; what the room says otherwise stays on the machine.
package kws

import (
	"fmt"
	"os"
	"path/filepath"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

type Spotter struct {
	sp *sherpa.KeywordSpotter
	s  *sherpa.OnlineStream
	f  []float32
}

// New loads the model in dir (encoder/decoder/joiner *.int8.onnx, tokens.txt) with the
// keywords file (one per line: tokens @LABEL); threshold: lower hears more (0.25).
func New(dir, keywords string, threshold float32) (*Spotter, error) {
	find := func(part string) (string, error) {
		m, _ := filepath.Glob(filepath.Join(dir, part+"-*.int8.onnx"))
		if len(m) == 0 {
			return "", fmt.Errorf("kws: no %s model in %s", part, dir)
		}
		return m[0], nil
	}
	cfg := sherpa.KeywordSpotterConfig{MaxActivePaths: 4, KeywordsFile: keywords, KeywordsScore: 1, KeywordsThreshold: threshold}
	cfg.FeatConfig.SampleRate, cfg.FeatConfig.FeatureDim = 16000, 80
	var err error
	t := &cfg.ModelConfig.Transducer
	if t.Encoder, err = find("encoder"); err != nil {
		return nil, err
	}
	if t.Decoder, err = find("decoder"); err != nil {
		return nil, err
	}
	if t.Joiner, err = find("joiner"); err != nil {
		return nil, err
	}
	if _, err := os.Stat(keywords); err != nil {
		return nil, fmt.Errorf("kws: %w", err)
	}
	cfg.ModelConfig.Tokens, cfg.ModelConfig.NumThreads, cfg.ModelConfig.Provider = filepath.Join(dir, "tokens.txt"), 1, "cpu"
	sp := sherpa.NewKeywordSpotter(&cfg)
	if sp == nil {
		return nil, fmt.Errorf("kws: the spotter did not load (%s)", dir)
	}
	return &Spotter{sp: sp}, nil
}

// Begin starts an utterance.
func (k *Spotter) Begin() {
	if k.s != nil {
		sherpa.DeleteOnlineStream(k.s)
	}
	k.s = sherpa.NewKeywordStream(k.sp)
}

// Feed takes 16 kHz 16-bit PCM and returns a keyword's label heard in it ("" for none).
func (k *Spotter) Feed(pcm []byte) string {
	if k.s == nil {
		k.Begin()
	}
	k.f = k.f[:0]
	for i := 0; i+1 < len(pcm); i += 2 {
		k.f = append(k.f, float32(int16(uint16(pcm[i])|uint16(pcm[i+1])<<8))/32768)
	}
	k.s.AcceptWaveform(16000, k.f)
	return k.decode()
}

// End flushes the utterance's last frames (the model looks a little ahead) and tells a keyword
// heard there.
func (k *Spotter) End() string {
	if k.s == nil {
		return ""
	}
	k.s.AcceptWaveform(16000, make([]float32, 8000)) // 0.5 s of silence
	hit := k.decode()
	sherpa.DeleteOnlineStream(k.s)
	k.s = nil
	return hit
}

func (k *Spotter) decode() string {
	hit := ""
	for k.sp.IsReady(k.s) {
		k.sp.Decode(k.s)
		if r := k.sp.GetResult(k.s); r.Keyword != "" && hit == "" {
			hit = r.Keyword
			k.sp.Reset(k.s)
		}
	}
	return hit
}
