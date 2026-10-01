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

// Package textdet measures how much of a frame is text, with PaddleOCR's
// text-detection model on ONNX Runtime, and finds where credits start by
// searching end-of-file frames with it (spec 2026-10-01 segment detection
// §6.4). It never reads the text.
package textdet

import (
	"context"
	"errors"
)

// ErrUnavailable is a detector that could not be built: no ONNX Runtime
// library, or a model it would not load.
var ErrUnavailable = errors.New("textdet: unavailable")

// Detector reports a frame's text density.
type Detector interface {
	// Density is the share of the frame's pixels that are text, in
	// thousandths. rgb is w*h*3 bytes, rows top to bottom.
	Density(rgb []byte, w, h int) (permille int32, err error)
}

// coarseStepS is the first pass's spacing: about 15 frames over an episode's
// 450 s window, then a binary search to refineS.
const (
	coarseStepS = 30.0
	refineS     = 1.0
)

// FindStart finds where credits start in [fromS, toS]: the first coarse
// sample from which every sample to toS is text-dense (density >=
// threshold), refined by binary search against the sample before it. ok is
// false when the last sample is not dense: credits run to the end.
func FindStart(ctx context.Context, d Detector, frame func(ctx context.Context, atS float64) ([]byte, int, int, error),
	fromS, toS float64, threshold int32,
) (startS float64, ok bool, err error) {
	dense := func(atS float64) (bool, error) {
		rgb, w, h, err := frame(ctx, atS)
		if err != nil {
			return false, err
		}
		p, err := d.Density(rgb, w, h)
		return p >= threshold, err
	}
	var times []float64
	for t := fromS; t <= toS; t += coarseStepS {
		times = append(times, t)
	}
	if len(times) == 0 || times[len(times)-1] < toS {
		times = append(times, toS)
	}
	k := len(times)
	for i := len(times) - 1; i >= 0; i-- {
		ok, err := dense(times[i])
		if err != nil {
			return 0, false, err
		}
		if !ok {
			break
		}
		k = i
	}
	if k == len(times) {
		return 0, false, nil
	}
	if k == 0 {
		return times[0], true, nil
	}
	lo, hi := times[k-1], times[k]
	for hi-lo > refineS {
		mid := (lo + hi) / 2
		ok, err := dense(mid)
		if err != nil {
			return 0, false, err
		}
		if ok {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi, true, nil
}

// CreditsThreshold is the text density, in thousandths, from which a frame
// is a credits frame. Measured 2026-10-01 on Dexter S01E01: a single name
// card over a picture reads 9; a scene with a small name tag reads 0.
// Rolling credits read far higher.
const CreditsThreshold int32 = 5
