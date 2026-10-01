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

// Package frames reads end-of-file frames -- 1 fps, 128x72 grayscale from
// ffmpeg -- for the marks of credits: black frames, text over a flat
// picture, and rolling text (spec 2026-10-01 segment detection §6.3).
package frames

import "math"

// W and H are the frame size ffmpeg scales to.
const (
	W = 128
	H = 72
)

const (
	blackLuma        = 28 // a pixel darker than this is black
	blackFramePct    = 85 // a frame at least this black is a black frame
	edgeMagnitude    = 64 // |gx|+|gy| of the Sobel operator over this is an edge
	maxScrollShift   = 24 // rows a profile may move between frames
	minScrollCorr    = 0.8
	minProfileStdDev = 2.0 // a flatter row profile cannot show a shift
	scrollRunFrames  = 5   // consecutive frames with one shift make a scroll
)

// Stat describes one frame.
type Stat struct {
	Black bool
	// EntropyMilli is the luma histogram's entropy (64 bins), in thousandths
	// of a bit.
	EntropyMilli int32
	// EdgePermille is the share of interior pixels on an edge.
	EdgePermille int32
	// ScrollPx is the best vertical shift from the previous frame, and
	// ScrollOK whether it was a confident match.
	ScrollPx int8
	ScrollOK bool
	// Scrolling is true for a frame inside a run of at least 5 frames with
	// the same nonzero shift, within one row.
	Scrolling bool
}

// Stats describes each frame (W*H bytes, gray) of frames, in order.
func Stats(frames [][]byte) []Stat {
	out := make([]Stat, len(frames))
	var prev []float64
	for i, f := range frames {
		out[i].Black = black(f)
		out[i].EntropyMilli = entropyMilli(f)
		out[i].EdgePermille = edgePermille(f)
		prof := rowProfile(f)
		if prev != nil {
			out[i].ScrollPx, out[i].ScrollOK = bestShift(prev, prof)
		}
		prev = prof
	}
	markScrolling(out)
	return out
}

func black(f []byte) bool {
	n := 0
	for _, v := range f {
		if v < blackLuma {
			n++
		}
	}
	return n*100 >= blackFramePct*len(f)
}

func entropyMilli(f []byte) int32 {
	var hist [64]int
	for _, v := range f {
		hist[v>>2]++
	}
	var e float64
	for _, c := range hist {
		if c > 0 {
			p := float64(c) / float64(len(f))
			e -= p * math.Log2(p)
		}
	}
	return int32(math.Round(e * 1000))
}

func edgePermille(f []byte) int32 {
	at := func(x, y int) int { return int(f[y*W+x]) }
	n, total := 0, 0
	for y := 1; y < H-1; y++ {
		for x := 1; x < W-1; x++ {
			gx := at(x+1, y-1) + 2*at(x+1, y) + at(x+1, y+1) - at(x-1, y-1) - 2*at(x-1, y) - at(x-1, y+1)
			gy := at(x-1, y+1) + 2*at(x, y+1) + at(x+1, y+1) - at(x-1, y-1) - 2*at(x, y-1) - at(x+1, y-1)
			if abs(gx)+abs(gy) > edgeMagnitude {
				n++
			}
			total++
		}
	}
	return int32(n * 1000 / total)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
