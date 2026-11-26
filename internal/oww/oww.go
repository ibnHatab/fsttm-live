// Package oww is openWakeWord's detector in Go (fsttm-live, 2026-11-26): its two shared models (a mel
// spectrogram and Google's speech embedding) and a wake word's classifier, run in the ONNX runtime
// that sherpa-onnx already loads into the process (onnxruntime_go over the same library).
//
// It listens continuously, not per utterance: the spotter it replaces (sherpa-onnx's keyword
// spotter, started when the endpointer opened an utterance) missed a bare "Nina" whose soft N came
// before the voice detector's start (2026-11-26, pero: 1 of 6). Streaming as openWakeWord's
// AudioFeatures does: 80 ms chunks; the mel spectrogram of each chunk and the 30 ms before it (/10 + 2);
// one embedding per chunk from the last 76 mel frames; the classifier over the last 16 embeddings
// (1.28 s). Feed takes 16 kHz 16-bit PCM in any sizes and returns the highest score of the chunks it
// completed.
package oww

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	chunk    = 1280 // 80 ms
	ctxPad   = 480  // 30 ms of the previous chunk under the mel spectrogram's window
	melBins  = 32
	melWin   = 76 // mel frames per embedding
	embDim   = 96
	nFeat    = 16 // embeddings per classification
	keepMel  = 970
	keepFeat = 120
)

var initOnce sync.Once
var initErr error

// runtime finds the ONNX runtime library already mapped into this process (sherpa-onnx links it),
// else ORT_LIB, and starts onnxruntime_go's environment over it.
func runtime() error {
	initOnce.Do(func() {
		lib := os.Getenv("ORT_LIB")
		if lib == "" {
			if b, err := os.ReadFile("/proc/self/maps"); err == nil {
				for _, l := range strings.Split(string(b), "\n") {
					if i := strings.Index(l, "/"); i >= 0 && strings.Contains(l, "libonnxruntime.so") {
						lib = l[i:]
						break
					}
				}
			}
		}
		if lib == "" {
			initErr = fmt.Errorf("oww: no ONNX runtime library in the process (set ORT_LIB)")
			return
		}
		ort.SetSharedLibraryPath(lib)
		initErr = ort.InitializeEnvironment()
	})
	return initErr
}

type model struct {
	s       *ort.DynamicAdvancedSession
	in, out string
}

func load(path string) (*model, error) {
	ins, outs, err := ort.GetInputOutputInfo(path)
	if err != nil {
		return nil, fmt.Errorf("oww: %s: %w", path, err)
	}
	o, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer o.Destroy()
	_ = o.SetIntraOpNumThreads(1)
	_ = o.SetInterOpNumThreads(1)
	s, err := ort.NewDynamicAdvancedSession(path, []string{ins[0].Name}, []string{outs[0].Name}, o)
	if err != nil {
		return nil, fmt.Errorf("oww: %s: %w", path, err)
	}
	return &model{s: s, in: ins[0].Name, out: outs[0].Name}, nil
}

// run: one input of shape, the output's data (a copy) and its shape.
func (m *model) run(shape []int64, data []float32) ([]float32, []int64, error) {
	t, err := ort.NewTensor(ort.NewShape(shape...), data)
	if err != nil {
		return nil, nil, err
	}
	defer t.Destroy()
	out := []ort.Value{nil}
	if err := m.s.Run([]ort.Value{t}, out); err != nil {
		return nil, nil, err
	}
	defer out[0].Destroy()
	r := out[0].(*ort.Tensor[float32])
	return append([]float32(nil), r.GetData()...), r.GetShape(), nil
}

// Detector is one wake word's detector over a stream.
type Detector struct {
	mel, emb, ww *model
	raw          []int16     // the last chunk's last 30 ms (none before the first: as openWakeWord)
	pending      []int16     // samples not yet a whole chunk
	melBuf       [][]float32 // mel frames
	feat         [][]float32 // embeddings
}

