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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

func hevcOutput(t *testing.T) (src, out string) {
	t.Helper()
	src = videoClip(t, "src.mkv", "-c:v", "libx264", "-preset", "veryfast")
	out = filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), encodePlan(cpuVideo("sdr", standard.ColorTags{})), src, out, Options{})
	require.NoError(t, err)
	return src, out
}

func TestVerifyPassesAGoodOutput(t *testing.T) {
	src, out := hevcOutput(t)
	exp := standard.Expectation{VideoStreams: 1, VideoCodec: "hevc", PixelFormat: "yuv420p10le", DurationMillis: 2000}
	r, err := Verify(context.Background(), src, out, exp)
	require.NoError(t, err)
	assert.True(t, r.OK, "%v", r.Problems)
	assert.Positive(t, r.SizeBytes)
}

func TestVerifyNamesEachProblem(t *testing.T) {
	src, out := hevcOutput(t)
	for name, c := range map[string]struct {
		exp  standard.Expectation
		want string
	}{
		"streams":  {standard.Expectation{VideoStreams: 1, AudioStreams: 1, VideoCodec: "hevc", PixelFormat: "yuv420p10le"}, "audio streams"},
		"codec":    {standard.Expectation{VideoStreams: 1, VideoCodec: "av1", PixelFormat: "yuv420p10le"}, "video codec"},
		"pixfmt":   {standard.Expectation{VideoStreams: 1, VideoCodec: "hevc", PixelFormat: "yuv420p"}, "pixel format"},
		"duration": {standard.Expectation{VideoStreams: 1, VideoCodec: "hevc", PixelFormat: "yuv420p10le", DurationMillis: 4000}, "duration"},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := Verify(context.Background(), src, out, c.exp)
			require.NoError(t, err)
			assert.False(t, r.OK)
			assert.Contains(t, joined(r.Problems), c.want)
		})
	}
}

func joined(p []string) string {
	s := ""
	for _, x := range p {
		s += x + "; "
	}
	return s
}
