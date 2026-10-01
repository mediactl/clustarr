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

// Package align finds audio shared between fingerprinted windows: a
// season's intro, or its ending theme (spec 2026-10-01 segment detection
// §6.2), after Intro Skipper's procedure.
package align

import (
	"math/bits"
	"slices"
	"sort"

	"github.com/mediactl/clustarr/pkg/segments/chromaprint"
)

// Region is a span of a window, in seconds from its start.
type Region struct{ StartS, EndS float64 }

// Params tunes a comparison.
type Params struct {
	// MaxBitDiff is how many of a point's 32 bits may differ for it to match.
	MaxBitDiff int
	// MaxGapS is the longest run of unmatched points bridged inside a region.
	MaxGapS float64
	// MinS and MaxS bound a region's length; one outside them is no region.
	MinS, MaxS float64
	// IndexShift drops low bits from the index key, so near-identical points
	// propose the same alignment.
	IndexShift uint
}

// IntroParams are Intro Skipper's defaults for intros.
var IntroParams = Params{MaxBitDiff: 6, MaxGapS: 3.5, MinS: 15, MaxS: 120, IndexShift: 2}

// candidates is how many of the most-voted alignments are scanned.
const candidates = 10

// agreeS is how far two comparisons' regions may differ and still agree.
const agreeS = 3.0

// neighbours is how many episodes each one is compared with.
const neighbours = 4

// Shared finds the longest region of a and b -- points of two windows --
// whose aligned points differ in at most MaxBitDiff bits, bridging gaps up
// to MaxGapS, between MinS and MaxS long. ok is false when none qualifies.
func Shared(a, b []uint32, p Params) (ra, rb Region, ok bool) {
	if len(a) == 0 || len(b) == 0 {
		return ra, rb, false
	}
	index := make(map[uint32][]int32, len(b))
	for j, v := range b {
		index[v>>p.IndexShift] = append(index[v>>p.IndexShift], int32(j))
	}
	votes := map[int]int{}
	for i, v := range a {
		for _, j := range index[v>>p.IndexShift] {
			votes[int(j)-i]++
		}
	}
	offsets := make([]int, 0, len(votes))
	for d, n := range votes {
		if n >= 2 {
			offsets = append(offsets, d)
		}
	}
	sort.Slice(offsets, func(x, y int) bool {
		if votes[offsets[x]] != votes[offsets[y]] {
			return votes[offsets[x]] > votes[offsets[y]]
		}
		return offsets[x] < offsets[y]
	})
	if len(offsets) > candidates {
		offsets = offsets[:candidates]
	}
	gap := int(p.MaxGapS / chromaprint.ItemSeconds)
	best := -1
	for _, d := range offsets {
		start, end, n := longestRun(a, b, d, gap, p)
		if n > best {
			best = n
			ra = Region{StartS: seconds(start), EndS: seconds(end + 1)}
			rb = Region{StartS: seconds(start + d), EndS: seconds(end + 1 + d)}
		}
	}
	return ra, rb, best > 0
}

// longestRun scans a against b shifted by d and returns the longest run of
// matching points (gaps up to gap bridged) whose length is within bounds,
// as indices into a, with its length in points (0 when none).
func longestRun(a, b []uint32, d, gap int, p Params) (bestStart, bestEnd, bestLen int) {
	lo, hi := max(0, -d), min(len(a), len(b)-d)
	runStart, last := -1, -1
	closeRun := func() {
		if runStart < 0 {
			return
		}
		n := last - runStart + 1
		if s := seconds(n); s >= p.MinS && s <= p.MaxS && n > bestLen {
			bestStart, bestEnd, bestLen = runStart, last, n
		}
	}
	for i := lo; i < hi; i++ {
		if bits.OnesCount32(a[i]^b[i+d]) > p.MaxBitDiff {
			continue
		}
		if runStart >= 0 && i-last > gap {
			closeRun()
			runStart = -1
		}
		if runStart < 0 {
			runStart = i
		}
		last = i
	}
	closeRun()
	return bestStart, bestEnd, bestLen
}

func seconds(points int) float64 { return float64(points) * chromaprint.ItemSeconds }

// Season returns each episode's agreed region. fps holds the episodes'
// windows in episode order (nil for one not fingerprinted). Each is compared
// with its nearest neighbours, up to 4; its region is the median of the
// comparisons that agree within 3 s, needing two of them -- or one, for an
// episode with a single neighbour, so a season of two files has intros and a
// season of one has none.
func Season(fps [][]uint32, p Params) []*Region {
	out := make([]*Region, len(fps))
	for i, fp := range fps {
		if fp == nil {
			continue
		}
		near := nearest(fps, i)
		need := min(2, len(near))
		if need == 0 {
			continue
		}
		var found []Region
		for _, j := range near {
			if r, _, ok := Shared(fp, fps[j], p); ok {
				found = append(found, r)
			}
		}
		if len(found) < need {
			continue
		}
		starts, ends := medians(found)
		var agree []Region
		for _, r := range found {
			if abs(r.StartS-starts) <= agreeS && abs(r.EndS-ends) <= agreeS {
				agree = append(agree, r)
			}
		}
		if len(agree) < need {
			continue
		}
		s, e := medians(agree)
		out[i] = &Region{StartS: s, EndS: e}
	}
	return out
}

// nearest is up to `neighbours` indices nearest i with a fingerprint,
// earlier first on a tie.
func nearest(fps [][]uint32, i int) []int {
	var out []int
	for dist := 1; len(out) < neighbours && (i-dist >= 0 || i+dist < len(fps)); dist++ {
		for _, j := range []int{i - dist, i + dist} {
			if j >= 0 && j < len(fps) && fps[j] != nil && len(out) < neighbours {
				out = append(out, j)
			}
		}
	}
	return out
}

func medians(rs []Region) (start, end float64) {
	s := make([]float64, len(rs))
	e := make([]float64, len(rs))
	for i, r := range rs {
		s[i], e[i] = r.StartS, r.EndS
	}
	return median(s), median(e)
}

func median(v []float64) float64 {
	slices.Sort(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
