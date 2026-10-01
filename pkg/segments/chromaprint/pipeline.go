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

package chromaprint

import "math"

// Ported from acoustid/chromaprint (LGPL-2.1+): fingerprinter.cpp,
// fingerprinter_configuration.cpp, chroma.cpp, chroma_filter.cpp,
// chroma_normalizer.h, fingerprint_calculator.cpp, filter_utils.h,
// quantizer.h, utils.h and utils/rolling_integral_image.h.

const (
	numBands = 12
	minFreq  = 28
	maxFreq  = 3520
)

// chromaFilter is kChromaFilterCoefficients, applied oldest frame first.
var chromaFilter = []float64{0.25, 0.75, 1.0, 0.75, 0.25}

type filter struct{ kind, y, height, width int }

type quantizer struct{ t0, t1, t2 float64 }

type classifier struct {
	f filter
	q quantizer
}

// classifiers is kClassifiersTest2, verbatim.
var classifiers = [16]classifier{
	{filter{0, 4, 3, 15}, quantizer{1.98215, 2.35817, 2.63523}},
	{filter{4, 4, 6, 15}, quantizer{-1.03809, -0.651211, -0.282167}},
	{filter{1, 0, 4, 16}, quantizer{-0.298702, 0.119262, 0.558497}},
	{filter{3, 8, 2, 12}, quantizer{-0.105439, 0.0153946, 0.135898}},
	{filter{3, 4, 4, 8}, quantizer{-0.142891, 0.0258736, 0.200632}},
	{filter{4, 0, 3, 5}, quantizer{-0.826319, -0.590612, -0.368214}},
	{filter{1, 2, 2, 9}, quantizer{-0.557409, -0.233035, 0.0534525}},
	{filter{2, 7, 3, 4}, quantizer{-0.0646826, 0.00620476, 0.0784847}},
	{filter{2, 6, 2, 16}, quantizer{-0.192387, -0.029699, 0.215855}},
	{filter{2, 1, 3, 2}, quantizer{-0.0397818, -0.00568076, 0.0292026}},
	{filter{5, 10, 1, 15}, quantizer{-0.53823, -0.369934, -0.190235}},
	{filter{3, 6, 2, 10}, quantizer{-0.124877, 0.0296483, 0.139239}},
	{filter{2, 1, 1, 14}, quantizer{-0.101475, 0.0225617, 0.231971}},
	{filter{3, 5, 6, 4}, quantizer{-0.0799915, -0.00729616, 0.063262}},
	{filter{1, 9, 2, 12}, quantizer{-0.272556, 0.019424, 0.302559}},
	{filter{3, 4, 2, 14}, quantizer{-0.164292, -0.0321188, 0.0846339}},
}

func maxFilterWidth() int {
	w := 0
	for _, c := range classifiers {
		w = max(w, c.f.width)
	}
	return w
}

// hammingWindow is PrepareHammingWindow scaled by 1/INT16_MAX.
func hammingWindow() []float64 {
	w := make([]float64, frameSize)
	for i := range w {
		w[i] = (1.0 / math.MaxInt16) * (0.54 - 0.46*math.Cos(float64(i)*2.0*math.Pi/float64(frameSize-1)))
	}
	return w
}

// prepareNotes maps each FFT bin in [minIdx, maxIdx) to its chroma band.
func prepareNotes() (notes []int, minIdx, maxIdx int) {
	notes = make([]int, frameSize)
	minIdx = max(1, freqToIndex(minFreq))
	maxIdx = min(frameSize/2, freqToIndex(maxFreq))
	for i := minIdx; i < maxIdx; i++ {
		freq := float64(i) * SampleRate / frameSize
		octave := math.Log(freq/(440.0/16.0)) / math.Log(2.0)
		note := numBands * (octave - math.Floor(octave))
		notes[i] = int(note) // (char)note truncates
	}
	return notes, minIdx, maxIdx
}

func freqToIndex(freq float64) int { return int(math.Round(frameSize * freq / SampleRate)) }

// normalize is NormalizeVector with EuclideanNorm and threshold 0.01.
func normalize(v []float64) {
	var squares float64
	for _, x := range v {
		squares += x * x
	}
	norm := 0.0
	if squares > 0 {
		norm = math.Sqrt(squares)
	}
	if norm < 0.01 {
		clear(v)
		return
	}
	for i := range v {
		v[i] /= norm
	}
}

// integralImage keeps every row's running sums; a fingerprint is short
// enough that Chromaprint's rolling window is not worth porting.
type integralImage struct{ data [][]float64 }

func (m *integralImage) rows() int { return len(m.data) }

func (m *integralImage) addRow(row []float64) {
	cur := make([]float64, len(row))
	var sum float64
	for i, x := range row {
		sum += x
		cur[i] = sum
	}
	if n := len(m.data); n > 0 {
		for i, x := range m.data[n-1] {
			cur[i] += x
		}
	}
	m.data = append(m.data, cur)
}

// area is RollingIntegralImage::Area: rows [r1, r2), columns [c1, c2).
func (m *integralImage) area(r1, c1, r2, c2 int) float64 {
	if r1 == r2 || c1 == c2 {
		return 0
	}
	if r1 == 0 {
		row := m.data[r2-1]
		if c1 == 0 {
			return row[c2-1]
		}
		return row[c2-1] - row[c1-1]
	}
	row1, row2 := m.data[r1-1], m.data[r2-1]
	if c1 == 0 {
		return row2[c2-1] - row1[c2-1]
	}
	return row2[c2-1] - row1[c2-1] - row2[c1-1] + row1[c1-1]
}

func subtractLog(a, b float64) float64 { return math.Log((1.0 + a) / (1.0 + b)) }

// apply is Filter::Apply with SubtractLog; x is the row (time), y the band.
func (f filter) apply(m *integralImage, x int) float64 {
	y, w, h := f.y, f.width, f.height
	switch f.kind {
	case 0:
		return subtractLog(m.area(x, y, x+w, y+h), 0)
	case 1:
		h2 := h / 2
		return subtractLog(m.area(x, y+h2, x+w, y+h), m.area(x, y, x+w, y+h2))
	case 2:
		w2 := w / 2
		return subtractLog(m.area(x+w2, y, x+w, y+h), m.area(x, y, x+w2, y+h))
	case 3:
		w2, h2 := w/2, h/2
		a := m.area(x, y+h2, x+w2, y+h) + m.area(x+w2, y, x+w, y+h2)
		b := m.area(x, y, x+w2, y+h2) + m.area(x+w2, y+h2, x+w, y+h)
		return subtractLog(a, b)
	case 4:
		h3 := h / 3
		a := m.area(x, y+h3, x+w, y+2*h3)
		b := m.area(x, y, x+w, y+h3) + m.area(x, y+2*h3, x+w, y+h)
		return subtractLog(a, b)
	case 5:
		w3 := w / 3
		a := m.area(x+w3, y, x+2*w3, y+h)
		b := m.area(x, y, x+w3, y+h) + m.area(x+2*w3, y, x+w, y+h)
		return subtractLog(a, b)
	}
	return 0
}

func (q quantizer) quantize(v float64) uint32 {
	if v < q.t1 {
		if v < q.t0 {
			return 0
		}
		return 1
	}
	if v < q.t2 {
		return 2
	}
	return 3
}

var grayCode = [4]uint32{0, 1, 3, 2}

func subfingerprint(m *integralImage, offset int) uint32 {
	var bits uint32
	for _, c := range classifiers {
		bits = bits<<2 | grayCode[c.q.quantize(c.f.apply(m, offset))]
	}
	return bits
}
