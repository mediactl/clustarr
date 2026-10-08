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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/quality"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The two e2e clips as testmedia Specs: the Dockerfile's old ffmpeg recipes,
// with the probe clip at the 1080p its names claim (W10.11 and the package
// doc's ruling). The HDR10 clip is made and read back here; the 57 MiB CBR
// clip is made only at image build, where run checks its size, and its
// frame size is checked through a one-second cut of the same graph.
func TestTheE2EClipsAreTheOldRecipes(t *testing.T) {
	s := specs()
	tiny := s["tiny.mkv"]
	require.Len(t, tiny.Video, 1)
	assert.Equal(t, probeClip, tiny.Video[0].Source)
	assert.Equal(t, "48M", tiny.Video[0].Options["b"])
	assert.Equal(t, "nal-hrd=cbr:force-cfr=1", tiny.Video[0].Options["x264-params"])
	require.Len(t, tiny.Audio, 1)
	assert.Equal(t, "aac", tiny.Audio[0].Encoder)

	hdr := testmedia.Inspect(t, testmedia.Make(t, "hdr10.mkv", s["hdr10.mkv"]))
	require.Len(t, hdr.Streams, 1)
	assert.Equal(t, "hevc", hdr.Streams[0].Codec)
	assert.Equal(t, "smpte2084", hdr.Streams[0].Color.Transfer)
	assert.Contains(t, hdr.Streams[0].SideData, "Mastering display metadata")
}

// The probe clip reads as the 1080p its names say, so the probe's
// correction of a "1080p" name (quality.AugmentFromMediaInfo, which the
// import and the library rescan both apply) leaves Bluray-1080p alone --
// the smoke test's 640x480 clip turned it into Bluray-480p.
func TestTheProbeClipIsTheResolutionItsNamesClaim(t *testing.T) {
	cut := specs()["tiny.mkv"]
	cut.Video = append([]testmedia.Video(nil), cut.Video...)
	cut.Video[0].Frames = 25
	cut.Audio = nil
	info := testmedia.Inspect(t, testmedia.Make(t, "tiny-cut.mkv", cut))
	require.Len(t, info.Streams, 1)
	assert.Equal(t, "h264", info.Streams[0].Codec)
	assert.Equal(t, []int{1920, 1080}, []int{info.Streams[0].Width, info.Streams[0].Height})

	named := commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone}
	got, changed := quality.AugmentFromMediaInfo(named, &commonv1.MediaInfo{
		Width: int32(info.Streams[0].Width), Height: int32(info.Streams[0].Height), VideoCodec: "h264",
	})
	assert.False(t, changed, "the probe corrected a 1080p name to %s", got.Name)
	assert.Equal(t, "Bluray-1080p", got.Name)
}
