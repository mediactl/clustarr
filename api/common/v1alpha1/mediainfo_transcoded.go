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

package v1alpha1

import "strings"

// transcodeEncoders are the libavcodec encoders that produce the formats a
// transcode targets, HEVC and AV1: software and every hardware family.
var transcodeEncoders = map[string]bool{
	"libx265": true, "libkvazaar": true,
	"libsvtav1": true, "libaom-av1": true, "librav1e": true,
}

// TranscodedElsewhere reports whether the probe shows the video was
// transcoded after release by a tool other than squasharr: its stream's
// ENCODER tag names a libavcodec HEVC or AV1 encoder ("Lavc61.3.100
// hevc_qsv", "Lavc60 libx265", "Lavc61 av1_nvenc"). ffmpeg writes that tag
// when it encodes a stream, and a release is encoded by x265 itself or
// never tagged at all -- on the owner's library (2026-09-29) this matched
// Tdarr's 538 QSV outputs and one libx265 encode, and none of the 92 x265
// releases. An ffmpeg H.264 encode (libx264, h264_*) is a release format,
// not a transcode target, and does not count. A plain Go method; it
// generates nothing.
func (mi *MediaInfo) TranscodedElsewhere() bool {
	if mi == nil {
		return false
	}
	fields := strings.Fields(strings.ToLower(mi.VideoEncoder))
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "lavc") {
		return false
	}
	enc := fields[len(fields)-1]
	return transcodeEncoders[enc] || strings.HasPrefix(enc, "hevc_") || strings.HasPrefix(enc, "av1_")
}
