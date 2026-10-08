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

package testmedia_test

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/testmedia"
)

// everything is pkg/transcode/engine's everythingClip, as a Spec: H.264
// video, English AC-3 5.1 (default), French AAC stereo "Commentary"
// (comment), English SubRip (forced), a font, two chapters, container
// title "Clip".
func everything(seconds int) testmedia.Spec {
	half := int64(seconds) * 500
	return testmedia.Spec{
		Duration: time.Duration(seconds) * time.Second, Shortest: true, Tags: map[string]string{"title": "Clip", "CLUSTARR_PROFILE": "p@h"},
		Video: []testmedia.Video{{Source: "testsrc2=size=160x90:rate=24", Encoder: "libx264", Options: map[string]string{"preset": "ultrafast"}}},
		Audio: []testmedia.Audio{
			{Source: "sine=frequency=440:sample_rate=48000", Encoder: "ac3", Layout: "5.1(side)", Stream: testmedia.Stream{Language: "eng", Disposition: []string{"default"}}},
			{Source: "sine=frequency=880:sample_rate=48000", Encoder: "aac", Layout: "stereo", Stream: testmedia.Stream{Language: "fre", Title: "Commentary", Disposition: []string{"comment"}}},
		},
		Subtitles:   []testmedia.Subtitle{{Codec: "subrip", Events: []testmedia.Event{{Start: 500 * time.Millisecond, End: 1500 * time.Millisecond, Text: "Hello"}}, Stream: testmedia.Stream{Language: "eng", Disposition: []string{"forced"}}}},
		Attachments: []testmedia.Attachment{{Name: "font.ttf", MIMEType: "application/x-truetype-font", Data: []byte("not really a font")}},
		Chapters:    []testmedia.Chapter{{Start: 0, End: half, TimeBase: [2]int{1, 1000}, Title: "One"}, {Start: half, End: 2 * half, TimeBase: [2]int{1, 1000}, Title: "Two"}},
	}
}

func TestAClipCarriesWhatItsSpecSays(t *testing.T) {
	path := testmedia.Make(t, "everything.mkv", everything(2))
	info := testmedia.Inspect(t, path)
	assert.Equal(t, "matroska,webm", info.Format)
	assert.InDelta(t, 2.0, info.Duration.Seconds(), 0.1)
	assert.Equal(t, "Clip", info.Tags["title"])
	assert.Equal(t, "p@h", info.Tags["CLUSTARR_PROFILE"])
	require.Len(t, info.Streams, 5)
	assert.Equal(t, "h264", info.Streams[0].Codec)
	assert.Equal(t, []any{"ac3", "eng", 6, []string{"default"}}, []any{info.Streams[1].Codec, info.Streams[1].Language, info.Streams[1].Channels, info.Streams[1].Disposition})
	assert.Equal(t, []any{"aac", "fre", "Commentary", 2}, []any{info.Streams[2].Codec, info.Streams[2].Language, info.Streams[2].Title, info.Streams[2].Channels})
	assert.Contains(t, info.Streams[2].Disposition, "comment")
	assert.Equal(t, []any{"subtitle", "subrip", "eng"}, []any{info.Streams[3].Type, info.Streams[3].Codec, info.Streams[3].Language})
	assert.Contains(t, info.Streams[3].Disposition, "forced")
	assert.Equal(t, "attachment", info.Streams[4].Type)
	require.Len(t, info.Chapters, 2)
	assert.Equal(t, "Two", info.Chapters[1].Title)
}

