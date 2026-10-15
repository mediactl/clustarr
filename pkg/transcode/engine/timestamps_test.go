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
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
)

// A remux of a source whose video repeats a timestamp -- four packets at
// one time, as USURY's HEVC Blu-ray encodes of 49 Up and 63 Up do
// (2026-10-15) -- muxes with every packet, its decode timestamps strictly
// increasing. MP4 refuses a decode timestamp that does not move forward;
// before the guard every attempt failed in the muxer ("non monotonically
// increasing dts").
func TestACopiedStreamThatRepeatsATimestampStillMuxes(t *testing.T) {
	src := hevcClip(t, "mkv",
		"-x265-params", "log-level=error:bframes=3",
		"-bsf:v", `setts=pts=if(between(N\,20\,23)\,0.75/TB\,PTS)`)
	dts := packetDTS(t, src)
	require.False(t, increasing(dts), "the clip must repeat a decode timestamp, as the damaged sources do: %v", dts)

	out := filepath.Join(t.TempDir(), "out.part.mp4")
	_, err := Run(context.Background(), copyPlan(transcode.ContainerMP4), src, out, Options{})
	require.NoError(t, err)

	got := packetDTS(t, out)
	assert.Len(t, got, len(dts), "every packet is kept")
	assert.True(t, increasing(got), "the output's decode timestamps move forward: %v", got)
}

// increasing reports whether every set decode timestamp is past the set
// one before it.
func increasing(dts []string) bool {
	var prev int64
	seen := false
	for _, s := range dts {
		cur, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			continue
		}
		if seen && cur <= prev {
			return false
		}
		prev, seen = cur, true
	}
	return true
}

// packetDTS is ffprobe's decode timestamp of every video packet, in order;
// "N/A" for one the container leaves unset.
func packetDTS(t *testing.T, path string) []string {
	t.Helper()
	return strings.Fields(run(t, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "packet=dts", "-of", "csv=p=0", path))
}

func TestTheDTSGuardHoldsDecodeTimestampsStrictlyIncreasing(t *testing.T) {
	const none = avutil.AV_NOPTS_VALUE
	ms := ffgo.NewRational(1, 1000)
	tests := []struct {
		name     string
		in, out  ffgo.Rational
		packets  [][2]int64 // pts, dts in
		want     [][2]int64 // pts, dts out
		repaired int
	}{{
		// 63 Up's packets at 1.8 s, in milliseconds.
		name: "a repeated timestamp moves one tick on",
		in:   ms, out: ffgo.NewRational(1, 16000),
		packets: [][2]int64{{2080, 1120}, {1800, 1160}, {1800, 1200}, {1800, 1800}, {1800, 1800}, {2280, 1800}, {2200, 1800}, {2160, 2080}, {2120, 2120}},
		want:    [][2]int64{{2080, 1120}, {1800, 1160}, {1800, 1200}, {1800, 1800}, {1801, 1801}, {2280, 1802}, {2200, 1803}, {2160, 2080}, {2120, 2120}},
		// The second packet at 1800/1800 and the two after it.
		repaired: 3,
	}, {
		name: "the first packets' unset decode timestamps are the muxer's to derive",
		in:   ms, out: ms,
		packets:  [][2]int64{{0, none}, {200, none}, {120, none}, {80, 0}, {40, 40}},
		want:     [][2]int64{{0, none}, {200, none}, {120, none}, {80, 0}, {40, 40}},
		repaired: 0,
	}, {
		// FFmpeg's "Invalid DTS ... replacing by guess": the median of the
		// two and one past the last.
		name: "a decode timestamp after its presentation becomes a guess",
		in:   ms, out: ms,
		packets:  [][2]int64{{0, 0}, {40, 40}, {60, 100}},
		want:     [][2]int64{{0, 0}, {40, 40}, {60, 60}},
		repaired: 1,
	}, {
		// One output tick is 10 of the packet's: a step of one would
		// round onto the same output timestamp.
		name: "the step is one tick of the coarser output time base",
		in:   ffgo.NewRational(1, 1000), out: ffgo.NewRational(1, 100),
		packets:  [][2]int64{{0, 0}, {0, 0}},
		want:     [][2]int64{{0, 0}, {10, 10}},
		repaired: 1,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newDTSGuard(tt.in, tt.out)
			var got [][2]int64
			for _, p := range tt.packets {
				pts, dts := g.fix(p[0], p[1])
				got = append(got, [2]int64{pts, dts})
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.repaired, g.repaired)
		})
	}
}
