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

package quality_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestLookupResolvesCanonicalAndSonarrAliasNames(t *testing.T) {
	cases := []struct {
		name       string
		lookupName string
		wantSource common.Source
		wantRes    int32
		wantMod    common.Modifier
		wantWeight int
	}{
		{"canonical Bluray-1080p", "Bluray-1080p", common.SourceBluray, common.Resolution1080p, common.ModifierNone, 19},
		{"canonical Remux-1080p", "Remux-1080p", common.SourceBluray, common.Resolution1080p, common.ModifierRemux, 20},
		{"Sonarr alias for Remux-1080p", "Bluray-1080p Remux", common.SourceBluray, common.Resolution1080p, common.ModifierRemux, 20},
		{"Sonarr alias for Remux-2160p", "Bluray-2160p Remux", common.SourceBluray, common.Resolution2160p, common.ModifierRemux, 24},
		{"WEBDL and WEBRip 1080p are distinct definitions", "WEBDL-1080p", common.SourceWebDL, common.Resolution1080p, common.ModifierNone, 18},
		{"BR-DISK", "BR-DISK", common.SourceBluray, common.Resolution1080p, common.ModifierBRDisk, 25},
		{"Raw-HD", "Raw-HD", common.SourceTV, common.Resolution1080p, common.ModifierRawHD, 26},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def, ok := quality.Lookup("video", tc.lookupName)
			require.True(t, ok, "Lookup(%q) should resolve", tc.lookupName)
			require.Equal(t, tc.wantSource, def.Quality.Source)
			require.Equal(t, tc.wantRes, def.Quality.Resolution)
			require.Equal(t, tc.wantMod, def.Quality.Modifier)
			require.Equal(t, tc.wantWeight, def.Weight)
		})
	}
	_, ok := quality.Lookup("video", "Not-A-Real-Quality")
	require.False(t, ok)
}

func TestLookupResolvesNonVideoTables(t *testing.T) {
	cases := []struct {
		kind, name string
		wantWeight int
	}{
		{"music", "FLAC", 6}, {"music", "MP3-192", 5}, {"music", "WAV", 8},
		{"book", "PDF", 1}, {"book", "MOBI", 2}, {"book", "EPUB", 3}, {"book", "AZW3", 4},
		{"audiobook", "MP3", 2}, {"audiobook", "M4B", 3}, {"audiobook", "FLAC", 4},
		{"comic", "PDF", 1}, {"comic", "CBR", 2}, {"comic", "CBZ", 3},
	}
	for _, tc := range cases {
		def, ok := quality.Lookup(tc.kind, tc.name)
		require.Truef(t, ok, "Lookup(%q, %q)", tc.kind, tc.name)
		require.Equal(t, tc.wantWeight, def.Weight)
		require.Equal(t, tc.name, def.Quality.Name)
	}
}
