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
	"fmt"
	"os"

	"github.com/obinnaokechukwu/ffgo"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// durationTolMillis is how far the output's duration may drift from the
// source's: the argv engine's tolerance.
const durationTolMillis = 1000

// Verify probes output through ffgo (no ffprobe) and checks it against exp:
// stream counts by kind, HEVC Main 10's codec and pixel format, and the
// duration (exp's, else the source's) within a second. A probe that cannot
// run is an error; a mismatch is a Report with OK false and each problem.
func Verify(ctx context.Context, source, output string, exp standard.Expectation) (*transcode.Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	st, err := os.Stat(output)
	if err != nil {
		return nil, fmt.Errorf("engine: verify: %w", err)
	}
	d, err := ffgo.NewDecoder(output)
	if err != nil {
		return nil, fmt.Errorf("engine: verify: open output: %w", err)
	}
	defer func() { _ = d.Close() }()
	r := &transcode.Report{SizeBytes: st.Size()}
	var vids, auds, subs []*ffgo.StreamInfo
	for _, s := range d.Streams() {
		switch s.Type {
		case ffgo.MediaTypeVideo:
			vids = append(vids, s)
		case ffgo.MediaTypeAudio:
			auds = append(auds, s)
		case ffgo.MediaTypeSubtitle:
			subs = append(subs, s)
		}
	}
	problem := func(format string, a ...any) { r.Problems = append(r.Problems, fmt.Sprintf(format, a...)) }
	if int32(len(vids)) != exp.VideoStreams {
		problem("%d video streams, want %d", len(vids), exp.VideoStreams)
	}
	if int32(len(auds)) != exp.AudioStreams {
		problem("%d audio streams, want %d", len(auds), exp.AudioStreams)
	}
	if int32(len(subs)) != exp.SubtitleStreams {
		problem("%d subtitle streams, want %d", len(subs), exp.SubtitleStreams)
	}
	if len(vids) > 0 {
		if exp.VideoCodec != "" && vids[0].Codec != exp.VideoCodec {
			problem("video codec %q, want %q", vids[0].Codec, exp.VideoCodec)
		}
		if exp.PixelFormat != "" && vids[0].PixelFmt != ffgo.PixelFormatByName(exp.PixelFormat) {
			problem("pixel format %d, want %s (%d)", vids[0].PixelFmt, exp.PixelFormat, ffgo.PixelFormatByName(exp.PixelFormat))
		}
	}
	want := exp.DurationMillis
	if want == 0 {
		if sd, err := ffgo.NewDecoder(source); err == nil {
			want = sd.Duration().Milliseconds()
			_ = sd.Close()
		}
	}
	if got := d.Duration().Milliseconds(); want > 0 && abs(got-want) > durationTolMillis {
		problem("duration %d ms, want %d ms (within %d ms)", got, want, durationTolMillis)
	}
	r.OK = len(r.Problems) == 0
	return r, nil
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
