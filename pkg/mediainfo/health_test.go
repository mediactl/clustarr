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

package mediainfo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// timeline is a stream with a packet every step from 0 to end, less the
// stretches in skip.
func timeline(step, end time.Duration, skip ...Span) *Timeline {
	t := &Timeline{}
next:
	for ts := time.Duration(0); ts < end; ts += step {
		for _, s := range skip {
			if ts >= s.From && ts < s.To {
				continue next
			}
		}
		t.Add(ts)
	}
	return t
}

func TestHolesAreTheStretchesEveryStreamSkips(t *testing.T) {
	const (
		frame = time.Second / 24
		audio = 21 * time.Millisecond
		end   = 60 * time.Second
	)
	sec := func(a, b float64) Span {
		return Span{time.Duration(a * float64(time.Second)), time.Duration(b * float64(time.Second))}
	}
	tests := []struct {
		name    string
		streams []*Timeline
		end     time.Duration
		holes   int
		missing time.Duration
		damaged bool
	}{
		{name: "whole", streams: []*Timeline{timeline(frame, end), timeline(audio, end)}, end: end},
		{
			name:    "a still held in a variable-rate video is no hole: the sound runs on",
			streams: []*Timeline{timeline(frame, end, sec(10, 25)), timeline(audio, end)}, end: end,
		},
		{
			name:    "an audio track that starts late and ends early",
			streams: []*Timeline{timeline(frame, end), timeline(audio, 50*time.Second, sec(0, 5))}, end: end,
		},
		{
			name:    "one lost article: a second gone from every stream, within what a file may lack",
			streams: []*Timeline{timeline(frame, end, sec(20, 21)), timeline(audio, end, sec(20, 21)), timeline(audio, end, sec(20, 21))},
			end:     end, holes: 1, missing: time.Second,
		},
		{
			name: "holes through the file",
			streams: []*Timeline{
				timeline(frame, end, sec(5, 7), sec(20, 29), sec(40, 41.5)),
				timeline(audio, end, sec(5, 6.5), sec(20, 28), sec(40, 41.5)),
			},
			end: end, holes: 3, missing: 11 * time.Second, damaged: true,
		},
		{
			name:    "cut short: the container still states its full length",
			streams: []*Timeline{timeline(frame, 30*time.Second), timeline(audio, 30*time.Second)},
			end:     end, holes: 1, missing: 30 * time.Second, damaged: true,
		},
		{
			name:    "a subtitle that outlasts picture and sound by a few seconds is not a cut",
			streams: []*Timeline{timeline(frame, end), timeline(audio, end)}, end: end + 6*time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Holes(tt.end, tt.end, tt.streams)
			require.True(t, h.Judged)
			require.Equal(t, tt.holes, h.Holes)
			require.InDelta(t, tt.missing.Seconds(), h.Missing.Seconds(), 0.2)
			require.Equal(t, tt.damaged, h.Damaged())
			if tt.damaged {
				require.ErrorIs(t, h.Err(), ErrDamaged)
			} else {
				require.NoError(t, h.Err())
			}
		})
	}
}

// A video's presentation times arrive out of order by a few frames: the
// step back is no gap, and neither is the step forward after it.
func TestATimelineTakesTimestampsOutOfOrder(t *testing.T) {
	const frame = time.Second / 24
	tl := &Timeline{}
	for i := 0; i < 24*30; i += 4 {
		for _, k := range []int{3, 1, 0, 2} { // P, B, B, B in decode order
			tl.Add(time.Duration(i+k) * frame)
		}
	}
	h := Holes(30*time.Second, 30*time.Second, []*Timeline{tl, timeline(21*time.Millisecond, 30*time.Second)})
	require.Zero(t, h.Holes)
}

func TestAFileWithNoStreamsIsNotJudged(t *testing.T) {
	h := Holes(time.Minute, time.Minute, []*Timeline{{}, nil})
	require.False(t, h.Judged)
	require.False(t, h.Damaged())
}
