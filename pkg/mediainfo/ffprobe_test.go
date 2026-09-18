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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

// doviStreamsJSON is a hand-authored ffprobe "streams" JSON: one HEVC
// Main 10 video stream carrying a Dolby Vision profile 8.1 "DOVI
// configuration record" side data entry. Field names and shape are from
// docs/research/transcode.md §2.2's side-data table
// (libavutil/side_data.c / fftools/ffprobe.c), not live-captured -- this
// box has no dovi_tool to synthesise a real DV stream (verified: ffmpeg
// -dolbyvision 1 here fails with "Dolby Vision requires VBV settings" and
// no real RPU to encode).
const doviStreamsJSON = `{
  "streams": [
    {
      "index": 0,
      "codec_name": "hevc",
      "codec_type": "video",
      "profile": "Main 10",
      "width": 3840,
      "height": 2160,
      "pix_fmt": "yuv420p10le",
      "side_data_list": [
        {
          "side_data_type": "DOVI configuration record",
          "dv_version_major": 1,
          "dv_version_minor": 0,
          "dv_profile": 8,
          "dv_level": 6,
          "rpu_present_flag": 1,
          "el_present_flag": 0,
          "bl_present_flag": 1,
          "dv_bl_signal_compatibility_id": 1,
          "dv_md_compression": "none"
        }
      ]
    }
  ],
  "format": {"filename": "dv.mkv", "format_name": "matroska,webm", "duration": "1.0", "size": "1", "bit_rate": "1"},
  "chapters": []
}`

func TestBuildRawParsesDoviConfigurationRecord(t *testing.T) {
	var pd ffprobe.ProbeData
	require.NoError(t, json.Unmarshal([]byte(doviStreamsJSON), &pd))

	raw := buildRaw(&pd)

	require.NotNil(t, raw.Dovi)
	assert.Equal(t, int32(1), raw.Dovi.VersionMajor)
	assert.Equal(t, int32(8), raw.Dovi.Profile)
	assert.Equal(t, int32(6), raw.Dovi.Level)
	assert.True(t, raw.Dovi.RPUPresent)
	assert.False(t, raw.Dovi.ELPresent)
	assert.True(t, raw.Dovi.BLPresent)
	assert.Equal(t, int32(1), raw.Dovi.BLSignalCompatibilityID)
	assert.Equal(t, "none", raw.Dovi.MDCompression)
}

func TestBuildRawWithNoDoviSideDataLeavesDoviNil(t *testing.T) {
	pd := &ffprobe.ProbeData{
		Streams: []*ffprobe.Stream{{Index: 0, CodecType: "video", CodecName: "h264"}},
		Format:  &ffprobe.Format{Filename: "plain.mp4"},
	}
	raw := buildRaw(pd)
	assert.Nil(t, raw.Dovi)
	assert.Same(t, pd.Format, raw.Format)
}

// hdr10FrameJSON is real ffprobe 9.0.1 output (the second command in
// docs/research/transcode.md §2.1) captured on this box against an
// x265-encoded clip tagged exactly as docs/research/transcode.md §2.2
// describes (colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc,
// master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1),
// max-cll=1000,400) -- the side-data values are exactly the note's own
// worked example, independently reproduced.
const hdr10FrameJSON = `{
  "frames": [
    {
      "pix_fmt": "yuv420p10le",
      "color_range": "tv",
      "color_space": "bt2020nc",
      "color_primaries": "bt2020",
      "color_transfer": "smpte2084",
      "side_data_list": [
        {"side_data_type": "H.26[45] User Data Unregistered SEI message"},
        {"side_data_type": "Mastering display metadata", "red_x": "34000/50000", "red_y": "16000/50000", "green_x": "13250/50000", "green_y": "34500/50000", "blue_x": "7500/50000", "blue_y": "3000/50000", "white_point_x": "15635/50000", "white_point_y": "16450/50000", "min_luminance": "1/10000", "max_luminance": "10000000/10000"},
        {"side_data_type": "Content light level metadata", "max_content": 1000, "max_average": 400}
      ]
    }
  ]
}`

func TestMergeFrameExtractsColourAndStaticHDRMetadata(t *testing.T) {
	var fd frameProbeData
	require.NoError(t, json.Unmarshal([]byte(hdr10FrameJSON), &fd))

	raw := &Raw{}
	mergeFrame(raw, fd)

	assert.Equal(t, "bt2020", raw.ColorPrimaries)
	assert.Equal(t, "smpte2084", raw.ColorTransfer)
	assert.Equal(t, "bt2020nc", raw.ColorSpace)
	assert.Equal(t, "tv", raw.ColorRange)
	require.NotNil(t, raw.MasteringDisplay)
	assert.Equal(t, "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1)", raw.MasteringDisplay.X265())
	require.NotNil(t, raw.ContentLight)
	assert.Equal(t, "1000,400", raw.ContentLight.X265())
	assert.False(t, raw.HasHDR10Plus)
}

func TestMergeFrameDetectsHDR10Plus(t *testing.T) {
	const j = `{"frames":[{"color_transfer":"smpte2084","side_data_list":[{"side_data_type":"HDR Dynamic Metadata SMPTE2094-40 (HDR10+)"}]}]}`
	var fd frameProbeData
	require.NoError(t, json.Unmarshal([]byte(j), &fd))

	raw := &Raw{}
	mergeFrame(raw, fd)

	assert.True(t, raw.HasHDR10Plus)
}

func TestMergeFrameWithNoFramesLeavesRawUnchanged(t *testing.T) {
	raw := &Raw{}
	mergeFrame(raw, frameProbeData{})
	assert.Empty(t, raw.ColorTransfer)
	assert.Nil(t, raw.MasteringDisplay)
}
