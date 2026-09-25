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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatVideoCodecFollowsRadarr(t *testing.T) {
	for _, tc := range []struct{ codec, profile, title, want string }{
		{"h264", "High", "Movie.2010.1080p.BluRay.x264-GRP", "x264"},
		{"h264", "High", "Movie.2010.1080p.WEB-DL.H.264-GRP", "h264"},
		{"h264", "High", "", "h264"},
		{"hevc", "Main 10", "Movie.2010.2160p.x265-GRP", "x265"},
		{"hevc", "Main 10", "2ef6f194995e4a11b055d0f2354ef0ba", "h265"},
		{"av1", "", "", "AV1"},
		{"vp9", "", "", "VP9"},
		{"mpeg4", "Advanced Simple Profile", "Movie.XviD-GRP", "XviD"},
		{"mpeg4", "Advanced Simple Profile", "Movie.DivX-GRP", "DivX"},
		{"vc1", "", "", "VC1"},
		{"mpeg2video", "", "", "MPEG2"},
		{"prores", "", "", "PRORES"},
		{"", "", "", ""},
	} {
		require.Equal(t, tc.want, FormatVideoCodec(tc.codec, tc.profile, tc.title), "%s/%s/%s", tc.codec, tc.profile, tc.title)
	}
}
