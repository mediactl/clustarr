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
	"fmt"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

var sourceDisplay = map[commonv1.Source]string{
	commonv1.SourceUnknown:   "Unknown",
	commonv1.SourceCam:       "CAM",
	commonv1.SourceTelesync:  "TELESYNC",
	commonv1.SourceTelecine:  "TELECINE",
	commonv1.SourceWorkprint: "Workprint",
	commonv1.SourceDVD:       "DVD",
	commonv1.SourceTV:        "HDTV",
	commonv1.SourceWebDL:     "WEBDL",
	commonv1.SourceWebRip:    "WEBRip",
	commonv1.SourceBluray:    "Bluray",
}

// qualityFull renders the *arr "Quality Full" token: <source>-<resolution>p
// with a proper/repack/real suffix, following the Radarr/Sonarr naming
// convention. Below-720p TV source collapses to the fixed name "SDTV" with
// no resolution suffix -- SDTV is itself a complete quality-tier name in
// the *arr vocabulary, not "SDTV-480p". That collapse applies only to a
// plain TV source: a Remux or BR-DISK modifier always keeps its resolution
// suffix (e.g. "Remux-480p"), even on a Quality whose Source also happens
// to be tv, since Remux/BR-DISK are themselves complete quality tiers
// distinct from SDTV's "no useful resolution" case.
func qualityFull(q commonv1.Quality, r commonv1.Revision) string {
	base := sourceDisplay[q.Source]
	modified := q.Modifier == commonv1.ModifierRemux || q.Modifier == commonv1.ModifierBRDisk
	sdtv := !modified && q.Source == commonv1.SourceTV && q.Resolution > 0 && q.Resolution < 720
	if sdtv {
		base = "SDTV"
	}
	switch q.Modifier {
	case commonv1.ModifierRemux:
		base = "Remux"
	case commonv1.ModifierBRDisk:
		base = "BR-DISK"
	}
	name := base
	if q.Resolution > 0 && !sdtv {
		name = fmt.Sprintf("%s-%dp", base, q.Resolution)
	}
	switch {
	case r.Real > 0:
		name += " REAL"
	case r.Repack:
		name += " Repack"
	case r.Version > 1:
		name += " Proper"
	}
	return name
}

var hdrDisplay = map[commonv1.HdrFormat]string{
	commonv1.HdrFormatPQ10:                 "PQ",
	commonv1.HdrFormatHDR10:                "HDR10",
	commonv1.HdrFormatHDR10Plus:            "HDR10+",
	commonv1.HdrFormatHLG10:                "HLG",
	commonv1.HdrFormatDolbyVision:          "DV",
	commonv1.HdrFormatDolbyVisionHDR10:     "DV HDR10",
	commonv1.HdrFormatDolbyVisionSDR:       "DV",
	commonv1.HdrFormatDolbyVisionHLG:       "DV HLG",
	commonv1.HdrFormatDolbyVisionHDR10Plus: "DV HDR10+",
}
