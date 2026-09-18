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
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// bitDepthFromPixFmt returns the video bit depth ffprobe's pix_fmt
// implies: "yuv420p" -> 8 (the implicit default), "yuv420p10le" -> 10,
// "...12le" -> 12.
func bitDepthFromPixFmt(pixFmt string) int32 {
	switch {
	case strings.Contains(pixFmt, "12le"), strings.Contains(pixFmt, "12be"):
		return 12
	case strings.Contains(pixFmt, "10le"), strings.Contains(pixFmt, "10be"):
		return 10
	default:
		return 8
	}
}

// frameRateMilli parses ffprobe's r_frame_rate ("24/1", "24000/1001")
// into thousandths of a frame per second (23.976 fps -> 23976), matching
// api/common/v1alpha1.MediaInfo.FpsMilli's scaled-integer convention.
func frameRateMilli(rFrameRate string) int32 {
	num, den, ok := strings.Cut(rFrameRate, "/")
	if !ok {
		return 0
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return int32(math.Round(n / d * 1000))
}

// kbpsFromBitRate parses ffprobe's decimal bit_rate string (bits/second)
// into whole kilobits/second. Some containers (MKV video streams
// especially) omit bit_rate entirely; that parses to 0, not an error.
func kbpsFromBitRate(bitRate string) int32 {
	v, err := strconv.ParseInt(bitRate, 10, 64)
	if err != nil {
		return 0
	}
	return int32(v / 1000)
}

// containerFromPath derives the container token from the file extension.
// ffprobe's format_name lists every demuxer alias a container matches
// ("mov,mp4,m4a,3gp,3g2,mj2" for an .mp4), which is not usable as-is.
func containerFromPath(path string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
}

// ClassifyHDR derives the api/common HDR vocabulary from raw's Dolby
// Vision record and frame-level colour tags, per
// docs/research/transcode.md §2.3 "Deriving HDRFormat" and §3.5's
// profile/compat-id table.
func ClassifyHDR(raw *Raw) commonv1.HdrFormat {
	if raw == nil {
		return commonv1.HdrFormatNone
	}
	if raw.Dovi != nil {
		switch raw.Dovi.Profile {
		case 5:
			return commonv1.HdrFormatDolbyVision
		case 7:
			// FFmpeg's decoder drops the enhancement layer, so a profile 7
			// base layer decodes as HDR10 (docs/research/transcode.md §3.5).
			return commonv1.HdrFormatDolbyVisionHDR10
		}
		switch raw.Dovi.BLSignalCompatibilityID {
		case 1:
			return commonv1.HdrFormatDolbyVisionHDR10
		case 2:
			return commonv1.HdrFormatDolbyVisionSDR
		case 4:
			return commonv1.HdrFormatDolbyVisionHLG
		default:
			return commonv1.HdrFormatDolbyVision
		}
	}
	switch {
	case raw.HasHDR10Plus:
		return commonv1.HdrFormatHDR10Plus
	case raw.ColorTransfer == "smpte2084":
		if raw.MasteringDisplay != nil {
			return commonv1.HdrFormatHDR10
		}
		return commonv1.HdrFormatPQ10
	case raw.ColorTransfer == "arib-std-b67":
		return commonv1.HdrFormatHLG10
	default:
		return commonv1.HdrFormatNone
	}
}

// videoDynamicRangeType maps the HDR enum onto the *arr naming-token
// display strings docs/research/naming.md documents (line 443: "DV
// HDR10", "HDR10+", "HLG"; the {MediaInfo VideoDynamicRangeType} token).
var videoDynamicRangeType = map[commonv1.HdrFormat]string{
	commonv1.HdrFormatNone:                 "",
	commonv1.HdrFormatPQ10:                 "PQ10",
	commonv1.HdrFormatHDR10:                "HDR10",
	commonv1.HdrFormatHDR10Plus:            "HDR10+",
	commonv1.HdrFormatHLG10:                "HLG",
	commonv1.HdrFormatDolbyVision:          "DV",
	commonv1.HdrFormatDolbyVisionHDR10:     "DV HDR10",
	commonv1.HdrFormatDolbyVisionSDR:       "DV SDR",
	commonv1.HdrFormatDolbyVisionHLG:       "DV HLG",
	commonv1.HdrFormatDolbyVisionHDR10Plus: "DV HDR10+",
}

// VideoDynamicRangeType renders hdr as pkg/naming's
// {MediaInfo VideoDynamicRangeType} token expects.
func VideoDynamicRangeType(hdr commonv1.HdrFormat) string {
	return videoDynamicRangeType[hdr]
}

// AudioChannelsString renders a channel layout as the *arr display
// string ("5.1", "7.1", "2.0"). It prefers ffprobe's channel_layout,
// stripping any positional suffix ("5.1(side)" -> "5.1"; see
// docs/research/transcode.md §2.2), and falls back to "<channels>.0"
// when the layout is empty or unrecognized.
func AudioChannelsString(channelLayout string, channels int32) string {
	switch channelLayout {
	case "mono":
		return "1.0"
	case "stereo":
		return "2.0"
	}
	if i := strings.IndexByte(channelLayout, '('); i >= 0 {
		channelLayout = channelLayout[:i]
	}
	if channelLayout != "" {
		return channelLayout
	}
	return fmt.Sprintf("%d.0", channels)
}
