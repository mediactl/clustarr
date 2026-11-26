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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/mediainfo/native"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// realProber is the in-process probe the worker plans from, skipping without
// FFmpeg 9 and the shim; T2a's nativetest.FFmpeg(t, native.Needs) replaces
// the skip (W8.7).
func realProber(t *testing.T) *native.Prober {
	t.Helper()
	p, err := native.New()
	if err != nil {
		t.Skipf("no FFmpeg 9 with its shim: %v", err)
	}
	return p
}

// clip makes a 2 s file on internal/testmedia: a test picture as video
// encodes it, a tone in AAC and two chapters.
func clip(t *testing.T, name string, video testmedia.Video) string {
	t.Helper()
	video.Source = "testsrc2=size=320x180:rate=24:duration=2"
	return testmedia.Make(t, name, testmedia.Spec{
		Video: []testmedia.Video{video},
		Audio: []testmedia.Audio{{Source: "sine=frequency=440:sample_rate=48000:duration=2"}},
		Chapters: []testmedia.Chapter{
			{Start: 0, End: 1000, TimeBase: [2]int{1, 1000}, Title: "One"},
			{Start: 1000, End: 2000, TimeBase: [2]int{1, 1000}, Title: "Two"},
		},
	})
}

// hevc10Video is the standard's own video as a clip: HEVC, 10-bit, with the
// ENCODER tag ffmpeg(1) writes on a stream it encodes, which is what tells
// another tool's transcode (final, remuxed) from a release's 10-bit HEVC
// (encoded: commonv1.MediaInfo.TranscodedElsewhere).
var hevc10Video = testmedia.Video{
	Encoder: "libx265", PixFmt: "yuv420p10le",
	Options: map[string]string{"preset": "ultrafast", "x265-params": "log-level=error"},
	Stream:  testmedia.Stream{Metadata: map[string]string{"ENCODER": "Lavc63.11.100 libx265"}},
}

// The controller plans from the stored probe summary and the worker from a
// live probe of the same file; both must reach the same decision and the
// same plan hash, or every job is refused or re-planned at the worker.
func TestTheSummaryAndTheProbePlanTheSameStandard(t *testing.T) {
	for name, c := range map[string]struct {
		src  func() string
		want Decision
	}{
		"compliant mkv": {func() string { return clip(t, "c.mkv", hevc10Video) }, DecisionCopyVideo}, // remuxed into MP4
		"h264 mkv": {func() string {
			return clip(t, "h.mkv", testmedia.Video{Options: map[string]string{"preset": "ultrafast"}})
		}, DecisionEncode},
		"compliant mp4": {func() string {
			// The muxer's own tag for HEVC is hev1; the standard's is hvc1 (`-tag:v hvc1`).
			return testmedia.Remux(t, clip(t, "src.mkv", hevc10Video), "c.mp4", testmedia.Retag{CodecTags: map[int]string{0: "hvc1"}})
		}, DecisionSkip}, // already the MP4 layout
	} {
		t.Run(name, func(t *testing.T) {
			src := c.src()
			mi, raw, err := realProber(t).Probe(context.Background(), src)
			require.NoError(t, err)
			live, err := transcode.FromProbe(mi, raw)
			require.NoError(t, err)
			stored, err := transcode.FromSummary(src, mi)
			require.NoError(t, err)

			fromProbe, fromSummary := Plan(live, profile, cpu), Plan(stored, profile, cpu)
			assert.Equal(t, c.want, fromSummary.Decision, fromSummary.Reason)
			assert.Equal(t, fromProbe.Decision, fromSummary.Decision)
			assert.Equal(t, fromProbe.Hash(), fromSummary.Hash())
		})
	}
}

func TestContainerNamesFromTheSummaryAndTheDemuxer(t *testing.T) {
	for name, want := range map[string]transcode.Container{
		"matroska,webm": transcode.ContainerMKV, "mkv": transcode.ContainerMKV, "webm": transcode.ContainerMKV,
		"mov,mp4,m4a,3gp,3g2,mj2": transcode.ContainerMP4, "mp4": transcode.ContainerMP4, "m4v": transcode.ContainerMP4, "mov": transcode.ContainerMP4,
		"mpegts": "", "avi": "", "": "",
	} {
		assert.Equal(t, want, containerOf(name), name)
	}
}

// Sidecar names read a subtitle's language, forced and SDH flags and
// title, so the stored summary must carry them as the live probe does:
// both plan a "Signs" ASS track, a forced SRT, an SDH SRT and a plain one
// alike (Review Focus 5 of the MP4 standard's phase 1 plan).
func TestTheSummaryAndTheProbePlanSubtitlesAlike(t *testing.T) {
	cue := []testmedia.Event{{Start: 500 * time.Millisecond, End: 1500 * time.Millisecond, Text: "Hello"}}
	out := testmedia.Make(t, "subs.mkv", testmedia.Spec{
		Video: []testmedia.Video{{Source: "testsrc2=size=320x180:rate=24:duration=2", Options: map[string]string{"preset": "ultrafast"}}},
		Audio: []testmedia.Audio{{Source: "sine=frequency=440:sample_rate=48000:duration=2"}},
		Subtitles: []testmedia.Subtitle{
			{
				Codec:  "ass",
				Events: []testmedia.Event{{Start: 500 * time.Millisecond, End: 1500 * time.Millisecond, Text: "Sign"}},
				Stream: testmedia.Stream{Language: "eng", Title: "Signs"},
			},
			{Codec: "subrip", Events: cue, Stream: testmedia.Stream{Language: "ger", Disposition: []string{"forced"}}},
			{Codec: "subrip", Events: cue, Stream: testmedia.Stream{Language: "eng", Disposition: []string{"hearing_impaired"}}},
			{Codec: "subrip", Events: cue, Stream: testmedia.Stream{Language: "spa"}},
		},
	})
	mi, raw, err := realProber(t).Probe(context.Background(), out)
	require.NoError(t, err)
	live, err := transcode.FromProbe(mi, raw)
	require.NoError(t, err)
	stored, err := transcode.FromSummary(out, mi)
	require.NoError(t, err)
	fromProbe, fromSummary := Plan(live, profile, cpu), Plan(stored, profile, cpu)
	require.Len(t, fromSummary.Sidecars, 4, "%+v", fromSummary.Sidecars)
	assert.Equal(t, fromProbe.Sidecars, fromSummary.Sidecars)
	assert.Equal(t, fromProbe.Subtitles, fromSummary.Subtitles)
	assert.Equal(t, fromProbe.Hash(), fromSummary.Hash())
}
