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

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// The MP4 standard's interim Matroska layout (standard.Version 7): a file
// whose image subtitle no text track covers has its video and audio given
// the layout and the image subtitle copied into a Matroska output, tagged
// as any transcode, with its text subtitles beside it, until phase 2's OCR.
// FFmpeg encodes no PGS or DVD subtitle, so the copied stream here is the
// clip's SubRip one; the engine copies a subtitle stream's packets and codec
// parameters whatever the codec, as it does for a graft's copy plan.
func TestTheInterimMatroskaLayoutKeepsASubtitleStreamAndTheTag(t *testing.T) {
	src := everythingClip(t, 2)
	out := filepath.Join(t.TempDir(), "interim.mkv")
	plan := standard.Result{
		Decision: standard.DecisionCopyVideo, Container: transcode.ContainerMKV,
		Video: standard.VideoPlan{SourceIndex: 0, Action: "copy"},
		Audio: []standard.AudioPlan{
			{SourceIndex: 0, Action: standard.AudioAAC, Channels: 2, Layout: "stereo", BitRate: 160000, Language: "eng", Title: "Stereo", Default: true},
			{SourceIndex: 0, Action: standard.AudioCopy, Language: "eng"},
		},
		Subtitles: []standard.SubtitlePlan{{SourceIndex: 0, Action: standard.SubtitleCopy, Codec: "subrip", Language: "eng"}},
		Chapters:  true,
		Tags:      map[string]string{"CLUSTARR_PROFILE": "hevc-mkv@abc"},
		Expect:    standard.Expectation{VideoStreams: 1, AudioStreams: 2, SubtitleStreams: 1, VideoCodec: "h264", PixelFormat: "yuv420p"},
	}
	_, err := Run(context.Background(), plan, src, out, Options{})
	require.NoError(t, err)
	rep, err := Verify(context.Background(), src, out, plan.Expect)
	require.NoError(t, err)
	assert.True(t, rep.OK, "%v", rep.Problems)

	p := ffprobeJSON(t, out)
	var subs, audio []string
	for _, s := range p.Streams {
		switch s.CodecType {
		case "subtitle":
			subs = append(subs, s.CodecName+"/"+s.Tags["language"])
		case "audio":
			audio = append(audio, s.CodecName)
		}
	}
	assert.Equal(t, []string{"subrip/eng"}, subs, "the subtitle stream rides in the Matroska output")
	assert.Equal(t, []string{"aac", "ac3"}, audio, "the audio is the layout's")
	assert.Equal(t, "hevc-mkv@abc", p.Format.Tags["CLUSTARR_PROFILE"], "tagged as any transcode, so it is final until the OCR")
	assert.Len(t, p.Chapters, 2)
}
