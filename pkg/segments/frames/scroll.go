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

package frames

import "math"

// rowProfile is each row's mean luma.
func rowProfile(f []byte) []float64 {
	p := make([]float64, H)
	for y := range H {
		s := 0
		for _, v := range f[y*W : (y+1)*W] {
			s += int(v)
		}
		p[y] = float64(s) / W
	}
	return p
}

// bestShift finds the shift s in [-maxScrollShift, maxScrollShift] that
// best maps prev onto cur (cur[y] ~ prev[y+s]: text moving up is s > 0),
// by normalized cross-correlation over the overlap. ok is false when the
// best correlation is weak or either profile is too flat to tell.
func bestShift(prev, cur []float64) (int8, bool) {
	if stddev(prev) < minProfileStdDev || stddev(cur) < minProfileStdDev {
		return 0, false
	}
	best, bestCorr := 0, -2.0
	for s := -maxScrollShift; s <= maxScrollShift; s++ {
		var a, b []float64
		if s >= 0 {
			a, b = prev[s:], cur[:H-s]
		} else {
			a, b = prev[:H+s], cur[-s:]
		}
		if c := ncc(a, b); c > bestCorr {
			best, bestCorr = s, c
		}
	}
	return int8(best), bestCorr >= minScrollCorr
}

func ncc(a, b []float64) float64 {
	ma, mb := mean(a), mean(b)
	var num, da, db float64
	for i := range a {
		x, y := a[i]-ma, b[i]-mb
		num += x * y
		da += x * x
		db += y * y
	}
	if da == 0 || db == 0 {
		return 0
	}
	return num / math.Sqrt(da*db)
}

func mean(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func stddev(v []float64) float64 {
	m, s := mean(v), 0.0
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(v)))
}

// markScrolling sets Scrolling on every frame inside a run of at least
// scrollRunFrames frames whose confident shifts are the same nonzero value
// within one row.
func markScrolling(st []Stat) {
	start := -1
	flush := func(end int) {
		if start >= 0 && end-start >= scrollRunFrames {
			for i := start; i < end; i++ {
				st[i].Scrolling = true
			}
		}
	}
	for i := range st {
		ok := st[i].ScrollOK && st[i].ScrollPx != 0
		same := start >= 0 && ok && absInt8(st[i].ScrollPx-st[start].ScrollPx) <= 1
		switch {
		case same:
		case ok:
			flush(i)
			start = i
		default:
			flush(i)
			start = -1
		}
	}
	flush(len(st))
}

func absInt8(x int8) int8 {
	if x < 0 {
		return -x
	}
	return x
}
