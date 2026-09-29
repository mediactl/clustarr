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

package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// TestMediaInfoTranscodedElsewhere: a video stream ffmpeg encoded to HEVC
// or AV1 was transcoded after release, by whatever did it. The encoder
// tags are the owner's movie library's (2026-09-29): 538 files Tdarr
// re-encoded with Intel QSV, one libx265 encode, 92 x265 releases with no
// tag, and 7 H.264 releases that were made with ffmpeg's libx264.
func TestMediaInfoTranscodedElsewhere(t *testing.T) {
	for _, tc := range []struct {
		encoder string
		want    bool
	}{
		{"Lavc61.3.100 hevc_qsv", true},
		{"Lavc60.31.102 hevc_nvenc", true},
		{"Lavc59.37.100 hevc_vaapi", true},
		{"Lavc61.3.100 libx265", true},
		{"Lavc61.3.100 av1_qsv", true},
		{"Lavc61.3.100 libsvtav1", true},
		{"Lavc61.3.100 libaom-av1", true},
		{"lavc61.3.100 HEVC_QSV", true},
		{"Lavc61.3.100 libx264", false},
		{"Lavc58.54.100 h264_nvenc", false},
		{"HEVC Coding", false},
		{"AVC Coding", false},
		{"JVT/AVC Coding", false},
		{"libx265", false},
		{"", false},
	} {
		mi := &commonv1.MediaInfo{VideoEncoder: tc.encoder}
		assert.Equalf(t, tc.want, mi.TranscodedElsewhere(), "%q", tc.encoder)
	}
	var none *commonv1.MediaInfo
	assert.False(t, none.TranscodedElsewhere(), "an unprobed file")
}
