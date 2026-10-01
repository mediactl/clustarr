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

package transcode_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/transcode"
)

func TestNVDECKey(t *testing.T) {
	for _, tt := range []struct {
		codec, pixFmt, want string
	}{
		{"h264", "yuv420p", "h264:8"},
		{"h264", "yuvj420p", "h264:8"},
		{"h264", "yuv420p10le", "h264:10"},
		{"hevc", "yuv420p10le", "hevc:10"},
		{"hevc", "yuv420p12le", "hevc:12"},
		{"av1", "yuv420p", "av1:8"},
		{"h264", "yuv422p", ""},
		{"hevc", "yuv444p10le", ""},
		{"h264", "", ""},
	} {
		assert.Equal(t, tt.want, transcode.NVDECKey(transcode.VideoStream{Codec: tt.codec, PixFmt: tt.pixFmt}), "%s %s", tt.codec, tt.pixFmt)
	}
}

func TestNVDECDecodes(t *testing.T) {
	measured := func(formats map[string]bool) transcode.Limits {
		return transcode.Limits{NVDEC: &transcode.Decoders{Formats: formats}}
	}
	h264 := transcode.VideoStream{Codec: "h264", PixFmt: "yuv420p"}
	hi10p := transcode.VideoStream{Codec: "h264", PixFmt: "yuv420p10le"}
	av1 := transcode.VideoStream{Codec: "av1", PixFmt: "yuv420p"}
	vc1 := transcode.VideoStream{Codec: "vc1", PixFmt: "yuv420p"}
	h264422 := transcode.VideoStream{Codec: "h264", PixFmt: "yuv422p"}

	for _, tt := range []struct {
		name   string
		limits transcode.Limits
		v      transcode.VideoStream
		want   bool
	}{
		{"unmeasured: the static list decodes 8-bit H.264", transcode.Limits{}, h264, true},
		{"unmeasured: the static list never decodes Hi10P", transcode.Limits{}, hi10p, false},
		{"unmeasured: AV1 needs a measurement", transcode.Limits{}, av1, false},
		{"a measured failure overrides the static list", measured(map[string]bool{"h264:8": false}), h264, false},
		{"a measured success decodes what the static list does not", measured(map[string]bool{"av1:8": true}), av1, true},
		{"a format the trial did not test falls back to the static list", measured(map[string]bool{"h264:8": true}), vc1, true},
		{"4:2:2 is never decoded on NVDEC, whatever was measured", measured(map[string]bool{"h264:8": true}), h264422, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, transcode.NVDECDecodes(tt.limits, tt.v))
		})
	}
}
