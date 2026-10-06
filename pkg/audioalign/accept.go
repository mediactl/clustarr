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

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Thresholds decide whether an alignment is trusted enough to mux.
type Thresholds struct {
	MinCoverage       float64
	MaxSegments       int
	MinSegmentWindows int
	MinRateMargin     float64
}

// DefaultThresholds are the spike's starting values (spec §7.1), set from
// gate G1's real pair (spec §10).
var DefaultThresholds = Thresholds{MinCoverage: 0.6, MaxSegments: 4, MinSegmentWindows: 3, MinRateMargin: 1.02}

// Accept reports why r is not trusted, or nil.
func (r Result) Accept(t Thresholds) error {
	switch {
	case len(r.Segments) == 0:
		return fmt.Errorf("audioalign: no confident alignment (%d of %d windows confident)", r.Confident, r.Windows)
	case r.Coverage < t.MinCoverage:
		return fmt.Errorf("audioalign: confident windows cover %.0f%% of the target, under %.0f%%", 100*r.Coverage, 100*t.MinCoverage)
	case len(r.Segments) > t.MaxSegments:
		return fmt.Errorf("audioalign: %d segments, over %d: the releases are cut too differently", len(r.Segments), t.MaxSegments)
	case r.RateMargin < t.MinRateMargin:
		return fmt.Errorf("audioalign: rate %s is not clearly better than the next candidate (margin %.3f)", r.RateName, r.RateMargin)
	}
	for _, s := range r.Segments {
		if s.Length < time.Duration(t.MinSegmentWindows*stepFrames)*time.Duration(frameSeconds*float64(time.Second))/2 {
			return fmt.Errorf("audioalign: a segment of %s is too short to trust", s.Length)
		}
	}
	return nil
}

// Transform is what a graft applies to a donor track: stretched by r.Rate,
// each segment placed at its target position, silence elsewhere, cut to
// targetLen samples.
func Transform(donor []float32, r Result, targetLen int) []float32 {
	n := int(float64(len(donor)) * r.Rate)
	st := make([]float32, n)
	for i := range st {
		src := float64(i) / r.Rate
		j := int(src)
		if j+1 >= len(donor) {
			break
		}
		f := float32(src - float64(j))
		st[i] = donor[j]*(1-f) + donor[j+1]*f
	}
	out := make([]float32, targetLen)
	for _, s := range r.Segments {
		ds := int(s.DonorStart.Seconds() * SampleRate)
		ts := int(s.TargetStart.Seconds() * SampleRate)
		ln := int(s.Length.Seconds() * SampleRate)
		for i := 0; i < ln; i++ {
			if ds+i >= len(st) || ts+i >= len(out) || ts+i < 0 || ds+i < 0 {
				continue
			}
			out[ts+i] = st[ds+i]
		}
	}
	return out
}

// Verify applies r to the donor's anchor track and aligns the result
// against the target's anchor again: the median residual offset of the
// confident windows, and the share of them within 80 ms.
func Verify(donorAnchor, targetAnchor []float32, r Result) (time.Duration, float64) {
	moved := Transform(donorAnchor, r, len(targetAnchor))
	d, t := extract(moved), extract(targetAnchor)
	if d.frames() < winFrames || t.frames() < winFrames {
		return time.Duration(math.MaxInt64), 0
	}
	ws := newTarget(t).windows(d, 1)
	var res []int
	for _, w := range ws {
		if w.confident {
			res = append(res, abs(w.lag))
		}
	}
	if len(res) == 0 {
		return time.Duration(math.MaxInt64), 0
	}
	sort.Ints(res)
	within := 0
	for _, v := range res {
		if v <= 4 {
			within++
		}
	}
	return frames(res[len(res)/2]), float64(within) / float64(len(res))
}
