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

// Text over a flat picture: thresholds measured on 2026-10-01 against the
// fixtures (test/data/segments/frames). Dexter's name cards over a grey
// picture read entropy 4.00-4.06 bits and 73-95 permille edges; normal scenes
// read at least 4.51 bits and 207 permille. The margin is thin, so a run
// carried by this signal alone is weak (confidence 60): the DNN stage is
// asked to confirm it.
const (
	flatEntropyMilli  = 4300
	minTextEdgePermil = 20
	maxFlatEdgePermil = 200
)

const (
	minRunS   = 15 // a run shorter than this is not credits
	maxGapS   = 2  // non-credit-like seconds bridged inside a run
	strongPct = 50 // a run with at least this share of strong seconds is strong
)

// Run is a span of credit-like seconds, in seconds from the file's start.
type Run struct {
	StartS, EndS int
	Confidence   int32
}

// strong is a black or scrolling second; weak is text over a flat picture.
func strong(s Stat) bool { return s.Black || s.Scrolling }

func weak(s Stat) bool {
	return s.EntropyMilli < flatEntropyMilli && s.EdgePermille >= minTextEdgePermil && s.EdgePermille < maxFlatEdgePermil
}

// CreditRuns returns the runs of credit-like seconds in st (one Stat per
// second, the first at offsetS seconds into the file), at least 15 s long,
// with gaps of up to 2 s bridged. A run that is mostly black or scrolling is
// 70, +10 when at least half of it scrolls, +10 when it reaches the window's
// end, at most 90; a run carried by flat text alone is 60.
func CreditRuns(st []Stat, offsetS int) []Run {
	var out []Run
	start, last := -1, -1
	flush := func() {
		if start < 0 || last-start+1 < minRunS {
			return
		}
		n, strongN, scrollN := last-start+1, 0, 0
		for _, s := range st[start : last+1] {
			if strong(s) {
				strongN++
			}
			if s.Scrolling {
				scrollN++
			}
		}
		conf := int32(60)
		if strongN*100 >= strongPct*n {
			conf = 70
			if scrollN*2 >= n {
				conf += 10
			}
			if last >= len(st)-1-maxGapS {
				conf += 10
			}
		}
		out = append(out, Run{StartS: offsetS + start, EndS: offsetS + last + 1, Confidence: min(conf, 90)})
	}
	for i, s := range st {
		if !strong(s) && !weak(s) {
			continue
		}
		if start >= 0 && i-last-1 > maxGapS {
			flush()
			start = -1
		}
		if start < 0 {
			start = i
		}
		last = i
	}
	flush()
	return out
}
