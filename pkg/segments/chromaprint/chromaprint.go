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

// Package chromaprint is a Go port of Chromaprint's default fingerprint
// (algorithm 2, "test2"), after acoustid/chromaprint (LGPL-2.1+, which
// GPL-3.0 absorbs). It takes mono 16-bit PCM at 11,025 Hz -- ffmpeg resamples
// to it -- and returns one 32-bit point per 1,365 samples.
package chromaprint

// SampleRate is the only rate Fingerprint accepts.
const SampleRate = 11025

// ItemSeconds is the time one fingerprint point advances.
const ItemSeconds = float64(hop) / SampleRate

const (
	frameSize = 4096
	hop       = frameSize / 3 // frame_size - overlap, overlap = 4096 - 4096/3
)

// Fingerprint computes the fingerprint of pcm. Frames of frameSize samples
// start every hop samples while a whole frame fits, as Chromaprint's
// AudioSlicer emits them; a clip shorter than the classifiers' widest filter
// in frames has no fingerprint.
func Fingerprint(pcm []int16) []uint32 {
	window := hammingWindow()
	fft := newFFT(frameSize)
	notes, minIdx, maxIdx := prepareNotes()
	var (
		buf     [][]float64 // the chroma filter's last len(chromaFilter) vectors
		img     integralImage
		out     []uint32
		frame   = make([]float64, frameSize)
		power   = make([]float64, frameSize/2+1)
		maxWide = maxFilterWidth()
	)
	for start := 0; start+frameSize <= len(pcm); start += hop {
		for i := range frame {
			frame[i] = float64(pcm[start+i]) * window[i]
		}
		fft.power(frame, power)
		chroma := make([]float64, numBands)
		for i := minIdx; i < maxIdx; i++ {
			chroma[notes[i]] += power[i]
		}
		buf = append(buf, chroma)
		if len(buf) < len(chromaFilter) {
			continue
		}
		if len(buf) > len(chromaFilter) {
			buf = buf[1:]
		}
		filtered := make([]float64, numBands)
		for b := range numBands {
			for j, c := range chromaFilter {
				filtered[b] += buf[j][b] * c
			}
		}
		normalize(filtered)
		img.addRow(filtered)
		if img.rows() >= maxWide {
			out = append(out, subfingerprint(&img, img.rows()-maxWide))
		}
	}
	return out
}
