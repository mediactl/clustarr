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

package ffruntime_test

import (
	"path/filepath"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/test/nativetest"
)

// Load against the pinned build: FFmpeg 9, the shim at the API this build
// needs, and the paths the loader really mapped.
func TestLoadReportsFFmpeg9AndTheShim(t *testing.T) {
	rep := nativetest.FFmpeg(t, ffruntime.Needs{})
	assert.Equal(t, 9, rep.FFmpegMajor)
	assert.GreaterOrEqual(t, rep.ShimAPI, ffruntime.MinShimAPI)
	assert.Contains(t, filepath.Base(rep.AVCodec), "libavcodec.so")
	assert.Contains(t, filepath.Base(rep.ShimPath), "libffshim.so")
}

// TestShimAPIMatchesTheFork pins the shim to the fork this module builds
// against (spec §7.7): a newer fork that raised FFSHIM_API_VERSION must
// raise MinShimAPI with it.
func TestShimAPIMatchesTheFork(t *testing.T) {
	nativetest.FFmpeg(t, ffruntime.Needs{})
	assert.Equal(t, ffruntime.MinShimAPI, ffgo.ShimAPIVersion())
}

func TestRequireNamesWhatIsMissing(t *testing.T) {
	nativetest.FFmpeg(t, ffruntime.Needs{})
	err := ffruntime.Require(ffruntime.Needs{Muxers: []string{"no-such-muxer"}, Decoders: []string{"no-such-decoder"}, Filters: []string{"no-such-filter"}})
	require.ErrorIs(t, err, ffruntime.ErrUnavailable)
	assert.ErrorContains(t, err, "muxer no-such-muxer")
	assert.ErrorContains(t, err, "decoder no-such-decoder")
	assert.ErrorContains(t, err, "filter no-such-filter")
	assert.NoError(t, ffruntime.Require(ffruntime.Needs{Demuxers: []string{"matroska"}, Muxers: []string{"mp4", "srt", "ass"},
		Decoders: []string{"h264"}, Encoders: []string{"srt"}, Filters: []string{"scale"}}))
}
