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

package audioalign

import "math"

// SampleRate is the rate every input is decoded to.
const SampleRate = 8000

const (
	frameSize = 512
	hop       = 160 // 20 ms at SampleRate
	nBands    = 24
	lowHz     = 150.0
	highHz    = 3800.0
)

// frameDur is one feature frame.
const frameSeconds = float64(hop) / SampleRate

// features are per-band onset strengths: the positive frame-to-frame change
// of each band's log energy, z-normalised per band. bands[b][t].
type features [][]float64

func extract(x []float32) features {
	n := (len(x) - frameSize) / hop
	if n < 2 {
		return make(features, nBands)
	}
	edges := make([]int, nBands+1)
	for i := range edges {
		hz := lowHz * math.Pow(highHz/lowHz, float64(i)/nBands)
		edges[i] = int(hz * frameSize / SampleRate)
	}
	win := make([]float64, frameSize)
	for i := range win {
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/frameSize)
	}
	logE := make([][]float64, nBands)
	for b := range logE {
		logE[b] = make([]float64, n)
	}
	buf := make([]complex128, frameSize)
	for t := 0; t < n; t++ {
		off := t * hop
		for i := 0; i < frameSize; i++ {
			buf[i] = complex(float64(x[off+i])*win[i], 0)
		}
		fft(buf, false)
		for b := 0; b < nBands; b++ {
			var e float64
			for k := edges[b]; k < edges[b+1] && k < frameSize/2; k++ {
				re, im := real(buf[k]), imag(buf[k])
				e += math.Sqrt(re*re + im*im)
			}
			logE[b][t] = math.Log1p(1000 * e)
		}
	}
	out := make(features, nBands)
	for b := range out {
		o := make([]float64, n-1)
		var sum float64
		for t := 1; t < n; t++ {
			if d := logE[b][t] - logE[b][t-1]; d > 0 {
				o[t-1] = d
			}
			sum += o[t-1]
		}
		mean := sum / float64(len(o))
		var ss float64
		for i := range o {
			o[i] -= mean
			ss += o[i] * o[i]
		}
		sd := math.Sqrt(ss/float64(len(o))) + 1e-9
		for i := range o {
			o[i] /= sd
		}
		out[b] = o
	}
	return out
}

func (f features) frames() int {
	if len(f) == 0 {
		return 0
	}
	return len(f[0])
}

// stretch resamples the frame axis by r: a donor played r times faster
// than the target is stretched by r onto the target's timeline.
func (f features) stretch(r float64) features {
	n := int(float64(f.frames()) * r)
	out := make(features, len(f))
	for b, band := range f {
		o := make([]float64, n)
		for i := range o {
			src := float64(i) / r
			j := int(src)
			if j+1 >= len(band) {
				break
			}
			fr := src - float64(j)
			o[i] = band[j]*(1-fr) + band[j+1]*fr
		}
		out[b] = o
	}
	return out
}
