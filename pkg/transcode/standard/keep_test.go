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

package standard

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// An 8-bit SDR H.264 encode no smaller than its source keeps the source's
// video (owner, 2026-11-15: Spare Me, Great Lord! S01E11, a 1.1 Mbps WEB-DL
// whose hevc_qsv encode came out 110%): the file still takes the MP4
// layout the encode would have given it -- its audio, sidecars and tags --
// with the video copied and tagged CLUSTARR_VIDEO=keep.
func TestAnH264EncodeNoSmallerThanItsSourceKeepsItsVideo(t *testing.T) {
	in := info(h264, eac3)
	p := Plan(in, profile, cpu)
	require.Equal(t, DecisionEncode, p.Decision, p.Reason)
	p.Tags["CLUSTARR_GRAFT"] = "donor@1" // a joined graft rides along

	k, ok := KeepVideo(p, in)
	require.True(t, ok)
	assert.Equal(t, DecisionCopyVideo, k.Decision)
	assert.Equal(t, VideoPlan{SourceIndex: 0, Action: "copy"}, k.Video)
	assert.Equal(t, "h264", k.Expect.VideoCodec, "verified as the H.264 it is")
	assert.Equal(t, "yuv420p", k.Expect.PixelFormat)
	assert.Equal(t, map[string]string{
		ProfileTagKey: "hevc-mkv@abc", VideoTagKey: VideoKept, "CLUSTARR_GRAFT": "donor@1",
	}, k.Tags)
	assert.Equal(t, p.Audio, k.Audio, "the encode's audio layout")
	assert.Equal(t, p.Sidecars, k.Sidecars, "the encode's sidecars")
	assert.Equal(t, p.Container, k.Container)
	assert.Equal(t, p.Expect.AudioStreams, k.Expect.AudioStreams)
	assert.Equal(t, p.Expect.DurationMillis, k.Expect.DurationMillis)
	assert.NotEqual(t, p.Hash(), k.Hash(), "a plan of its own")
	assert.Equal(t, VideoEncoded, p.Tags[VideoTagKey], "the encode's plan is left as it was")
}

// Only an encode made to save space has a kept video: a release's 10-bit
// HEVC, Hi10P, HDR or Dolby Vision H.264 and AV1 are encoded for
// playability, and a plan that encodes nothing has nothing to keep.
func TestOnlyAnEncodeMadeToSaveSpaceKeepsItsVideo(t *testing.T) {
	for name, v := range map[string]struct {
		codec, pixfmt string
		bits          int32
		hdr           commonv1.HdrFormat
	}{
		"a release's 10-bit HEVC": {"hevc", "yuv420p10le", 10, commonv1.HdrFormatNone},
		"Hi10P":                   {"h264", "yuv420p10le", 10, commonv1.HdrFormatNone},
		"HDR10 H.264":             {"h264", "yuv420p", 8, commonv1.HdrFormatHDR10},
		"Dolby Vision H.264":      {"h264", "yuv420p", 8, commonv1.HdrFormatDolbyVisionSDR},
		"AV1":                     {"av1", "yuv420p10le", 10, commonv1.HdrFormatNone},
	} {
		t.Run(name, func(t *testing.T) {
			in := info(video(v.codec, v.pixfmt, v.bits, v.hdr), eac3)
			p := Plan(in, profile, cpu)
			require.Equal(t, DecisionEncode, p.Decision, p.Reason)
			_, ok := KeepVideo(p, in)
			assert.False(t, ok)
		})
	}

	keep := profile
	keep.KeepCodecs = []string{"hevc", "h264"}
	in := info(h264, eac3)
	remux := Plan(in, keep, cpu)
	require.Equal(t, DecisionCopyVideo, remux.Decision, remux.Reason)
	_, ok := KeepVideo(remux, in)
	assert.False(t, ok, "a remux was never an encode")
}

// A kept video is final: the profile that no longer keeps H.264 encodes a
// remux that copied it (CLUSTARR_VIDEO=copy), but never one kept because
// its encode was no smaller, which would fail the same way every time.
func TestAKeptVideoIsNeverEncodedAgain(t *testing.T) {
	tagged := func(videoTag string) Decision {
		in := mp4Info(h264, audio(0, "aac", 2, "stereo", "eng"))
		in.Tags = map[string]string{ProfileTagKey: "hevc-mkv@old", VideoTagKey: videoTag}
		return Plan(in, profile, cpu).Decision
	}
	assert.Equal(t, DecisionSkip, tagged(VideoKept))
	assert.Equal(t, DecisionEncode, tagged(VideoCopied), "a copy kept only by policy.keepCodecs is encoded once")
}