func TestHDR10AndTenBitHEVC(t *testing.T) {
	path := testmedia.Make(t, "hdr10.mkv", testmedia.Spec{Duration: time.Second, Video: []testmedia.Video{{
		Source: "testsrc2=size=160x90:rate=24", Encoder: "libx265", PixFmt: "yuv420p10le",
		Options: map[string]string{"x265-params": "log-level=error"},
		HDR10:   &testmedia.HDR10{MasteringDisplay: [10]uint32{13250, 34500, 7500, 3000, 34000, 16000, 15635, 16450, 10000000, 50}, MaxCLL: 1000, MaxFALL: 400},
	}}})
	info := testmedia.Inspect(t, path)
	require.Len(t, info.Streams, 1)
	assert.Equal(t, "hevc", info.Streams[0].Codec)
	assert.Equal(t, "Main 10", info.Streams[0].Profile)
	assert.Equal(t, "yuv420p10le", info.Streams[0].PixFmt)
	assert.Equal(t, "smpte2084", info.Streams[0].Color.Transfer, "HDR10 implies BT.2020/PQ unless Color says otherwise")
	assert.Equal(t, "bt2020", info.Streams[0].Color.Primaries)
	assert.Contains(t, info.Streams[0].SideData, "Mastering display metadata")
	assert.Contains(t, info.Streams[0].SideData, "Content light level metadata")
	assert.Contains(t, testmedia.Frames(t, path, 0)[0].SideData, "Mastering display metadata")
}

// The inspectors the converted tests assert through, in place of ffprobe's
// stream, frame and packet entries, ffmpeg's astats, and `-c copy` retags.
func TestTheInspectorsReadWhatTheConvertedTestsAssert(t *testing.T) {
	path := testmedia.Make(t, "everything.mkv", everything(2))
	info := testmedia.Inspect(t, path)
	assert.NotEmpty(t, info.Streams[0].Profile)
	assert.NotEmpty(t, info.Streams[0].PixFmt)
	assert.Equal(t, "5.1(side)", info.Streams[1].Layout)
	frames := testmedia.Frames(t, path, 0)
	require.Len(t, frames, 48, "2 s at 24 fps")
	for i := 1; i < len(frames); i++ {
		assert.Greater(t, frames[i].PTS, frames[i-1].PTS)
	}
	per := map[int]int{}
	for _, p := range testmedia.Packets(t, path) {
		per[p.Stream]++
	}
	assert.Equal(t, 48, per[0])
	assert.NotZero(t, per[1])
	rms := testmedia.ChannelRMS(t, path, 1)
	require.Len(t, rms, 6)
	// sine's amplitude is 1/8: an RMS of 20·log10(0.125/√2) = -21.07 dB.
	assert.InDelta(t, -21.07, rms[2], 0.5, "aformat upmixes the mono sine to the centre, as ffmpeg does")
	assert.Less(t, rms[0], -90.0, "and leaves the front left silent")
	re := testmedia.Remux(t, path, "retag.mkv", testmedia.Retag{Tags: map[string]string{"CLUSTARR_PROFILE": "q@r"}, StreamMetadata: map[int]map[string]string{2: {"language": "ger"}}})
	got := testmedia.Inspect(t, re)
	assert.Equal(t, "q@r", got.Tags["CLUSTARR_PROFILE"])
	assert.Equal(t, "Clip", got.Tags["title"], "a retag keeps the tags it does not name")
	assert.Equal(t, "ger", got.Streams[2].Language)
	assert.Len(t, got.Streams, len(info.Streams))
	cut := testmedia.Remux(t, path, "cut.mkv", testmedia.Retag{Until: time.Second})
	video := 0
	for _, p := range testmedia.Packets(t, cut) {
		if p.Stream == 0 {
			video++
			assert.Less(t, p.PTS, time.Second)
		}
	}
	assert.Equal(t, 24, video, "Until drops the packets past it")
}

func TestTruncateAndTheSameSpecWritesTheSameBytes(t *testing.T) {
	a := testmedia.Make(t, "a.mkv", everything(1))
	b := filepath.Join(t.TempDir(), "b.mkv")
	require.NoError(t, testmedia.Write(b, everything(1)))
	ba, err := os.ReadFile(a)
	require.NoError(t, err)
	bb, err := os.ReadFile(b)
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256(ba), sha256.Sum256(bb), "bit-exact: the goldens' clips are reproducible on one FFmpeg build")
	cut := testmedia.Truncate(t, a, 4096)
	st, err := os.Stat(cut)
	require.NoError(t, err)
	assert.Equal(t, int64(4096), st.Size())
}
