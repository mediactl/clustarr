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
	"errors"
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
	if err := r.inDonorOrder(); err != nil {
		return err
	}
	for _, s := range r.Segments {
		if s.Length < time.Duration(t.MinSegmentWindows*stepFrames)*time.Duration(frameSeconds*float64(time.Second))/2 {
			return fmt.Errorf("audioalign: a segment of %s is too short to trust", s.Length)
		}
	}
	return nil
}

// DonorAt maps a target time to the donor time whose audio plays there:
// within the covering segment the offset on the stretched donor, divided
// by the rate. It reads false where no segment covers t (the target's own
// material, which a graft leaves silent). Where segments overlap, the
// later one wins, as Transform has always placed them.
func (r Result) DonorAt(t time.Duration) (time.Duration, bool) {
	s, ok := r.DonorSeconds(t.Seconds())
	return time.Duration(math.Round(s * float64(time.Second))), ok
}

// DonorSeconds is DonorAt in seconds, for a caller mapping samples.
func (r Result) DonorSeconds(t float64) (float64, bool) {
	rate := r.Rate
	if rate <= 0 {
		rate = 1
	}
	for i := len(r.Segments) - 1; i >= 0; i-- {
		s := r.Segments[i]
		ts, ln := s.TargetStart.Seconds(), s.Length.Seconds()
		if t >= ts && t < ts+ln {
			return (s.DonorStart.Seconds() + t - ts) / rate, true
		}
	}
	return 0, false
}

// Transform is what the graft mux applies, at 8 kHz: each target sample is
// the donor's at DonorAt (linearly interpolated), silence where DonorAt
// reads false.
func Transform(donor []float32, r Result, targetLen int) []float32 {
	out := make([]float32, targetLen)
	for i := range out {
		d, ok := r.DonorSeconds(float64(i) / SampleRate)
		if !ok {
			continue
		}
		src := d * SampleRate
		j := int(src)
		if j < 0 || j+1 >= len(donor) {
			continue
		}
		f := float32(src - float64(j))
		out[i] = donor[j]*(1-f) + donor[j+1]*f
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

// ErrSegmentOrder is Accept's refusal of segments that go back on the
// donor as they go forward on the target: the graft mux reads the donor
// forward once, through a sliding buffer.
var ErrSegmentOrder = errors.New("audioalign: the segments go back on the donor")

func (r Result) inDonorOrder() error {
	segs := append([]Segment(nil), r.Segments...)
	sort.Slice(segs, func(i, j int) bool { return segs[i].TargetStart < segs[j].TargetStart })
	for i := 1; i < len(segs); i++ {
		if segs[i].DonorStart < segs[i-1].DonorStart {
			return fmt.Errorf("%w: target %s starts at donor %s, after target %s at donor %s",
				ErrSegmentOrder, segs[i].TargetStart, segs[i].DonorStart, segs[i-1].TargetStart, segs[i-1].DonorStart)
		}
	}
	return nil
}
