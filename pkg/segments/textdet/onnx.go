/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package textdet

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"sync"

	ort "github.com/shota3506/onnxruntime-purego/onnxruntime"
)

//go:embed model/det.onnx
var model []byte

// apiVersion is the ONNX Runtime C API the binding speaks (ORT 1.23).
const apiVersion = 23

// textProb is the per-pixel probability counted as text (PaddleOCR's DB
// binarization threshold).
const textProb = 0.3

var (
	mean = [3]float32{0.485, 0.456, 0.406}
	std  = [3]float32{0.229, 0.224, 0.225}
)

type onnxDetector struct {
	mu      sync.Mutex // one Run at a time: frames are few, and memory stays flat
	rt      *ort.Runtime
	env     *ort.Env
	sess    *ort.Session
	in, out string
}

// NewONNX loads ONNX Runtime from libPath ("" searches the system's library
// paths) and the embedded model. Any failure is ErrUnavailable.
func NewONNX(libPath string) (Detector, error) {
	rt, err := ort.NewRuntime(libPath, apiVersion)
	if err != nil {
		return nil, fmt.Errorf("%w: load ONNX Runtime: %w", ErrUnavailable, err)
	}
	env, err := rt.NewEnv("clustarr-textdet", ort.LoggingLevelError)
	if err != nil {
		_ = rt.Close()
		return nil, fmt.Errorf("%w: environment: %w", ErrUnavailable, err)
	}
	sess, err := rt.NewSessionFromReader(env, bytes.NewReader(model), nil)
	if err != nil {
		env.Close()
		_ = rt.Close()
		return nil, fmt.Errorf("%w: load the model: %w", ErrUnavailable, err)
	}
	if len(sess.InputNames()) != 1 || len(sess.OutputNames()) != 1 {
		sess.Close()
		env.Close()
		_ = rt.Close()
		return nil, fmt.Errorf("%w: the model has %d inputs and %d outputs, want 1 and 1",
			ErrUnavailable, len(sess.InputNames()), len(sess.OutputNames()))
	}
	return &onnxDetector{rt: rt, env: env, sess: sess, in: sess.InputNames()[0], out: sess.OutputNames()[0]}, nil
}

// Density implements Detector. The frame is padded with black to a
// multiple of 32 rows and columns, as the model needs.
func (d *onnxDetector) Density(rgb []byte, w, h int) (int32, error) {
	if len(rgb) != w*h*3 {
		return 0, fmt.Errorf("textdet: %d bytes for a %dx%d frame", len(rgb), w, h)
	}
	pw, ph := (w+31)/32*32, (h+31)/32*32
	input := make([]float32, 3*pw*ph)
	for c := range 3 {
		pad := -mean[c] / std[c] // a black pixel, normalized
		plane := input[c*pw*ph : (c+1)*pw*ph]
		for i := range plane {
			plane[i] = pad
		}
		for y := range h {
			for x := range w {
				v := float32(rgb[(y*w+x)*3+c]) / 255
				plane[y*pw+x] = (v - mean[c]) / std[c]
			}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t, err := ort.NewTensorValue(d.rt, input, []int64{1, 3, int64(ph), int64(pw)})
	if err != nil {
		return 0, fmt.Errorf("textdet: input tensor: %w", err)
	}
	defer t.Close()
	outs, err := d.sess.Run(context.Background(), map[string]*ort.Value{d.in: t})
	if err != nil {
		return 0, fmt.Errorf("textdet: run: %w", err)
	}
	defer func() {
		for _, v := range outs {
			v.Close()
		}
	}()
	prob, _, err := ort.GetTensorData[float32](outs[d.out])
	if err != nil {
		return 0, fmt.Errorf("textdet: output: %w", err)
	}
	n := 0
	for y := range h { // count only the frame, not its padding
		for x := range w {
			if prob[y*pw+x] > textProb {
				n++
			}
		}
	}
	return int32(n * 1000 / (w * h)), nil
}
