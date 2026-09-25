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
	"strings"
)

// FormatVideoCodec is Radarr's MediaInfoFormatter.FormatVideoCodec: the
// probe names the codec, and the release title says whether an AVC or HEVC
// stream was an x264/x265 encode, which the file itself cannot.
func FormatVideoCodec(codecName, videoProfile, releaseTitle string) string {
	title := strings.ToLower(releaseTitle)
	switch strings.ToLower(codecName) {
	case "h264", "avc":
		if strings.Contains(title, "x264") {
			return "x264"
		}
		return "h264"
	case "hevc", "h265":
		if strings.Contains(title, "x265") {
			return "x265"
		}
		return "h265"
	case "av1":
		return "AV1"
	case "vp9":
		return "VP9"
	case "vc1":
		return "VC1"
	case "mpeg2video":
		return "MPEG2"
	case "mpeg4", "msmpeg4v3":
		if strings.Contains(title, "divx") {
			return "DivX"
		}
		return "XviD"
	case "":
		return ""
	}
	return strings.ToUpper(codecName)
}
