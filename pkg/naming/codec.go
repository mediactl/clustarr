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

package naming

import (
	"strings"
)

// VideoCodecLabel is Radarr's MediaInfoFormatter.FormatVideoCodec, ported
// here (ruling R5, docs/superpowers/sdd/2026-09-24-probe-driven-naming) so
// the {MediaInfo VideoCodec} token has one implementation: the probe names
// the codec (h264, hevc, ...), but only the release's own title -- the
// download client item's name, or the .nzb/.torrent name, carried on
// Context.ReleaseTitle -- says whether an AVC or HEVC stream was actually
// an x264/x265 encode, which the file itself cannot say.
//
// pkg/mediainfo.FormatVideoCodec (Task 2) delegates to this function rather
// than the other way around: pkg/naming stays a pure package importing
// only api/common/v1alpha1, and must not import pkg/mediainfo, which pulls
// in pkg/events and tracing.
func VideoCodecLabel(codecName, videoProfile, releaseTitle string) string {
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
