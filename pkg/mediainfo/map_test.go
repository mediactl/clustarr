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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestToMediaInfo(t *testing.T) {
	raw := &Raw{
		Format: &ffprobe.Format{Filename: "movie.mkv", DurationSeconds: 7384.5},
		Streams: []*ffprobe.Stream{
			{Index: 0, CodecType: "video", CodecName: "hevc", Profile: "Main 10", Width: 3840, Height: 2160, PixFmt: "yuv420p10le", RFrameRate: "24000/1001", BitRate: "15000000"},
			{Index: 1, CodecType: "audio", CodecName: "eac3", Profile: "Atmos", Channels: 8, ChannelLayout: "5.1(side)", BitRate: "768000", Disposition: ffprobe.StreamDisposition{Default: 1}, Tags: ffprobe.StreamTags{Language: "eng", Title: "Atmos 7.1"}},
			{Index: 2, CodecType: "audio", CodecName: "ac3", Channels: 6, BitRate: "640000", Disposition: ffprobe.StreamDisposition{Comment: 1}, Tags: ffprobe.StreamTags{Language: "eng", Title: "Commentary"}},
			{Index: 3, CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle", Disposition: ffprobe.StreamDisposition{Forced: 1}, Tags: ffprobe.StreamTags{Language: "eng"}},
			{Index: 4, CodecType: "attachment"},
		},
		Chapters:      []*ffprobe.Chapter{{ID: 0}, {ID: 1}},
		ColorTransfer: "smpte2084",
		MasteringDisplay: &MasteringDisplay{
			GreenX: 13250, GreenY: 34500, BlueX: 7500, BlueY: 3000,
			RedX: 34000, RedY: 16000, WhiteX: 15635, WhiteY: 16450,
			MaxLuminance: 10000000, MinLuminance: 1,
		},
	}

	mi := toMediaInfo(raw)

	assert.Equal(t, "mkv", mi.Container)
	assert.Equal(t, "hevc", mi.VideoCodec)
	assert.Equal(t, "Main 10", mi.VideoProfile)
	assert.Equal(t, int32(10), mi.VideoBitDepth)
	assert.Equal(t, int32(3840), mi.Width)
	assert.Equal(t, int32(2160), mi.Height)
	assert.Equal(t, int32(23976), mi.FpsMilli)
	assert.Equal(t, int32(15000), mi.VideoBitrateKbps)
	assert.Equal(t, commonv1.HdrFormatHDR10, mi.Hdr)
	assert.Nil(t, mi.DoviProfile)
	assert.Equal(t, int64(7384500), mi.RuntimeMillis)

	require.Len(t, mi.Audio, 2)
	assert.Equal(t, "eac3", mi.Audio[0].Codec)
	assert.Equal(t, "Atmos", mi.Audio[0].Profile)
	assert.Equal(t, "eng", mi.Audio[0].Language)
	assert.Equal(t, int32(8), mi.Audio[0].Channels)
	assert.Equal(t, "5.1(side)", mi.Audio[0].ChannelLayout)
	assert.Equal(t, int32(768), mi.Audio[0].BitrateKbps)
	assert.True(t, mi.Audio[0].Default)
	assert.True(t, mi.Audio[1].Commentary)

	require.Len(t, mi.Subtitles, 1)
	assert.Equal(t, "hdmv_pgs_subtitle", mi.Subtitles[0].Codec)
	assert.True(t, mi.Subtitles[0].Forced)
	assert.True(t, mi.Subtitles[0].Bitmap)

	assert.Equal(t, int32(1), mi.Attachments)
	assert.Equal(t, int32(2), mi.Chapters)
}

// MediaInfo.Audio and .Subtitles carry MaxItems=64; the apiserver rejects a
// MediaFile status apply over either WHOLE, so the file would never probe.
func TestToMediaInfoCapsStreamListsAtTheCRDsMaxItems(t *testing.T) {
	raw := &Raw{Format: &ffprobe.Format{Filename: "many.mkv"}}
	raw.Streams = append(raw.Streams, &ffprobe.Stream{Index: 0, CodecType: "video", CodecName: "hevc"})
	for i := range MaxStreamsPerKind + 6 {
		raw.Streams = append(raw.Streams,
			&ffprobe.Stream{Index: 1 + 2*i, CodecType: "audio", CodecName: "aac"},
			&ffprobe.Stream{Index: 2 + 2*i, CodecType: "subtitle", CodecName: "subrip"})
	}
	mi := toMediaInfo(raw)
	require.Len(t, mi.Audio, MaxStreamsPerKind)
	require.Len(t, mi.Subtitles, MaxStreamsPerKind)
	assert.Equal(t, int32(1), mi.Audio[0].Index, "the first streams are kept, in order")
}

// TestToMediaInfoRecordsTheVideoEncoder: the video stream's ENCODER tag is
// how a file some other tool re-encoded is told from a release
// (MediaInfo.TranscodedElsewhere). Matroska keeps the key as written and
// MP4's muxer lowercases it, so the lookup ignores case; a value past the
// CRD's bound is dropped rather than failing the whole status apply.
func TestToMediaInfoRecordsTheVideoEncoder(t *testing.T) {
	probe := func(tags ffprobe.Tags) *commonv1.MediaInfo {
		return toMediaInfo(&Raw{
			Format:  &ffprobe.Format{Filename: "movie.mkv"},
			Streams: []*ffprobe.Stream{{Index: 0, CodecType: "video", CodecName: "hevc", TagList: tags}},
		})
	}
	assert.Equal(t, "Lavc61.3.100 hevc_qsv", probe(ffprobe.Tags{"ENCODER": "Lavc61.3.100 hevc_qsv"}).VideoEncoder)
	assert.Equal(t, "Lavc60 libx264", probe(ffprobe.Tags{"encoder": " Lavc60 libx264 "}).VideoEncoder)
	assert.Empty(t, probe(nil).VideoEncoder)
	assert.Empty(t, probe(ffprobe.Tags{"ENCODER": strings.Repeat("x", MaxVideoEncoderLength+1)}).VideoEncoder)
}

// loadProbe reads a recorded `ffprobe -show_format -show_streams` JSON
// under test/data/mediainfo.
func loadProbe(t *testing.T, name string) *Raw {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "data", "mediainfo", name))
	require.NoError(t, err)
	var pd ffprobe.ProbeData
	require.NoError(t, json.Unmarshal(b, &pd))
	return &Raw{Format: pd.Format, Streams: pd.Streams}
}

// TestVideoBitrateFromAMatroskaFile: a Matroska stream carries no bit_rate
// -- 11,182 of the owner's 12,449 probed files read 0, so the NVENC bitrate
// cap had nothing to cap from -- but mkvmerge records the stream's average
// as its BPS statistics tag. With neither, the container's bitrate less the
// audio streams' is the estimate. The input is a real WEB-DL's probe.
func TestVideoBitrateFromAMatroskaFile(t *testing.T) {
	raw := loadProbe(t, "ffprobe_mkv_webdl_bps.json")
	require.Empty(t, raw.Streams[0].BitRate, "fixture: a Matroska video stream has no bit_rate")
	assert.Equal(t, int32(4134), toMediaInfo(raw).VideoBitrateKbps, "mkvmerge's BPS tag")

	delete(raw.Streams[0].TagList, "BPS")
	assert.Equal(t, int32((4327758-189375)/1000), toMediaInfo(raw).VideoBitrateKbps,
		"no tag: the container's bitrate less the audio's")

	raw.Format.BitRate = ""
	assert.Zero(t, toMediaInfo(raw).VideoBitrateKbps, "nothing to derive it from")
}
