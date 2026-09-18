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

package release_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

func TestParseMovieTitleYearAndQuality(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  release.ParsedRelease // Title, Year, Quality.Name, Group only checked
	}{
		{
			"remux with edition", "Dune.Part.Two.2024.EXTENDED.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.7.1.Atmos-FraMeSToR",
			release.ParsedRelease{Title: "Dune Part Two", Year: 2024, Group: "FraMeSToR"},
		},
		{
			"webdl streaming service", "Oppenheimer.2023.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX",
			release.ParsedRelease{Title: "Oppenheimer", Year: 2023, Group: "FLUX"},
		},
		{
			"yify style", "The.Matrix.1999.1080p.BluRay.x264.YIFY",
			release.ParsedRelease{Title: "The Matrix", Year: 1999, Group: "YIFY"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := release.ParseKind(tt.title, commonv1.MediaKindMovie)
			require.NoError(t, err)
			assert.Equal(t, tt.want.Title, p.Title)
			assert.Equal(t, tt.want.Year, p.Year)
			assert.Equal(t, tt.want.Group, p.Group)
			assert.Equal(t, commonv1.ReleaseTypeSingle, p.ReleaseType)
		})
	}
}

func TestParseMovieTitlesIncludesAlternateTitles(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		titles []string
	}{
		{
			"AKA alternate title", "Le.Samourai.AKA.The.Samurai.1967.1080p.BluRay.x264-GROUP",
			[]string{"Le Samourai", "The Samurai"},
		},
		{
			"plain title has no alternates", "The.Matrix.1999.1080p.BluRay.x264-GROUP",
			[]string{"The Matrix"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := release.ParseKind(tt.title, commonv1.MediaKindMovie)
			require.NoError(t, err)
			assert.Equal(t, tt.titles[0], p.Title, "Titles[0] must equal Title")
			assert.Equal(t, tt.titles, p.Titles)
		})
	}
}
