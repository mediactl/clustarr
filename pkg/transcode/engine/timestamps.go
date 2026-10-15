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

package engine

import (
	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avutil"
)

// dtsGuard holds a copied stream's decode timestamps strictly increasing,
// as FFmpeg's own remux does (fftools/ffmpeg_mux.c, mux_fixup_ts). A
// damaged source repeats a timestamp -- USURY's HEVC Blu-ray encodes of 49
// Up and 63 Up carry four frames at one time (2026-10-15) -- and MP4's
// muxer refuses a decode timestamp that does not move forward, which
// failed every remux of them. A decode timestamp after its presentation
// becomes FFmpeg's guess, the median of the two and the next tick; one that
// does not move forward moves one tick past the last, and the presentation
// with it when that would fall behind. An unset decode timestamp is the
// muxer's to derive, from the presentation and the stream's reorder delay.
//
// The guard works in the packet's time base, since the muxer rescales a
// copied packet after it, so its tick is one tick of the output's, rounded
// up: one tick of a finer packet time base could round onto the same
// output timestamp.
type dtsGuard struct {
	last     int64 // the last decode timestamp passed; AV_NOPTS_VALUE before one
	step     int64
	repaired int // packets whose timestamps moved
}

func newDTSGuard(in, out ffgo.Rational) dtsGuard {
	step := int64(1)
	if in.Num > 0 && in.Den > 0 && out.Num > 0 && out.Den > 0 {
		n, d := int64(out.Num)*int64(in.Den), int64(out.Den)*int64(in.Num)
		step = max(1, (n+d-1)/d)
	}
	return dtsGuard{last: avutil.AV_NOPTS_VALUE, step: step}
}

// fix returns the packet's timestamps as they are to be written.
func (g *dtsGuard) fix(pts, dts int64) (int64, int64) {
	const none = avutil.AV_NOPTS_VALUE
	if dts == none {
		return pts, dts
	}
	moved := false
	if pts != none && dts > pts {
		next := pts // before any packet, FFmpeg's median is the presentation
		if g.last != none {
			next = g.last + g.step
		}
		m := median(pts, dts, next)
		pts, dts, moved = m, m, true
	}
	if g.last != none && dts < g.last+g.step {
		next := g.last + g.step
		if pts != none && pts >= dts {
			pts = max(pts, next)
		}
		dts, moved = next, true
	}
	if moved {
		g.repaired++
	}
	g.last = dts
	return pts, dts
}

func median(a, b, c int64) int64 { return max(min(a, b), min(max(a, b), c)) }
