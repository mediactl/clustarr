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

package audioalign_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/audioalign"
)

// TestDonorAtMapsTargetTimeThroughSegments: the graft mux reads each output
// sample from the donor time DonorAt gives, so DonorAt is Transform's
// mapping -- stretched-donor offset within the covering segment, divided by
// the rate -- and reads false in a gap no segment covers.
func TestDonorAtMapsTargetTimeThroughSegments(t *testing.T) {
	r := audioalign.Result{Rate: 2, Segments: []audioalign.Segment{
		{DonorStart: 0, TargetStart: time.Second, Length: 10 * time.Second},
		{DonorStart: 14 * time.Second, TargetStart: 12 * time.Second, Length: 10 * time.Second},
	}}
	cases := []struct {
		at   time.Duration
		want time.Duration
		ok   bool
	}{
		{0, 0, false},          // before the first segment
		{time.Second, 0, true}, // its start
		{6 * time.Second, 2500 * time.Millisecond, true}, // 5 s in, stretched 2x
		{11500 * time.Millisecond, 0, false},             // the gap between the two
		{12 * time.Second, 7 * time.Second, true},        // stretched 14 s is donor 7 s
		{22 * time.Second, 0, false},                     // past the end
	}
	for _, c := range cases {
		got, ok := r.DonorAt(c.at)
		require.Equal(t, c.ok, ok, "at %s", c.at)
		if c.ok {
			require.Equal(t, c.want, got, "at %s", c.at)
		}
	}
}

// TestTransformFollowsDonorAt: the 8 kHz Transform Verify applies and the
// graft mux are one mapping, so a sample Transform places is the donor's
// sample at DonorAt.
func TestTransformFollowsDonorAt(t *testing.T) {
	donor := make([]float32, 8*audioalign.SampleRate)
	for i := range donor {
		donor[i] = float32(i)
	}
	r := audioalign.Result{Rate: 1, Segments: []audioalign.Segment{{DonorStart: 2 * time.Second, TargetStart: 0, Length: 3 * time.Second}}}
	out := audioalign.Transform(donor, r, 6*audioalign.SampleRate)
	at := 1500 * time.Millisecond
	d, ok := r.DonorAt(at)
	require.True(t, ok)
	require.Equal(t, donor[int(d.Seconds()*audioalign.SampleRate)], out[int(at.Seconds()*audioalign.SampleRate)])
	require.Zero(t, out[4*audioalign.SampleRate], "outside every segment is silence")
}

// TestAcceptRefusesSegmentsOutOfDonorOrder: the mux reads the donor
// forward through a sliding buffer, so a later target segment must start
// later on the donor too (a reordered recap is refused, not mangled).
func TestAcceptRefusesSegmentsOutOfDonorOrder(t *testing.T) {
	seg := func(d, tg time.Duration) audioalign.Segment {
		return audioalign.Segment{DonorStart: d, TargetStart: tg, Length: 5 * time.Minute}
	}
	ok := audioalign.Result{Rate: 1, RateMargin: 2, Coverage: 0.9, Segments: []audioalign.Segment{seg(0, 0), seg(6*time.Minute, 5*time.Minute)}}
	require.NoError(t, ok.Accept(audioalign.DefaultThresholds))
	back := audioalign.Result{Rate: 1, RateMargin: 2, Coverage: 0.9, Segments: []audioalign.Segment{seg(6*time.Minute, 0), seg(0, 5*time.Minute)}}
	require.True(t, errors.Is(back.Accept(audioalign.DefaultThresholds), audioalign.ErrSegmentOrder))
}

// matchedSeconds reports, for each whole second of target in [from, to)
// not within 1 s of a cut at skip, whether moved -- the donor through
// Transform -- carries that second of the target (normalised correlation
// at least 0.5).
func matchedSeconds(target, moved []float32, from, to int, skip ...int) (bad []int) {
	for s := from; s < to; s++ {
		near := false
		for _, c := range skip {
			if s >= c-1 && s <= c+1 {
				near = true
			}
		}
		if near {
			continue
		}
		var xy, xx, yy float64
		for i := s * audioalign.SampleRate; i < (s+1)*audioalign.SampleRate && i < len(target) && i < len(moved); i++ {
			a, b := float64(target[i]), float64(moved[i])
			xy, xx, yy = xy+a*b, xx+a*a, yy+b*b
		}
		if xx == 0 || yy == 0 || xy/math.Sqrt(xx*yy) < 0.5 {
			bad = append(bad, s)
		}
	}
	return bad
}

// TestTheDonorsExtraMaterialIsDroppedAtTheCut: where the donor has
// material the target lacks, neither offset fits it, so the boundary must be
// found where it is sharp -- on the target -- or the dub plays the donor's
// extra over the target's own audio (final review: target 195-199 s
// carried the inserted material).
func TestTheDonorsExtraMaterialIsDroppedAtTheCut(t *testing.T) {
	target := soundtrack(4, 600)
	donor := insert(target, 200*time.Second, soundtrack(99, 8))
	r, err := audioalign.Align(donor, target)
	require.NoError(t, err)
	moved := audioalign.Transform(donor, r, len(target))
	require.Empty(t, matchedSeconds(target, moved, 150, 250, 200), "seconds of the target the dub does not carry")
}

// TestTheTargetsExtraMaterialIsSilentAtTheCut: the other way, the donor
// lacks a stretch the target has; around it the dub still matches.
func TestTheTargetsExtraMaterialIsSilentAtTheCut(t *testing.T) {
	target := soundtrack(3, 600)
	donor := cut(target, 300*time.Second, 10*time.Second)
	r, err := audioalign.Align(donor, target)
	require.NoError(t, err)
	moved := audioalign.Transform(donor, r, len(target))
	require.Empty(t, matchedSeconds(target, moved, 250, 350, 300, 301, 302, 303, 304, 305, 306, 307, 308, 309, 310),
		"seconds of the target the dub does not carry")
}
