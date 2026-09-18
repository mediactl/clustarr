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

// hdrDisplayName is the single source of truth for rendering an HdrFormat
// as a human-facing string. It is the {MediaInfo VideoDynamicRangeType}
// naming token, ported from Radarr's
// MediaInfoFormatter.FormatVideoDynamicRangeType.
//
// "DV HDR10", "HDR10+" and "HLG" are pinned by docs/research/naming.md
// (the Radarr file-naming examples at lines 55 and 81 and the
// MediaInfoResource field at line 443); the remainder follow Radarr's own
// vocabulary ("PQ", "DV", "DV SDR", "DV HLG", "HDR10", "" for none), with
// "DV HDR10+" composed from the DV prefix and the pinned "HDR10+".
//
// This is a plain Go method, not part of the API surface: it carries no
// kubebuilder markers and generates nothing.
var hdrDisplayName = map[HdrFormat]string{
	HdrFormatNone:                 "",
	HdrFormatPQ10:                 "PQ",
	HdrFormatHDR10:                "HDR10",
	HdrFormatHDR10Plus:            "HDR10+",
	HdrFormatHLG10:                "HLG",
	HdrFormatDolbyVision:          "DV",
	HdrFormatDolbyVisionHDR10:     "DV HDR10",
	HdrFormatDolbyVisionSDR:       "DV SDR",
	HdrFormatDolbyVisionHLG:       "DV HLG",
	HdrFormatDolbyVisionHDR10Plus: "DV HDR10+",
}

// DisplayName renders h as the {MediaInfo VideoDynamicRangeType} naming
// token. An unset or unrecognised format renders as the empty string, so
// a caller can concatenate the result unconditionally.
func (h HdrFormat) DisplayName() string { return hdrDisplayName[h] }