// New loads the shared models from dir (melspectrogram.onnx, embedding_model.onnx) and the wake
// word's classifier (an openWakeWord .onnx).
func New(dir, word string) (*Detector, error) {
	if err := runtime(); err != nil {
		return nil, err
	}
	d := &Detector{}
	var err error
	if d.mel, err = load(filepath.Join(dir, "melspectrogram.onnx")); err != nil {
		return nil, err
	}
	if d.emb, err = load(filepath.Join(dir, "embedding_model.onnx")); err != nil {
		return nil, err
	}
	if d.ww, err = load(word); err != nil {
		return nil, err
	}
	return d, d.Reset()
}

// melspec: the mel frames of x (/10 + 2, as openWakeWord).
func (d *Detector) melspec(x []int16) ([][]float32, error) {
	f := make([]float32, len(x))
	for i, v := range x {
		f[i] = float32(v)
	}
	out, shape, err := d.mel.run([]int64{1, int64(len(x))}, f)
	if err != nil {
		return nil, err
	}
	n := int(shape[len(shape)-2])
	rows := make([][]float32, n)
	for i := range rows {
		r := out[i*melBins : (i+1)*melBins]
		for j := range r {
			r[j] = r[j]/10 + 2
		}
		rows[i] = r
	}
	return rows, nil
}

func (d *Detector) embed(win [][]float32) ([]float32, error) {
	x := make([]float32, 0, melWin*melBins)
	for _, r := range win {
		x = append(x, r...)
	}
	out, _, err := d.emb.run([]int64{1, melWin, melBins, 1}, x)
	return out, err
}

// Reset: the buffers as openWakeWord starts them (mel frames of ones; embeddings of 4 s of noise).
func (d *Detector) Reset() error {
	d.pending, d.raw = nil, nil
	d.melBuf = make([][]float32, melWin)
	for i := range d.melBuf {
		r := make([]float32, melBins)
		for j := range r {
			r[j] = 1
		}
		d.melBuf[i] = r
	}
	noise := make([]int16, 4*16000)
	for i := range noise {
		noise[i] = int16(rand.Intn(2000) - 1000)
	}
	m, err := d.melspec(noise)
	if err != nil {
		return err
	}
	d.feat = nil
	for i := 0; i+melWin <= len(m); i += 8 {
		e, err := d.embed(m[i : i+melWin])
		if err != nil {
			return err
		}
		d.feat = append(d.feat, e)
	}
	return nil
}

// Feed takes 16 kHz 16-bit little-endian PCM; returns the highest score among the 80 ms chunks it
// completed (-1: none completed).
func (d *Detector) Feed(pcm []byte) (float32, error) {
	for i := 0; i+1 < len(pcm); i += 2 {
		d.pending = append(d.pending, int16(uint16(pcm[i])|uint16(pcm[i+1])<<8))
	}
	best := float32(-1)
	for len(d.pending) >= chunk {
		c := d.pending[:chunk]
		x := append(append([]int16(nil), d.raw...), c...) // the previous chunk's last 30 ms, then this one
		d.raw = append(d.raw[:0], c[chunk-ctxPad:]...)
		d.pending = d.pending[chunk:]
		rows, err := d.melspec(x)
		if err != nil {
			return best, err
		}
		d.melBuf = append(d.melBuf, rows...)
		if len(d.melBuf) > keepMel {
			d.melBuf = d.melBuf[len(d.melBuf)-keepMel:]
		}
		e, err := d.embed(d.melBuf[len(d.melBuf)-melWin:])
		if err != nil {
			return best, err
		}
		d.feat = append(d.feat, e)
		if len(d.feat) > keepFeat {
			d.feat = d.feat[len(d.feat)-keepFeat:]
		}
		x2 := make([]float32, 0, nFeat*embDim)
		for _, f := range d.feat[len(d.feat)-nFeat:] {
			x2 = append(x2, f...)
		}
		out, _, err := d.ww.run([]int64{1, nFeat, embDim}, x2)
		if err != nil {
			return best, err
		}
		if out[0] > best {
			best = out[0]
		}
	}
	return best, nil
}
