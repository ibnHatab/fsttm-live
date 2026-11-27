//go:build android

package kws

import "errors"

// On Android there is no local wake word (sherpa-onnx's Go module links Linux's libraries): the
// app listens while it is in front, so the dialogue runs without a spotter.

type Spotter struct{}

func New(dir, keywords string, threshold float32) (*Spotter, error) {
	return nil, errors.New("kws: no local wake word on android")
}

func (k *Spotter) Begin()             {}
func (k *Spotter) Feed([]byte) string { return "" }
func (k *Spotter) End() string        { return "" }
