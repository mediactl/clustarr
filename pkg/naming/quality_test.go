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

package naming_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

func TestRenderQualityFull(t *testing.T) {
	tests := []struct {
		name string
		q    commonv1.Quality
		r    commonv1.Revision
		want string
	}{
		{"bluray 2160p", commonv1.Quality{Source: commonv1.SourceBluray, Resolution: 2160}, commonv1.Revision{Version: 1}, "Bluray-2160p"},
		{"webdl 1080p proper", commonv1.Quality{Source: commonv1.SourceWebDL, Resolution: 1080}, commonv1.Revision{Version: 2}, "WEBDL-1080p Proper"},
		{"remux 1080p", commonv1.Quality{Source: commonv1.SourceBluray, Resolution: 1080, Modifier: commonv1.ModifierRemux}, commonv1.Revision{Version: 1}, "Remux-1080p"},
		{"sdtv", commonv1.Quality{Source: commonv1.SourceTV, Resolution: 480}, commonv1.Revision{Version: 1}, "SDTV"},
		{"real", commonv1.Quality{Source: commonv1.SourceWebRip, Resolution: 720}, commonv1.Revision{Version: 1, Real: 1}, "WEBRip-720p REAL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := naming.NewEngine(naming.Config{})
			got, err := e.Render("{Quality Full}", naming.Context{Quality: tt.q, Revision: tt.r})
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRenderMediaInfoVideoDynamicRangeType(t *testing.T) {
	tests := []struct {
		hdr  commonv1.HdrFormat
		want string
	}{
		{commonv1.HdrFormatNone, ""},
		{commonv1.HdrFormatHDR10, "[HDR10]"},
		{commonv1.HdrFormatDolbyVisionHDR10, "[DV HDR10]"},
	}
	for _, tt := range tests {
		t.Run(string(tt.hdr), func(t *testing.T) {
			e := naming.NewEngine(naming.Config{})
			c := naming.Context{MediaInfo: commonv1.MediaInfo{Hdr: tt.hdr}}
			got, err := e.Render("{[MediaInfo VideoDynamicRangeType]}", c)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRenderEditionAndReleaseGroupAndCustomFormats(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{
		Edition:       "Ultimate Extended Edition",
		ReleaseGroup:  "RlsGrp",
		CustomFormats: []string{"CF Name", "Atmos"},
	}
	got, err := e.Render("{{Edition Tags}} {[Custom Formats]}{-Release Group}", c)
	require.NoError(t, err)
	require.Equal(t, "{Ultimate Extended Edition} [CF Name Atmos]-RlsGrp", got)
}
