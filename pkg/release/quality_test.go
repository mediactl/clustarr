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

package release

import (
	"testing"

	"github.com/stretchr/testify/assert"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestParseQualityTagsMapsSourceResolutionModifierToRadarrNames(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		source commonv1.Source
		res    int32
		mod    commonv1.Modifier
		qname  string
	}{
		{
			"remux 2160p", "The.Matrix.1999.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.7.1.Atmos-FraMeSToR",
			commonv1.SourceBluray, commonv1.Resolution2160p, commonv1.ModifierRemux, "Remux-2160p",
		},
		{
			"webdl 1080p", "Severance.S02E03.Chikhai.Bardo.1080p.ATVP.WEB-DL.DDP5.1.Atmos.H.264-NTb",
			commonv1.SourceWebDL, commonv1.Resolution1080p, commonv1.ModifierNone, "WEBDL-1080p",
		},
		{
			"hdtv 720p", "The.Bear.S01E01.System.720p.HULU.WEBRip.x264-CAKES",
			commonv1.SourceWebRip, commonv1.Resolution720p, commonv1.ModifierNone, "WEBRip-720p",
		},
		{
			"bluray 1080p", "Dune.Part.Two.2024.1080p.BluRay.x264-GROUP",
			commonv1.SourceBluray, commonv1.Resolution1080p, commonv1.ModifierNone, "Bluray-1080p",
		},
		{
			"br-disk", "Oppenheimer.2023.COMPLETE.BLURAY-GROUP",
			commonv1.SourceBluray, commonv1.Resolution1080p, commonv1.ModifierBRDisk, "BR-DISK",
		},
		{
			"dvdscr", "Some.Movie.2020.DVDSCR.XviD-GROUP",
			commonv1.SourceDVD, commonv1.Resolution480p, commonv1.ModifierScreener, "DVDSCR",
		},
		{
			"telesync", "Some.Movie.2020.TELESYNC.x264-GROUP",
			commonv1.SourceTelesync, commonv1.ResolutionUnknown, commonv1.ModifierNone, "TELESYNC",
		},
		{
			"sdtv", "Some.Show.S01E01.HDTV.XviD-GROUP",
			commonv1.SourceTV, commonv1.ResolutionUnknown, commonv1.ModifierNone, "SDTV",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, _, _, _ := parseQualityTags(tt.title)
			assert.Equal(t, tt.source, q.Source, "source")
			assert.Equal(t, tt.res, q.Resolution, "resolution")
			assert.Equal(t, tt.mod, q.Modifier, "modifier")
			assert.Equal(t, tt.qname, q.Name, "quality name")
		})
	}
}

func TestParseQualityTagsRevisionHandlesProperRepackRealVersion(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  commonv1.Revision
	}{
		{"plain", "The.Matrix.1999.1080p.BluRay.x264-GROUP", commonv1.Revision{Version: 1}},
		{"proper", "The.Matrix.1999.PROPER.1080p.BluRay.x264-GROUP", commonv1.Revision{Version: 2}},
		{"repack", "The.Matrix.1999.REPACK.1080p.BluRay.x264-GROUP", commonv1.Revision{Version: 2, Repack: true}},
		{
			"repack2 explicit version", "The.Matrix.1999.REPACK2.1080p.BluRay.x264-GROUP",
			commonv1.Revision{Version: 2, Repack: true},
		},
		{
			"real proper", "The.Matrix.1999.REAL.PROPER.1080p.BluRay.x264-GROUP",
			commonv1.Revision{Version: 2, Real: 1},
		},
		{
			"double real", "The.Matrix.1999.REAL.REAL.PROPER.1080p.BluRay.x264-GROUP",
			commonv1.Revision{Version: 2, Real: 2},
		},
		{
			"lowercase real is not a revision", "The.Matrix.1999.real.proper.1080p.BluRay.x264-GROUP",
			commonv1.Revision{Version: 2, Real: 0},
		}, // RealRegex is case-sensitive in *arr
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, rev, _, _ := parseQualityTags(tt.title)
			assert.Equal(t, tt.want, rev)
		})
	}
}
