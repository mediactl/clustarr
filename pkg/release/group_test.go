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
)

func TestParseGroupExtractsReleaseGroupEditionAndHashFallback(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		group   string
		hash    string
		edition string
	}{
		{"standard scene group", "Dune.Part.Two.2024.1080p.BluRay.x264-GROUP", "GROUP", "", ""},
		{"anime bracket group", "[SubsPlease] Frieren - 28 (1080p) [F02B9CDC].mkv", "SubsPlease", "", ""},
		{"8-hex hash is not a group", "Some.Movie.2020.1080p.WEB-DL.x264-a1b2c3d4", "", "a1b2c3d4", ""},
		{"directors cut edition", "Blade.Runner.1982.Directors.Cut.1080p.BluRay.x264-GROUP", "GROUP", "", "Director's Cut"},
		{"exception group with digits", "Some.Movie.2020.1080p.BluRay.x264-126811", "126811", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group, hash, edition := parseGroup(tt.title)
			assert.Equal(t, tt.group, group)
			assert.Equal(t, tt.hash, hash)
			assert.Equal(t, tt.edition, edition)
		})
	}
}

// TestParseGroupArrFileLayouts is the carried defect "pkg/release mis-parses
// the release group for both common *arr filename layouts": Radarr's and
// Sonarr's own renamed-file formats put the quality ("Bluray-1080p") at the
// end, where the old trailing "-GROUP" pattern read "1080p" (or
// "1080p-RlsGrp") as the group. The expected values are what Radarr's
// ReleaseGroupParser returns for each title; the corpus is the one
// importarr's rescan guard carried while this was broken.
func TestParseGroupArrFileLayouts(t *testing.T) {
	tests := []struct {
		name  string
		title string
		group string
	}{
		{"radarr layout, quality last, no group", "Heat (1995) - Bluray-1080p", ""},
		{"radarr layout with extension", "Heat (1995) - Bluray-1080p.mkv", ""},
		{"radarr layout, bracketed quality then group", "Heat (1995) - [Bluray-1080p]-RlsGrp", "RlsGrp"},
		{"dotted layout, quality then group", "Heat.1995.Bluray-1080p-RlsGrp", "RlsGrp"},
		{"dotted layout with extension", "Heat.1995.Bluray-1080p-RlsGrp.mkv", "RlsGrp"},
		{"quality suffix without a dash layout", "Heat (1995) WEBDL-720p", ""},
		{"2160p quality suffix", "Heat (1995) Remux-2160p", ""},
		{"sonarr standard layout", "Breaking Bad (2008) - S01E01 - Pilot [Bluray-1080p]", ""},
		{"sonarr layout with group", "Breaking Bad (2008) - S01E01 - Pilot [Bluray-1080p]-RlsGrp", "RlsGrp"},
		{"sonarr layout with a hyphenated episode title", "Show - S01E01 - Spider-Man Returns [HDTV-720p]", ""},
		{"web-dl token is not a group", "Heat.1995.1080p.WEB-DL.DDP5.1.H.264", ""},
		{"dts-hd token is not a group", "Heat.1995.1080p.BluRay.DTS-HD.MA.5.1.x264", ""},
		{"language suffix is not a group", "Heat.1995.1080p.BluRay.x264-ENG", ""},
		{"ordinary scene release", "Heat.1995.1080p.BluRay.x264-SPARKS", "SPARKS"},
		{"2160p scene release", "Heat.1995.2160p.UHD.BluRay.x265-TERMiNAL", "TERMiNAL"},
		{"web-dl scene release", "Heat.1995.1080p.WEB-DL.DDP5.1-NTb", "NTb"},
		{"dvdrip scene release", "Heat.1995.DVDRip.XviD-FraMeSToR", "FraMeSToR"},
		{"two-part group", "Heat.1995.1080p.BluRay.x264-D-Z0N3", "D-Z0N3"},
		{"trailing bracketed group", "Heat 1995 1080p BluRay x264 [FGT]", "FGT"},
		{"public-tracker suffix is not the group", "Heat.1995.1080p.BluRay.x264-SPARKS[rarbg]", "SPARKS"},
		{"reposter suffix is not the group", "Heat.1995.1080p.BluRay.x264-SPARKS-Obfuscated", "SPARKS"},
		{"a bare number is not a group", "Heat.1995.1080p.BluRay.x264-2", ""},
		{"wrapped exception group", "Heat (1995) (1080p BluRay x265 10bit Tigole)", "Tigole"},
		{"website prefix is not an anime group", "[ www.Torrenting.com ] - Heat.1995.1080p.BluRay.x264-SPARKS", "SPARKS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group, _, _ := parseGroup(tt.title)
			assert.Equal(t, tt.group, group)
		})
	}
}
