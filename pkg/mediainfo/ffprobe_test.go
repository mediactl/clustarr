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
